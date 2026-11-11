//! The router's local certificate authority.
//!
//! The CA lets a provider router stand in for `api.anthropic.com` when Claude Code reaches it
//! through `HTTPS_PROXY`. Claude Code trusts it only through `NODE_EXTRA_CA_CERTS`; it is never
//! added to a system trust store.

use std::fmt;
use std::fs;
use std::io;
use std::io::Write as _;
use std::os::unix::fs::{DirBuilderExt, MetadataExt, OpenOptionsExt, PermissionsExt};
use std::path::{Path, PathBuf};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use rcgen::{
    BasicConstraints, Certificate, CertificateParams, DistinguishedName, DnType, GeneralSubtree,
    IsCa, KeyPair, KeyUsagePurpose, NameConstraints, PKCS_ECDSA_P256_SHA256, PublicKeyData,
    date_time_ymd,
};
use rustls::pki_types::PrivatePkcs8KeyDer;
use rustls::pki_types::pem::{SectionKind, SliceIter};

/// The one name the router CA signs for.
pub const ROUTER_HOST: &str = "api.anthropic.com";

/// One hour before `now`, so the CA is already valid on the machine that mints it.
const BACKDATED: Duration = Duration::from_secs(3600);

/// Ten years.
const CA_LIFETIME: Duration = Duration::from_secs(3650 * 86_400);

/// A CA this close to expiry is replaced, so no leaf outlives it: the seven-day leaf life plus
/// one day.
const RENEW_BEFORE: Duration = Duration::from_secs(8 * 86_400);

const BEGIN_CERTIFICATE: &[u8] = b"-----BEGIN CERTIFICATE-----";
const END_CERTIFICATE: &[u8] = b"-----END CERTIFICATE-----";

/// What the CA store refuses or fails on.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum PkiError {
    /// A path another user can read or replace; the CA is not used.
    Insecure { path: String, reason: String },
    /// `huddle-ca-key.pem` does not hold a CA this module made.
    Invalid { path: String, reason: String },
    /// A file operation failed.
    Io { path: String, message: String },
    /// CA generation failed.
    Sign(String),
    /// A leaf was asked for another host.
    WrongHost(String),
}

impl fmt::Display for PkiError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            PkiError::Insecure { path, reason } | PkiError::Invalid { path, reason } => {
                write!(f, "local CA {path}: {reason}")
            }
            PkiError::Io { path, message } => write!(f, "{path}: {message}"),
            PkiError::Sign(message) => write!(f, "local CA: {message}"),
            PkiError::WrongHost(host) => {
                write!(f, "local CA signs only {ROUTER_HOST}, not \"{host}\"")
            }
        }
    }
}

impl std::error::Error for PkiError {}

/// What `cert_facts` reads out of a certificate's DER.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct CertFacts {
    pub is_ca: bool,
    pub name_constraints_critical: bool,
    pub permitted_dns: Vec<String>,
    pub not_after_unix: i64,
    /// The subjectPublicKey BIT STRING contents, without the unused-bits byte.
    pub public_key: Vec<u8>,
    /// The DER string tag of the subject commonName value.
    pub subject_cn_tag: u8,
    /// The raw bytes of the subject commonName value.
    pub subject_cn: Vec<u8>,
}

/// A loaded CA: its certificate, its signing key and the PEM file's path.
pub struct RouterCa {
    cert_pem: String,
    cert_der: Vec<u8>,
    facts: CertFacts,
    /// Held for the leaf signer.
    key: KeyPair,
    cert_path: PathBuf,
}

impl RouterCa {
    /// The CA certificate in PEM form.
    pub fn cert_pem(&self) -> &str {
        &self.cert_pem
    }

    /// The CA certificate in DER form.
    pub fn cert_der(&self) -> &[u8] {
        &self.cert_der
    }

    /// The expiry of the CA certificate, in seconds since the unix epoch.
    pub fn not_after_unix(&self) -> i64 {
        self.facts.not_after_unix
    }

    /// The DER tag and raw bytes of the subject commonName value.
    pub fn subject_cn(&self) -> (u8, &[u8]) {
        (self.facts.subject_cn_tag, &self.facts.subject_cn)
    }

    pub(crate) fn key(&self) -> &KeyPair {
        &self.key
    }

    /// The PEM file the proxy keys name.
    pub fn cert_path(&self) -> &Path {
        &self.cert_path
    }
}

