use huddle_pki::ca::{PkiError, ROUTER_HOST, RouterCa, cert_facts, load_or_create};
use huddle_pki::leaf::{Leaf, LeafCache};
use huddle_testkit::temp_home::TempHome;
use rustls::pki_types::{CertificateDer, ServerName};
use rustls::{ClientConfig, ClientConnection, RootCertStore, ServerConfig, ServerConnection};
use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::PathBuf;
use std::sync::{Arc, Mutex};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

fn ca(home: &TempHome) -> RouterCa {
    load_or_create(
        &home.root().join(".local/share/huddle/ca"),
        SystemTime::now(),
    )
    .expect("ca")
}

fn handshake(leaf: &Leaf, ca: &RouterCa, name: &str) -> Result<(), rustls::Error> {
    let provider = Arc::new(rustls::crypto::ring::default_provider());
    let server_config = ServerConfig::builder_with_provider(provider.clone())
        .with_safe_default_protocol_versions()
        .expect("versions")
        .with_no_client_auth()
        .with_cert_resolver(Arc::new(SingleCert(leaf.certified.clone())));
    let mut roots = RootCertStore::empty();
    roots
        .add(CertificateDer::from(ca.cert_der().to_vec()))
        .expect("root");
    let client_config = ClientConfig::builder_with_provider(provider)
        .with_safe_default_protocol_versions()
        .expect("versions")
        .with_root_certificates(roots)
        .with_no_client_auth();
    let mut server = ServerConnection::new(Arc::new(server_config)).expect("server");
    let mut client = ClientConnection::new(
        Arc::new(client_config),
        ServerName::try_from(name.to_string()).expect("name"),
    )
    .expect("client");
    for _ in 0..16 {
        pump(&mut client, &mut server)?;
        if !client.is_handshaking() && !server.is_handshaking() {
            return Ok(());
        }
    }
    panic!("the handshake did not finish");
}

fn pump(client: &mut ClientConnection, server: &mut ServerConnection) -> Result<(), rustls::Error> {
    let mut buf = Vec::new();
    client.write_tls(&mut buf).expect("client write");
    server.read_tls(&mut buf.as_slice()).expect("server read");
    server.process_new_packets()?;
    let mut buf = Vec::new();
    server.write_tls(&mut buf).expect("server write");
    client.read_tls(&mut buf.as_slice()).expect("client read");
    client.process_new_packets()?;
    Ok(())
}

#[derive(Debug)]
struct SingleCert(Arc<rustls::sign::CertifiedKey>);

impl rustls::server::ResolvesServerCert for SingleCert {
    fn resolve(
        &self,
        _: rustls::server::ClientHello<'_>,
    ) -> Option<Arc<rustls::sign::CertifiedKey>> {
        Some(self.0.clone())
    }
}

fn unix(t: SystemTime) -> i64 {
    t.duration_since(UNIX_EPOCH).unwrap().as_secs() as i64
}

#[test]
fn the_leaf_serves_a_handshake_for_the_api_host() {
    let home = TempHome::new();
    let ca = ca(&home);
    let leaf = ca.leaf(ROUTER_HOST, SystemTime::now()).expect("leaf");
    handshake(&leaf, &ca, ROUTER_HOST).expect("handshake");
}

#[test]
fn the_leaf_verifies_for_the_api_host_only() {
    let home = TempHome::new();
    let ca = ca(&home);
    let leaf = ca.leaf(ROUTER_HOST, SystemTime::now()).expect("leaf");
    let err = handshake(&leaf, &ca, "claude.ai").expect_err("not valid for another name");
    assert!(
        matches!(err, rustls::Error::InvalidCertificate(_)),
        "{err:?}"
    );
}

#[test]
fn the_ca_signs_only_the_api_host() {
    let home = TempHome::new();
    let err = ca(&home)
        .leaf("evil.example.com", SystemTime::now())
        .err()
        .expect("refused");
    assert_eq!(err, PkiError::WrongHost("evil.example.com".to_string()));
    assert_eq!(
        err.to_string(),
        "local CA signs only api.anthropic.com, not \"evil.example.com\""
    );
}

#[test]
fn a_leaf_lives_at_most_a_week() {
    let home = TempHome::new();
    let now = SystemTime::now();
    let leaf = ca(&home).leaf(ROUTER_HOST, now).expect("leaf");
    let chain = leaf.certified.cert.as_slice();
    let facts = cert_facts(chain[0].as_ref()).expect("facts");
    assert!(
        facts.not_after_unix <= unix(now) + 7 * 86400,
        "at most a week"
    );
    assert!(facts.not_after_unix > unix(now), "not already expired");
    assert_eq!(chain.len(), 2, "leaf then CA");
}

#[test]
fn the_last_leaf_ends_with_the_ca() {
    let home = TempHome::new();
    let ca = ca(&home);
    let ca_end = cert_facts(ca.cert_der()).expect("facts").not_after_unix;
    let late = UNIX_EPOCH + Duration::from_secs((ca_end - 2 * 86400) as u64);
    let leaf = ca.leaf(ROUTER_HOST, late).expect("leaf");
    assert!(
        leaf.not_after_unix <= ca_end,
        "the leaf never outlives the CA"
    );
}

#[test]
fn the_cache_keeps_one_leaf_until_a_day_is_left() {
    let home = TempHome::new();
    let notes: Arc<Mutex<Vec<String>>> = Arc::default();
    let sink = notes.clone();
    let cache = LeafCache::new(
        home.root().join(".local/share/huddle/ca"),
        Arc::new(move |l: &str| sink.lock().unwrap().push(l.to_string())),
    );
    let now = SystemTime::now();
    let first = cache.get(now).expect("leaf");
    let second = cache.get(now + Duration::from_secs(3600)).expect("leaf");
    assert!(
        Arc::ptr_eq(&first, &second),
        "the same leaf while it has more than a day left"
    );
    let later = now + Duration::from_secs(6 * 86400 + 12 * 3600);
    let third = cache.get(later).expect("leaf");
    assert!(
        !Arc::ptr_eq(&first, &third),
        "re-signed once under a day is left"
    );
    assert!(notes.lock().unwrap().is_empty());
}

#[test]
fn a_ca_that_cannot_load_logs_once_and_retries_after_a_minute() {
    let home = TempHome::new();
    let dir: PathBuf = home.root().join(".local/share/huddle/ca");
    fs::create_dir_all(&dir).expect("mkdir");
    fs::set_permissions(&dir, fs::Permissions::from_mode(0o770)).expect("chmod group-writable");
    let notes: Arc<Mutex<Vec<String>>> = Arc::default();
    let sink = notes.clone();
    let cache = LeafCache::new(
        dir.clone(),
        Arc::new(move |l: &str| sink.lock().unwrap().push(l.to_string())),
    );
    let now = SystemTime::now();
    assert!(cache.get(now).is_err());
    assert!(
        cache.get(now + Duration::from_secs(10)).is_err(),
        "still failing inside the minute"
    );
    {
        let lines = notes.lock().unwrap();
        assert_eq!(lines.len(), 1, "one line per failure episode: {lines:?}");
        assert!(lines[0].starts_with("local CA unusable ("), "{}", lines[0]);
        assert!(lines[0].ends_with("; CONNECT api.anthropic.com passes through as a blind tunnel"));
    }
    fs::set_permissions(&dir, fs::Permissions::from_mode(0o700)).expect("fix");
    assert!(
        cache.get(now + Duration::from_secs(70)).is_ok(),
        "retried after a minute"
    );
    assert_eq!(notes.lock().unwrap().len(), 1);
}
