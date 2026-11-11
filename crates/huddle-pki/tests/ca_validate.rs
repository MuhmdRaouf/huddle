use huddle_pki::ca::{PkiError, ROUTER_HOST, ca_cert_path, cert_facts, load_or_create};
use huddle_testkit::temp_home::TempHome;
use rcgen::{
    BasicConstraints, CertificateParams, DnType, GeneralSubtree, IsCa, KeyPair, NameConstraints,
    PKCS_ECDSA_P256_SHA256,
};
use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

fn fresh_dir(home: &TempHome) -> PathBuf {
    let dir = home.root().join(".local/share/huddle/ca");
    fs::create_dir_all(&dir).expect("mkdir");
    fs::set_permissions(&dir, fs::Permissions::from_mode(0o700)).expect("chmod");
    dir
}

fn write_key(dir: &Path, text: &str) {
    let path = dir.join("huddle-ca-key.pem");
    fs::write(&path, text).expect("write");
    fs::set_permissions(&path, fs::Permissions::from_mode(0o600)).expect("chmod");
}

fn pem_of(tag: &str, der: &[u8]) -> String {
    let body: String = base64(der)
        .as_bytes()
        .chunks(64)
        .map(|c| format!("{}\n", std::str::from_utf8(c).unwrap()))
        .collect();
    format!("-----BEGIN {tag}-----\n{body}-----END {tag}-----\n")
}

fn base64(der: &[u8]) -> String {
    const A: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut out = String::new();
    for c in der.chunks(3) {
        let (a, b, d) = (
            c[0] as u32,
            *c.get(1).unwrap_or(&0) as u32,
            *c.get(2).unwrap_or(&0) as u32,
        );
        let n = (a << 16) | (b << 8) | d;
        out.push(A[(n >> 18 & 63) as usize] as char);
        out.push(A[(n >> 12 & 63) as usize] as char);
        out.push(if c.len() > 1 {
            A[(n >> 6 & 63) as usize] as char
        } else {
            '='
        });
        out.push(if c.len() > 2 {
            A[(n & 63) as usize] as char
        } else {
            '='
        });
    }
    out
}

fn constrained_params(cn: &str) -> CertificateParams {
    let mut p = CertificateParams::default();
    p.distinguished_name = rcgen::DistinguishedName::new();
    p.distinguished_name.push(DnType::CommonName, cn);
    p.is_ca = IsCa::Ca(BasicConstraints::Constrained(0));
    p.name_constraints = Some(NameConstraints {
        permitted_subtrees: vec![GeneralSubtree::DnsName(ROUTER_HOST.to_string())],
        excluded_subtrees: vec![],
    });
    p
}

fn sec1_of(pkcs8: &[u8]) -> Vec<u8> {
    let mut d = pkcs8;
    fn take<'a>(d: &mut &'a [u8], want: u8) -> &'a [u8] {
        assert_eq!(d[0], want);
        let (len, skip) = if d[1] < 0x80 {
            (d[1] as usize, 2)
        } else if d[1] == 0x81 {
            (d[2] as usize, 3)
        } else {
            (((d[2] as usize) << 8) | d[3] as usize, 4)
        };
        let v = &d[skip..skip + len];
        *d = &d[skip + len..];
        v
    }
    let mut seq = take(&mut d, 0x30);
    let _ver = take(&mut seq, 0x02);
    let _alg = take(&mut seq, 0x30);
    take(&mut seq, 0x04).to_vec()
}

fn now() -> SystemTime {
    SystemTime::now()
}

#[test]
fn cert_facts_read_a_real_ca() {
    let home = TempHome::new();
    let ca = load_or_create(&home.root().join(".local/share/huddle/ca"), now()).expect("ca");
    let facts = cert_facts(ca.cert_der()).expect("facts");
    assert!(facts.is_ca);
    assert!(facts.name_constraints_critical);
    assert_eq!(facts.permitted_dns, vec![ROUTER_HOST.to_string()]);
    assert_eq!(facts.subject_cn, b"huddle local CA for api.anthropic.com");
    assert!(
        facts.not_after_unix > now().duration_since(UNIX_EPOCH).unwrap().as_secs() as i64,
        "the CA is not already expired"
    );
    assert_eq!(ca.not_after_unix(), facts.not_after_unix);
}

#[test]
fn a_key_file_with_its_certificate_appended_loads_and_keeps_it() {
    let home = TempHome::new();
    let dir = fresh_dir(&home);
    let key = KeyPair::generate_for(&PKCS_ECDSA_P256_SHA256).expect("key");
    let cert = constrained_params("cc-plugins local CA for api.anthropic.com")
        .self_signed(&key)
        .expect("cert");
    let sec1 = sec1_of(&key.serialize_der());
    write_key(
        &dir,
        &format!("{}{}", pem_of("EC PRIVATE KEY", &sec1), cert.pem()),
    );
    let ca = load_or_create(&dir, now()).expect("a key file with its certificate loads");
    assert_eq!(ca.cert_der(), cert.der().as_ref());
    assert_eq!(
        fs::read_to_string(ca_cert_path(&dir)).expect("read"),
        cert.pem()
    );
}

#[test]
fn a_key_of_another_ca_is_refused() {
    let home = TempHome::new();
    let dir = fresh_dir(&home);
    let key = KeyPair::generate_for(&PKCS_ECDSA_P256_SHA256).expect("key");
    let other = KeyPair::generate_for(&PKCS_ECDSA_P256_SHA256).expect("key");
    let cert = constrained_params("x").self_signed(&key).expect("cert");
    write_key(&dir, &format!("{}{}", other.serialize_pem(), cert.pem()));
    let err = load_or_create(&dir, now()).err().expect("refused");
    assert_eq!(
        err,
        PkiError::Invalid {
            path: dir.join("huddle-ca-key.pem").display().to_string(),
            reason: "the key does not match the certificate".into(),
        }
    );
}