/// `<dir>/huddle-ca.pem`, whether or not a CA exists there: the path the proxy keys name.
pub fn ca_cert_path(dir: &Path) -> PathBuf {
    dir.join("huddle-ca.pem")
}

/// Reads the facts a loaded CA is judged by out of a certificate's DER.
pub fn cert_facts(der: &[u8]) -> Result<CertFacts, PkiError> {
    read_facts(der).ok_or_else(|| PkiError::Invalid {
        path: "certificate".to_owned(),
        reason: "certificate DER could not be read".to_owned(),
    })
}

fn read_facts(der: &[u8]) -> Option<CertFacts> {
    let mut cert = Der::new(der);
    let mut tbs = cert.take_value(0x30)?.take_value(0x30)?;
    if tbs.peek_tag() == Some(0xa0) {
        tbs.take(0xa0)?; // the version
    }
    tbs.take(0x02)?; // the serial
    tbs.take(0x30)?; // the signature algorithm
    tbs.take(0x30)?; // the issuer
    let not_after_unix = validity_not_after(&mut tbs)?;
    let (subject_cn_tag, subject_cn) = subject_common_name(&mut tbs)?;
    let public_key = spki_key(&mut tbs)?;
    let (is_ca, name_constraints_critical, permitted_dns) = extensions(&mut tbs)?;
    Some(CertFacts {
        is_ca,
        name_constraints_critical,
        permitted_dns,
        not_after_unix,
        public_key,
        subject_cn_tag,
        subject_cn,
    })
}

/// A reader over DER: `rest` is what is left to read.
struct Der<'a> {
    rest: &'a [u8],
}

impl<'a> Der<'a> {
    fn new(rest: &'a [u8]) -> Der<'a> {
        Der { rest }
    }

    fn peek_tag(&self) -> Option<u8> {
        self.rest.first().copied()
    }

    /// The contents of the next value when it carries `want_tag`.
    fn take(&mut self, want_tag: u8) -> Option<&'a [u8]> {
        if self.peek_tag() != Some(want_tag) {
            return None;
        }
        let (skip, len) = der_length(&self.rest[1..])?;
        let body = self.rest.get(skip + 1..)?.get(..len)?;
        self.rest = &self.rest[skip + len + 1..];
        Some(body)
    }

    /// The tag and contents of the next value, whatever its tag.
    fn take_any(&mut self) -> Option<(u8, &'a [u8])> {
        let tag = self.peek_tag()?;
        Some((tag, self.take(tag)?))
    }

    /// A reader over the contents of the next value when it carries `want_tag`.
    fn take_value(&mut self, want_tag: u8) -> Option<Der<'a>> {
        Some(Der::new(self.take(want_tag)?))
    }
}

/// The span of the length bytes and the content length of a DER length prefix.
fn der_length(prefix: &[u8]) -> Option<(usize, usize)> {
    Some(match *prefix.first()? {
        n @ 0..=0x7f => (1, n as usize),
        0x81 => (2, *prefix.get(1)? as usize),
        0x82 => (
            3,
            ((*prefix.get(1)? as usize) << 8) | *prefix.get(2)? as usize,
        ),
        _ => return None,
    })
}

fn validity_not_after(tbs: &mut Der<'_>) -> Option<i64> {
    let mut validity = Der::new(tbs.take(0x30)?);
    validity.take_any()?; // notBefore
    let (tag, value) = validity.take_any()?;
    time_unix(tag, value)
}

/// The seconds since the unix epoch of a UTCTime or GeneralizedTime at whole seconds.
fn time_unix(tag: u8, value: &[u8]) -> Option<i64> {
    let text = std::str::from_utf8(value).ok()?;
    let (year, rest) = match tag {
        // UTCTime: YYMMDDHHMMSSZ, years 50..99 are 19xx.
        0x17 => {
            let short: i64 = text.get(..2)?.parse().ok()?;
            (
                if short >= 50 {
                    1900 + short
                } else {
                    2000 + short
                },
                text.get(2..)?,
            )
        }
        // GeneralizedTime: YYYYMMDDHHMMSSZ.
        0x18 => (text.get(..4)?.parse().ok()?, text.get(4..)?),
        _ => return None,
    };
    let month: u8 = rest.get(..2)?.parse().ok()?;
    let day: u8 = rest.get(2..4)?.parse().ok()?;
    let hour: i64 = rest.get(4..6)?.parse().ok()?;
    let minute: i64 = rest.get(6..8)?.parse().ok()?;
    let second: i64 = rest.get(8..10)?.parse().ok()?;
    if rest.as_bytes().get(10) != Some(&b'Z') {
        return None;
    }
    Some(days_from_civil(year, month, day) * 86_400 + hour * 3_600 + minute * 60 + second)
}

