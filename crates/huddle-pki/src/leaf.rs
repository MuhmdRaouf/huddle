//! The leaf certificate the router serves for `api.anthropic.com`.
//!
//! The leaf is a short-lived server certificate signed by the router's local CA. It is cached
//! with the CA and re-signed only once little of its life is left, so a CONNECT to the API host
//! gets a handshake without minting a key per request.

use std::path::PathBuf;
use std::sync::{Arc, Mutex, PoisonError};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use rcgen::string::PrintableString;
use rcgen::{
    CertificateParams, DistinguishedName, DnType, DnValue, ExtendedKeyUsagePurpose, IsCa, Issuer,
    KeyPair, KeyUsagePurpose, PKCS_ECDSA_P256_SHA256, date_time_ymd,
};
use rustls::pki_types::{CertificateDer, PrivateKeyDer, PrivatePkcs8KeyDer};
use rustls::sign::CertifiedKey;

use crate::ca::{PkiError, ROUTER_HOST, RouterCa, cert_facts, load_or_create};

/// Seven days.
const LEAF_LIFETIME_SECS: i64 = 7 * 86_400;

/// One hour before `now`, so the leaf is already valid when a client connects.
const BACKDATED_SECS: i64 = 3600;

/// The cache keeps a leaf while more than a day of its life is left.
const KEEP_BEYOND_SECS: i64 = 86_400;

/// A failed CA load is retried after a minute.
const RETRY_AFTER: Duration = Duration::from_secs(60);

/// The DER tag of an ASN.1 PrintableString.
const TAG_PRINTABLE_STRING: u8 = 0x13;

/// The DER tag of an ASN.1 UTF8String.
const TAG_UTF8_STRING: u8 = 0x0C;

/// The server certificate for `ROUTER_HOST` and the CA that signed it.
pub struct Leaf {
    /// The chain the TLS side serves: the leaf, then the CA.
    pub certified: Arc<CertifiedKey>,
    /// The leaf's expiry, in seconds since the unix epoch.
    pub not_after_unix: i64,
}

impl RouterCa {
    /// Signs a short-lived server certificate for `host`, which must be `ROUTER_HOST`. The leaf
    /// lives at most a week and never past the CA.
    pub fn leaf(&self, host: &str, now: SystemTime) -> Result<Leaf, PkiError> {
        if host != ROUTER_HOST {
            return Err(PkiError::WrongHost(host.to_owned()));
        }
        let leaf_key = KeyPair::generate_for(&PKCS_ECDSA_P256_SHA256)
            .map_err(|e| PkiError::Sign(e.to_string()))?;
        let mut params = CertificateParams::new(vec![ROUTER_HOST.to_owned()])
            .map_err(|e| PkiError::Sign(e.to_string()))?;
        let mut name = DistinguishedName::new();
        name.push(DnType::CommonName, ROUTER_HOST);
        params.distinguished_name = name;
        params.key_usages = vec![KeyUsagePurpose::DigitalSignature];
        params.extended_key_usages = vec![ExtendedKeyUsagePurpose::ServerAuth];
        params.is_ca = IsCa::NoCa;
        let (start_year, start_month, start_day) = day_of(unix_seconds(now) - BACKDATED_SECS);
        params.not_before = date_time_ymd(start_year, start_month, start_day);
        let end_unix = (unix_seconds(now) + LEAF_LIFETIME_SECS).min(self.not_after_unix());
        let (end_year, end_month, end_day) = day_of(end_unix);
        params.not_after = date_time_ymd(end_year, end_month, end_day);
        let issuer_params = leaf_issuer(self)?;
        let issuer = Issuer::from_params(&issuer_params, self.key());
        let cert = params
            .signed_by(&leaf_key, &issuer)
            .map_err(|e| PkiError::Sign(e.to_string()))?;
        let chain = vec![
            CertificateDer::from(cert.der().to_vec()),
            CertificateDer::from(self.cert_der().to_vec()),
        ];
        let signing_key = rustls::crypto::ring::sign::any_ecdsa_type(&PrivateKeyDer::Pkcs8(
            PrivatePkcs8KeyDer::from(leaf_key.serialize_der()),
        ))
        .map_err(|e| PkiError::Sign(e.to_string()))?;
        Ok(Leaf {
            certified: Arc::new(CertifiedKey::new(chain, signing_key)),
            not_after_unix: cert_facts(cert.der())?.not_after_unix,
        })
    }
}