#[test]
fn a_certificate_first_or_a_key_without_a_certificate_is_refused() {
    let home = TempHome::new();
    let dir = fresh_dir(&home);
    let key = KeyPair::generate_for(&PKCS_ECDSA_P256_SHA256).expect("key");
    let cert = constrained_params("x").self_signed(&key).expect("cert");
    write_key(&dir, &format!("{}{}", cert.pem(), key.serialize_pem()));
    assert!(
        load_or_create(&dir, now())
            .err()
            .expect("refused")
            .to_string()
            .contains("no EC private key")
    );
    write_key(&dir, &key.serialize_pem());
    assert!(
        load_or_create(&dir, now())
            .err()
            .expect("refused")
            .to_string()
            .contains("no certificate")
    );
}

#[test]
fn a_certificate_that_is_not_a_constrained_ca_is_refused() {
    let home = TempHome::new();
    let dir = fresh_dir(&home);
    let key = KeyPair::generate_for(&PKCS_ECDSA_P256_SHA256).expect("key");
    let mut p = constrained_params("x");
    p.name_constraints = None;
    let unconstrained = p.self_signed(&key).expect("cert");
    write_key(
        &dir,
        &format!("{}{}", key.serialize_pem(), unconstrained.pem()),
    );
    assert!(
        load_or_create(&dir, now())
            .err()
            .expect("refused")
            .to_string()
            .contains("not a CA constrained to api.anthropic.com")
    );

    let mut other_host = constrained_params("x");
    other_host.name_constraints = Some(NameConstraints {
        permitted_subtrees: vec![GeneralSubtree::DnsName("other.example.com".into())],
        excluded_subtrees: vec![],
    });
    let cert = other_host.self_signed(&key).expect("cert");
    write_key(&dir, &format!("{}{}", key.serialize_pem(), cert.pem()));
    assert!(
        load_or_create(&dir, now())
            .err()
            .expect("refused")
            .to_string()
            .contains("not a CA constrained to api.anthropic.com")
    );
}

#[test]
fn a_ca_with_under_eight_days_left_is_replaced() {
    let home = TempHome::new();
    let dir = home.root().join(".local/share/huddle/ca");
    let original = load_or_create(&dir, now()).expect("ca");
    let original_der = original.cert_der().to_vec();
    let almost_expired = now() + Duration::from_secs(3650 * 86400 - 3 * 86400);
    let replaced = load_or_create(&dir, almost_expired).expect("replaced");
    assert_ne!(
        replaced.cert_der(),
        original_der.as_slice(),
        "a new CA stands in the file"
    );
    let facts = cert_facts(replaced.cert_der()).expect("facts");
    assert!(
        facts.not_after_unix > almost_expired.duration_since(UNIX_EPOCH).unwrap().as_secs() as i64,
        "the new CA is not already expired"
    );
    assert_eq!(
        fs::read_to_string(ca_cert_path(&dir)).expect("read"),
        replaced.cert_pem(),
        "huddle-ca.pem follows the new key"
    );
}

#[test]
fn an_expiring_ca_that_cannot_be_replaced_is_an_error() {
    // Root writes past the write bits: the premise cannot be constructed there.
    if runs_as_root() {
        return;
    }
    let home = TempHome::new();
    let dir = home.root().join(".local/share/huddle/ca");
    load_or_create(&dir, now()).expect("ca");
    fs::set_permissions(&dir, fs::Permissions::from_mode(0o500)).expect("chmod");
    let almost_expired = now() + Duration::from_secs(3650 * 86400 - 3 * 86400);
    let err = load_or_create(&dir, almost_expired)
        .err()
        .expect("cannot replace");
    assert!(matches!(err, PkiError::Io { .. }));
    fs::set_permissions(&dir, fs::Permissions::from_mode(0o700)).expect("restore for cleanup");
}

/// Whether this process is root: an euid of 0 writes past the write bits, so a
/// test whose premise is an unwritable directory cannot run there. Reads the
/// effective uid off `/proc/self/status`; where that file is missing, the
/// answer is no.
fn runs_as_root() -> bool {
    fs::read_to_string("/proc/self/status")
        .ok()
        .and_then(|status| {
            status
                .lines()
                .find(|line| line.starts_with("Uid:"))
                .map(|line| line.split_whitespace().nth(2) == Some("0"))
        })
        .unwrap_or(false)
}

#[test]
fn ca_pem_is_written_from_the_key_file_as_it_stands() {
    let home = TempHome::new();
    let dir = home.root().join(".local/share/huddle/ca");
    load_or_create(&dir, now()).expect("ca");
    let key = KeyPair::generate_for(&PKCS_ECDSA_P256_SHA256).expect("key");
    let newer = constrained_params("huddle local CA for api.anthropic.com")
        .self_signed(&key)
        .expect("cert");
    let path = dir.join("huddle-ca-key.pem");
    fs::write(&path, format!("{}{}", key.serialize_pem(), newer.pem())).expect("write");
    fs::remove_file(ca_cert_path(&dir)).expect("remove");
    load_or_create(&dir, now()).expect("reload");
    assert_eq!(
        fs::read_to_string(ca_cert_path(&dir)).expect("read"),
        newer.pem()
    );
}