fn subject_common_name(tbs: &mut Der<'_>) -> Option<(u8, Vec<u8>)> {
    let mut name = Der::new(tbs.take(0x30)?);
    while name.peek_tag() == Some(0x31) {
        let mut set = Der::new(name.take(0x31)?);
        while set.peek_tag() == Some(0x30) {
            let mut rdn = Der::new(set.take(0x30)?);
            let oid = rdn.take(0x06)?;
            let (tag, value) = rdn.take_any()?;
            if oid == [0x55, 0x04, 0x03] {
                return Some((tag, value.to_vec()));
            }
        }
    }
    None
}

fn spki_key(tbs: &mut Der<'_>) -> Option<Vec<u8>> {
    let mut spki = Der::new(tbs.take(0x30)?);
    spki.take(0x30)?; // the algorithm
    let (unused, key) = spki.take(0x03)?.split_first()?;
    (unused == &0).then(|| key.to_vec())
}

fn extensions(tbs: &mut Der<'_>) -> Option<(bool, bool, Vec<String>)> {
    let mut is_ca = false;
    let mut name_constraints_critical = false;
    let mut permitted_dns = Vec::new();
    if tbs.peek_tag() == Some(0xa3) {
        let mut outer = Der::new(tbs.take(0xa3)?);
        let mut list = Der::new(outer.take(0x30)?);
        while list.peek_tag() == Some(0x30) {
            let mut ext = Der::new(list.take(0x30)?);
            let oid = ext.take(0x06)?;
            let mut critical = false;
            if ext.peek_tag() == Some(0x01) {
                critical = ext.take(0x01)?.first() != Some(&0);
            }
            let value = ext.take(0x04)?;
            match oid {
                [0x55, 0x1d, 0x13] => is_ca |= basic_constraints_is_ca(value)?,
                [0x55, 0x1d, 0x1e] => {
                    name_constraints_critical = critical;
                    permitted_dns = permitted_dns_names(value)?;
                }
                _ => {}
            }
        }
    }
    Some((is_ca, name_constraints_critical, permitted_dns))
}

/// Whether the basicConstraints extension says the certificate is a CA.
fn basic_constraints_is_ca(value: &[u8]) -> Option<bool> {
    let mut outer = Der::new(value);
    let mut inner = Der::new(outer.take(0x30)?);
    Some(match inner.peek_tag() {
        Some(0x01) => inner.take(0x01)?.first() != Some(&0),
        _ => false,
    })
}

/// The permitted dNSName subtrees of the nameConstraints extension.
fn permitted_dns_names(value: &[u8]) -> Option<Vec<String>> {
    let mut outer = Der::new(value);
    let mut root = outer.take_value(0x30)?;
    let mut names = Vec::new();
    if root.peek_tag() == Some(0xa0) {
        let mut permitted = root.take_value(0xa0)?;
        while permitted.peek_tag() == Some(0x30) {
            let mut subtree = permitted.take_value(0x30)?;
            if subtree.peek_tag() == Some(0x82) {
                let dns = subtree.take(0x82)?;
                names.push(String::from_utf8(dns.to_vec()).ok()?);
            }
        }
    }
    Some(names)
}

/// Returns the CA kept in `dir`, creating it on first use. Several routers may call this at
/// once: the first to link its `huddle-ca-key.pem` wins and the others load that one.
pub fn load_or_create(dir: &Path, now: SystemTime) -> Result<RouterCa, PkiError> {
    ensure_dir(dir)?;
    let key_path = dir.join("huddle-ca-key.pem");
    let ca = match load(&key_path)? {
        Some(ca) => ca,
        None => create(dir, &key_path, now)?,
    };
    let ca = if ca.facts.not_after_unix - unix_seconds(now) < RENEW_BEFORE.as_secs() as i64 {
        replace(dir, &key_path, now)?
    } else {
        ca
    };
    ensure_cert_file(dir, &key_path)?;
    Ok(RouterCa {
        cert_pem: ca.cert_pem,
        cert_der: ca.cert_der,
        facts: ca.facts,
        key: ca.key,
        cert_path: ca_cert_path(dir),
    })
}

/// A parsed `huddle-ca-key.pem`: the certificate in both forms, its key and the facts read from it.
struct Loaded {
    cert_pem: String,
    cert_der: Vec<u8>,
    facts: CertFacts,
    key: KeyPair,
}

