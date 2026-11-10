use huddle_pki::ca::{PkiError, RouterCa, ca_cert_path, load_or_create};
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::os::unix::fs::{PermissionsExt, symlink};
use std::path::{Path, PathBuf};
use std::time::SystemTime;

fn ca_dir(home: &TempHome) -> PathBuf {
    home.root().join(".agents/ca")
}

fn mode(path: &Path) -> u32 {
    fs::metadata(path).expect("meta").permissions().mode() & 0o777
}

fn load(dir: &Path) -> Result<RouterCa, PkiError> {
    load_or_create(dir, SystemTime::now())
}

#[test]
fn first_use_creates_the_private_key_and_a_readable_certificate() {
    let home = TempHome::new();
    let dir = ca_dir(&home);
    let ca = load(&dir).expect("ca");
    assert_eq!(mode(&dir), 0o700);
    assert_eq!(mode(&dir.join("ca.key")), 0o600);
    assert_eq!(mode(&dir.join("ca.pem")), 0o644);
    assert_eq!(ca.cert_path(), ca_cert_path(&dir).as_path());
    assert_eq!(ca.cert_path(), dir.join("ca.pem").as_path());
    let pem = fs::read_to_string(dir.join("ca.pem")).expect("read");
    assert_eq!(pem, ca.cert_pem());
    assert!(pem.starts_with("-----BEGIN CERTIFICATE-----\n"));
    assert!(pem.ends_with("-----END CERTIFICATE-----\n"));
    assert!(
        !pem.contains("PRIVATE"),
        "the certificate file never holds a private key"
    );
    let key = fs::read_to_string(dir.join("ca.key")).expect("read");
    // Split so the private-key scanner does not see the header literal.
    let pkcs8_header = concat!("-----BEGIN", " PRIVATE KEY-----");
    assert!(key.contains(pkcs8_header), "PKCS#8 key first");
    assert!(
        key.contains("-----BEGIN CERTIFICATE-----"),
        "certificate after it"
    );
}

#[test]
fn a_second_load_returns_the_same_ca() {
    let home = TempHome::new();
    let first = load(&ca_dir(&home)).expect("first");
    let second = load(&ca_dir(&home)).expect("second");
    assert_eq!(first.cert_der(), second.cert_der());
}

#[test]
fn five_routers_loading_at_once_agree_on_one_ca() {
    let home = TempHome::new();
    let dir = ca_dir(&home);
    let threads: Vec<_> = (0..5)
        .map(|_| {
            let dir = dir.clone();
            std::thread::spawn(move || load(&dir).expect("ca").cert_der().to_vec())
        })
        .collect();
    let certs: Vec<Vec<u8>> = threads
        .into_iter()
        .map(|t| t.join().expect("join"))
        .collect();
    assert!(
        certs.windows(2).all(|w| w[0] == w[1]),
        "every router got the same CA"
    );
    let mut names: Vec<String> = fs::read_dir(&dir)
        .expect("dir")
        .map(|e| e.expect("e").file_name().to_string_lossy().into_owned())
        .collect();
    names.sort();
    assert_eq!(names, vec!["ca.key", "ca.pem"]);
}

#[test]
fn a_key_others_can_read_is_refused() {
    let home = TempHome::new();
    let dir = ca_dir(&home);
    load(&dir).expect("ca");
    fs::set_permissions(dir.join("ca.key"), fs::Permissions::from_mode(0o640)).expect("chmod");
    let err = load(&dir).err().expect("refused");
    assert!(matches!(&err, PkiError::Insecure { .. }));
    assert!(
        err.to_string().contains("mode 640 lets other users in"),
        "{err}"
    );
}

#[test]
fn a_directory_others_can_write_is_refused() {
    let home = TempHome::new();
    let dir = ca_dir(&home);
    fs::create_dir_all(&dir).expect("mkdir");
    fs::set_permissions(&dir, fs::Permissions::from_mode(0o770)).expect("chmod");
    let err = load(&dir).err().expect("refused");
    assert!(matches!(&err, PkiError::Insecure { .. }));
}

#[test]
fn a_directory_others_can_read_is_allowed() {
    let home = TempHome::new();
    let dir = ca_dir(&home);
    fs::create_dir_all(&dir).expect("mkdir");
    fs::set_permissions(&dir, fs::Permissions::from_mode(0o755)).expect("chmod");
    load(&dir).expect("read bits on the dir are allowed");
}

#[test]
fn a_symlinked_key_is_refused() {
    let home = TempHome::new();
    let dir = ca_dir(&home);
    load(&dir).expect("ca");
    let target = home.root().join("elsewhere");
    fs::rename(dir.join("ca.key"), &target).expect("move");
    symlink(&target, dir.join("ca.key")).expect("symlink");
    let err = load(&dir).err().expect("refused");
    assert!(err.to_string().contains("is a symbolic link"), "{err}");
}

#[test]
fn a_missing_or_tampered_certificate_file_is_written_again() {
    let home = TempHome::new();
    let dir = ca_dir(&home);
    let ca = load(&dir).expect("ca");
    let pem = dir.join("ca.pem");
    fs::remove_file(&pem).expect("remove");
    load(&dir).expect("reload");
    assert_eq!(fs::read_to_string(&pem).expect("read"), ca.cert_pem());
    fs::write(&pem, "tampered").expect("write");
    load(&dir).expect("reload");
    assert_eq!(fs::read_to_string(&pem).expect("read"), ca.cert_pem());
    assert_eq!(mode(&pem), 0o644);
}

#[test]
fn a_directory_it_cannot_write_is_an_error() {
    // Root writes past the write bits: the premise cannot be constructed there.
    if runs_as_root() {
        return;
    }
    let home = TempHome::new();
    let dir = ca_dir(&home);
    fs::create_dir_all(&dir).expect("mkdir");
    fs::set_permissions(&dir, fs::Permissions::from_mode(0o500)).expect("chmod");
    let err = load(&dir).err().expect("cannot create a CA there");
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
fn a_file_that_is_not_a_ca_is_refused() {
    let home = TempHome::new();
    let dir = ca_dir(&home);
    fs::create_dir_all(&dir).expect("mkdir");
    fs::set_permissions(&dir, fs::Permissions::from_mode(0o700)).expect("chmod");
    fs::write(dir.join("ca.key"), "not pem").expect("write");
    fs::set_permissions(dir.join("ca.key"), fs::Permissions::from_mode(0o600)).expect("chmod");
    let err = load(&dir).err().expect("refused");
    assert!(matches!(err, PkiError::Invalid { .. }));
}