/// The issuer that signs a leaf with the CA's key. Its subject carries the CA's commonName
/// value under the CA's own string tag, so the leaf's issuer bytes equal the CA's subject
/// bytes and a verifier joins the chain.
fn leaf_issuer(ca: &RouterCa) -> Result<CertificateParams, PkiError> {
    let (tag, cn_bytes) = ca.subject_cn();
    let cn = str::from_utf8(cn_bytes)
        .map_err(|e| PkiError::Sign(format!("the CA subject commonName is not UTF-8: {e}")))?;
    let cn_value = match tag {
        TAG_PRINTABLE_STRING => DnValue::PrintableString(
            PrintableString::try_from(cn).map_err(|e| PkiError::Sign(e.to_string()))?,
        ),
        TAG_UTF8_STRING => DnValue::Utf8String(cn.to_owned()),
        tag => {
            return Err(PkiError::Sign(format!(
                "the CA subject commonName has an unsupported string tag 0x{tag:02X}"
            )));
        }
    };
    let mut params =
        CertificateParams::new(Vec::new()).map_err(|e| PkiError::Sign(e.to_string()))?;
    let mut name = DistinguishedName::new();
    name.push(DnType::CommonName, cn_value);
    params.distinguished_name = name;
    Ok(params)
}

/// The cache of one loaded CA and the leaf it signed, with the one-minute retry for a CA that
/// will not load and one log line per failure episode.
pub struct LeafCache {
    ca_dir: PathBuf,
    note: Arc<dyn Fn(&str) + Send + Sync>,
    state: Mutex<CacheState>,
}

struct CacheState {
    /// The loaded CA, kept once loaded.
    ca: Option<RouterCa>,
    /// The last failed load and when it failed.
    ca_err: Option<(PkiError, SystemTime)>,
    /// The failure is already in the log.
    logged: bool,
    leaf: Option<Arc<Leaf>>,
}

impl LeafCache {
    pub fn new(ca_dir: PathBuf, note: Arc<dyn Fn(&str) + Send + Sync>) -> LeafCache {
        LeafCache {
            ca_dir,
            note,
            state: Mutex::new(CacheState {
                ca: None,
                ca_err: None,
                logged: false,
                leaf: None,
            }),
        }
    }

    /// The leaf for `ROUTER_HOST` at `now`. It reuses the cached leaf while more than a day of
    /// its life is left, retries a failed CA load after a minute, and reports a failing CA once
    /// per failure episode.
    pub fn get(&self, now: SystemTime) -> Result<Arc<Leaf>, PkiError> {
        let mut state = self.state.lock().unwrap_or_else(PoisonError::into_inner);
        let now_unix = unix_seconds(now);
        if let Some(leaf) = &state.leaf
            && leaf.not_after_unix - now_unix > KEEP_BEYOND_SECS
        {
            return Ok(Arc::clone(leaf));
        }
        let ca = match state.ca.as_ref() {
            Some(ca) => ca,
            None => {
                if let Some((err, ca_at)) = &state.ca_err
                    && inside(now, *ca_at, RETRY_AFTER)
                {
                    return Err(err.clone());
                }
                match load_or_create(&self.ca_dir, now) {
                    Ok(ca) => {
                        state.ca_err = None;
                        state.logged = false;
                        &*state.ca.insert(ca)
                    }
                    Err(err) => {
                        if !state.logged {
                            (self.note)(&format!(
                                "local CA unusable ({err}); CONNECT {ROUTER_HOST} passes through as a blind tunnel"
                            ));
                            state.logged = true;
                        }
                        state.ca_err = Some((err.clone(), now));
                        return Err(err);
                    }
                }
            }
        };
        let leaf = Arc::new(ca.leaf(ROUTER_HOST, now)?);
        state.leaf = Some(Arc::clone(&leaf));
        Ok(leaf)
    }
}

/// Whether `now` lies within `span` of `then`; a `now` before `then` counts as inside.
fn inside(now: SystemTime, then: SystemTime, span: Duration) -> bool {
    match now.duration_since(then) {
        Ok(elapsed) => elapsed < span,
        Err(_) => true,
    }
}

/// The UTC calendar day of `secs`, at midnight: rcgen takes a date, and flooring keeps
/// `not_before <= now` and the life within a day of its span.
fn day_of(secs: i64) -> (i32, u8, u8) {
    civil_from_days(secs.div_euclid(86_400))
}

/// The seconds since the unix epoch of `t`, negative before the epoch.
fn unix_seconds(t: SystemTime) -> i64 {
    match t.duration_since(UNIX_EPOCH) {
        Ok(since_epoch) => since_epoch.as_secs() as i64,
        Err(e) => -(e.duration().as_secs() as i64),
    }
}

/// The (year, month, day) of the whole days since the unix epoch.
fn civil_from_days(days: i64) -> (i32, u8, u8) {
    let days = days + 719_468;
    let era = days.div_euclid(146_097);
    let day_of_era = days.rem_euclid(146_097);
    let year_of_era =
        (day_of_era - day_of_era / 1460 + day_of_era / 36_524 - day_of_era / 146_096) / 365;
    let year = year_of_era + era * 400;
    let day_of_year = day_of_era - (365 * year_of_era + year_of_era / 4 - year_of_era / 100);
    let month_prime = (5 * day_of_year + 2) / 153;
    let day = day_of_year - (153 * month_prime + 2) / 5 + 1;
    let month = if month_prime < 10 {
        month_prime + 3
    } else {
        month_prime - 9
    };
    (
        (if month <= 2 { year + 1 } else { year }) as i32,
        month as u8,
        day as u8,
    )
}