/// Makes `dir` (0700 when created; an existing dir keeps its mode) and refuses one other users
/// can write into.
fn ensure_dir(dir: &Path) -> Result<(), PkiError> {
    fs::DirBuilder::new()
        .mode(0o700)
        .recursive(true)
        .create(dir)
        .map_err(|e| io_error(dir, e))?;
    check_owned_private(dir, 0o022)
}

/// Reads `huddle-ca-key.pem`: `Ok(None)` when it is missing, the parsed CA when it holds one.
fn load(key_path: &Path) -> Result<Option<Loaded>, PkiError> {
    let meta = match fs::symlink_metadata(key_path) {
        Ok(meta) => meta,
        Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(None),
        Err(e) => return Err(io_error(key_path, e)),
    };
    check_meta(key_path, &meta, 0o077)?;
    let bytes = match fs::read(key_path) {
        Ok(bytes) => bytes,
        Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(None),
        Err(e) => return Err(io_error(key_path, e)),
    };
    parse_ca(key_path, &bytes).map(Some)
}

/// Parses a `huddle-ca-key.pem` file: a PKCS#8 `PRIVATE KEY` or SEC1 `EC PRIVATE KEY` PEM block followed by
/// the `CERTIFICATE` block, judged by the rules the CA store keeps.
fn parse_ca(key_path: &Path, bytes: &[u8]) -> Result<Loaded, PkiError> {
    let invalid = |reason: String| PkiError::Invalid {
        path: key_path.to_string_lossy().into_owned(),
        reason,
    };
    let mut sections = SliceIter::<(SectionKind, Vec<u8>)>::new(bytes);
    let key_der = match sections.next() {
        Some(Ok((SectionKind::PrivateKey, der))) => der,
        Some(Ok((SectionKind::EcPrivateKey, sec1))) => pkcs8_from_sec1(&sec1),
        _ => return Err(invalid("no EC private key".to_owned())),
    };
    let cert_der = match sections.next() {
        Some(Ok((SectionKind::Certificate, der))) => der,
        _ => return Err(invalid("no certificate".to_owned())),
    };
    let key = KeyPair::from_pkcs8_der_and_sign_algo(
        &PrivatePkcs8KeyDer::from(key_der),
        &PKCS_ECDSA_P256_SHA256,
    )
    .map_err(|e| invalid(e.to_string()))?;
    let facts = cert_facts(&cert_der)?;
    if !(facts.is_ca
        && facts.name_constraints_critical
        && facts.permitted_dns == [ROUTER_HOST.to_string()])
    {
        return Err(invalid(format!("not a CA constrained to {ROUTER_HOST}")));
    }
    if key.der_bytes() != facts.public_key {
        return Err(invalid("the key does not match the certificate".to_owned()));
    }
    let cert_pem = certificate_pem(bytes).ok_or_else(|| invalid("no certificate".to_owned()))?;
    Ok(Loaded {
        cert_pem,
        cert_der,
        facts,
        key,
    })
}

const OID_EC_PUBLIC_KEY: &[u8] = &[0x2a, 0x86, 0x48, 0xce, 0x3d, 0x02, 0x01];
const OID_PRIME256V1: &[u8] = &[0x2a, 0x86, 0x48, 0xce, 0x3d, 0x03, 0x01, 0x07];

/// Wraps a SEC1 EC private key in a PKCS#8 PrivateKeyInfo for P-256. The wrapped SEC1 may carry
/// its own `[0]` curve parameters; the key parser accepts them when they match the curve.
fn pkcs8_from_sec1(sec1: &[u8]) -> Vec<u8> {
    let version = tlv(0x02, &[0]);
    let algorithm = tlv(
        0x30,
        &[tlv(0x06, OID_EC_PUBLIC_KEY), tlv(0x06, OID_PRIME256V1)].concat(),
    );
    let private_key = tlv(0x04, sec1);
    tlv(0x30, &[version, algorithm, private_key].concat())
}

/// One DER TLV: `tag` holding `content` under its length prefix.
fn tlv(tag: u8, content: &[u8]) -> Vec<u8> {
    let mut out = vec![tag];
    out.extend_from_slice(&der_len(content.len()));
    out.extend_from_slice(content);
    out
}

/// The DER length prefix of `n` content bytes.
fn der_len(n: usize) -> Vec<u8> {
    match n {
        0..=0x7f => vec![n as u8],
        0x80..=0xff => vec![0x81, n as u8],
        _ => vec![0x82, (n >> 8) as u8, n as u8],
    }
}

/// The text of `bytes` from `-----BEGIN CERTIFICATE-----` through the end of its
/// `-----END CERTIFICATE-----` line, with exactly one trailing newline.
fn certificate_pem(bytes: &[u8]) -> Option<String> {
    let begin = find(bytes, BEGIN_CERTIFICATE)?;
    let after_begin = begin + BEGIN_CERTIFICATE.len();
    let end = find(&bytes[after_begin..], END_CERTIFICATE)? + after_begin;
    let line_end = end + END_CERTIFICATE.len();
    let stop = match bytes.get(line_end) {
        Some(b'\n') => line_end + 1,
        _ => line_end,
    };
    let text = std::str::from_utf8(&bytes[begin..stop]).ok()?;
    Some(if text.ends_with('\n') {
        text.to_owned()
    } else {
        format!("{text}\n")
    })
}

/// The offset of the first occurrence of `needle` in `haystack`.
fn find(haystack: &[u8], needle: &[u8]) -> Option<usize> {
    haystack
        .windows(needle.len())
        .position(|window| window == needle)
}

/// Mints a CA and links its key file into place. A hard link never replaces an existing file,
/// so exactly one concurrent creator wins and the others load that CA.
fn create(dir: &Path, key_path: &Path, now: SystemTime) -> Result<Loaded, PkiError> {
    loop {
        let (key, cert) = generate(now)?;
        let bytes = format!("{}{}", key.serialize_pem(), cert.pem());
        let temp = write_temp(dir, bytes.as_bytes(), 0o600)?;
        if let Err(e) = fs::hard_link(&temp, key_path)
            && e.kind() != io::ErrorKind::AlreadyExists
        {
            let _ = fs::remove_file(&temp);
            return Err(io_error(key_path, e));
        }
        let _ = fs::remove_file(&temp);
        if let Some(ca) = load(key_path)? {
            return Ok(ca);
        }
    }
}

/// Swaps an expiring CA for a new one. Claude Code sessions started before the swap keep
/// trusting the old certificate until they restart, so this happens only near the end of a
/// ten-year life.
fn replace(dir: &Path, key_path: &Path, now: SystemTime) -> Result<Loaded, PkiError> {
    let (key, cert) = generate(now)?;
    let bytes = format!("{}{}", key.serialize_pem(), cert.pem());
    let temp = write_temp(dir, bytes.as_bytes(), 0o600)?;
    if let Err(e) = fs::rename(&temp, key_path) {
        let _ = fs::remove_file(&temp);
        return Err(io_error(key_path, e));
    }
    match load(key_path)? {
        Some(ca) => Ok(ca),
        None => Err(io_error(
            key_path,
            io::Error::other("the replaced CA file went missing"),
        )),
    }
}

/// Writes `bytes` to the first free `.ca-<pid>-<i>.tmp` in `dir` with the mode, and returns its
/// path. The pid keeps a crashed process's leftovers from spending this process's own names.
fn write_temp(dir: &Path, bytes: &[u8], mode: u32) -> Result<PathBuf, PkiError> {
    let mut last = io::Error::other("no free temp name beside the target");
    for i in 0..100 {
        let candidate = dir.join(format!(".ca-{}-{}.tmp", std::process::id(), i));
        match fs::OpenOptions::new()
            .mode(mode)
            .write(true)
            .create_new(true)
            .open(&candidate)
        {
            Ok(mut file) => {
                return match file.write_all(bytes) {
                    Ok(()) => {
                        // The mode in `open` meets the umask; set it outright.
                        fs::set_permissions(&candidate, fs::Permissions::from_mode(mode))
                            .map_err(|e| io_error(&candidate, e))?;
                        Ok(candidate)
                    }
                    Err(e) => {
                        let _ = fs::remove_file(&candidate);
                        Err(io_error(&candidate, e))
                    }
                };
            }
            Err(e) if e.kind() == io::ErrorKind::AlreadyExists => last = e,
            Err(e) => return Err(io_error(&candidate, e)),
        }
    }
    Err(io_error(dir, last))
}

/// Makes a new CA key and its self-signed certificate, name-constrained to the router host.
fn generate(now: SystemTime) -> Result<(KeyPair, Certificate), PkiError> {
    let key = KeyPair::generate_for(&PKCS_ECDSA_P256_SHA256)
        .map_err(|e| PkiError::Sign(e.to_string()))?;
    let mut params = CertificateParams::default();
    let mut name = DistinguishedName::new();
    name.push(
        DnType::CommonName,
        format!("huddle local CA for {ROUTER_HOST}"),
    );
    params.distinguished_name = name;
    params.is_ca = IsCa::Ca(BasicConstraints::Constrained(0));
    params.key_usages = vec![KeyUsagePurpose::KeyCertSign, KeyUsagePurpose::CrlSign];
    params.name_constraints = Some(NameConstraints {
        permitted_subtrees: vec![GeneralSubtree::DnsName(ROUTER_HOST.to_string())],
        excluded_subtrees: vec![],
    });
    let start = now.checked_sub(BACKDATED).unwrap_or(UNIX_EPOCH);
    let (start_year, start_month, start_day) = day_of(start);
    params.not_before = date_time_ymd(start_year, start_month, start_day);
    let expiry = now
        .checked_add(CA_LIFETIME)
        .ok_or_else(|| PkiError::Sign("the CA lifetime overflows the clock".to_owned()))?;
    let (end_year, end_month, end_day) = day_of(expiry);
    params.not_after = date_time_ymd(end_year, end_month, end_day);
    let cert = params
        .self_signed(&key)
        .map_err(|e| PkiError::Sign(e.to_string()))?;
    Ok((key, cert))
}

/// The UTC calendar day of `t`, at midnight: rcgen takes a date, and flooring keeps
/// `not_before <= now` and the life within a day of ten years.
fn day_of(t: SystemTime) -> (i32, u8, u8) {
    civil_from_days(unix_seconds(t).div_euclid(86_400))
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

/// The whole days since the unix epoch of a UTC (year, month, day) date.
fn days_from_civil(year: i64, month: u8, day: u8) -> i64 {
    let y = if month <= 2 { year - 1 } else { year };
    let era = y.div_euclid(400);
    let year_of_era = y - era * 400;
    let month_prime = i64::from(if month > 2 { month - 3 } else { month + 9 });
    let day_of_year = (153 * month_prime + 2) / 5 + i64::from(day) - 1;
    let day_of_era = year_of_era * 365 + year_of_era / 4 - year_of_era / 100 + day_of_year;
    era * 146_097 + day_of_era - 719_468
}

/// Writes `huddle-ca.pem` from `huddle-ca-key.pem` as it stands when it is missing or differs, so the two files
/// agree even when another router renamed a new key in between.
fn ensure_cert_file(dir: &Path, key_path: &Path) -> Result<(), PkiError> {
    let bytes = fs::read(key_path).map_err(|e| io_error(key_path, e))?;
    let cert_pem = certificate_pem(&bytes).ok_or_else(|| PkiError::Invalid {
        path: key_path.to_string_lossy().into_owned(),
        reason: "no certificate".to_owned(),
    })?;
    let cert_path = ca_cert_path(dir);
    if let Ok(current) = fs::read(&cert_path)
        && current == cert_pem.as_bytes()
    {
        return Ok(());
    }
    let temp = write_temp(dir, cert_pem.as_bytes(), 0o644)?;
    if let Err(e) = fs::rename(&temp, &cert_path) {
        let _ = fs::remove_file(&temp);
        return Err(io_error(&cert_path, e));
    }
    Ok(())
}

/// Refuses a path another user owns or whose mode has any of the forbidden bits.
fn check_owned_private(path: &Path, forbidden: u32) -> Result<(), PkiError> {
    let meta = fs::symlink_metadata(path).map_err(|e| io_error(path, e))?;
    check_meta(path, &meta, forbidden)
}

fn check_meta(path: &Path, meta: &fs::Metadata, forbidden: u32) -> Result<(), PkiError> {
    let insecure = |reason: String| PkiError::Insecure {
        path: path.to_string_lossy().into_owned(),
        reason,
    };
    if meta.file_type().is_symlink() {
        return Err(insecure("is a symbolic link".to_owned()));
    }
    // Reads the process owner; no side effects.
    if meta.uid() != unsafe { libc::getuid() } {
        return Err(insecure("owned by another user".to_owned()));
    }
    let mode = meta.permissions().mode();
    if mode & forbidden != 0 {
        return Err(insecure(format!(
            "mode {:o} lets other users in",
            mode & 0o777
        )));
    }
    Ok(())
}

fn io_error(path: &Path, error: io::Error) -> PkiError {
    PkiError::Io {
        path: path.to_string_lossy().into_owned(),
        message: error.to_string(),
    }
}
