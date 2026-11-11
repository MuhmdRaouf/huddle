mod e2e_support;

use e2e_support::{Daemon, raw_exchange};
use huddle_pki::ca::{ca_cert_path, load_or_create};
use huddle_testkit::fake_upstream::FakeUpstream;
use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::SystemTime;

fn fixture() -> (FakeUpstream, FakeUpstream, Daemon) {
    let anthropic = e2e_support::upstream("{\"from\":\"anthropic\"}");
    let zai = e2e_support::upstream("{\"from\":\"zai\"}");
    let daemon = Daemon::start(
        &["zai"],
        &[
            ("HUDDLE_ROUTER_ANTHROPIC_URL", anthropic.url()),
            ("ZAI_BASE_URL", zai.url()),
            ("ZAI_API_KEY", "abcdefghijklmnop".to_string()),
        ],
    );
    load_or_create(
        &daemon.home.root().join(".local/share/huddle/ca"),
        SystemTime::now(),
    )
    .expect("create CA");
    (anthropic, zai, daemon)
}

fn ca_path(daemon: &Daemon) -> PathBuf {
    ca_cert_path(&daemon.home.root().join(".local/share/huddle/ca"))
}

fn curl_through_proxy(port: u16, ca: &Path, body: &str) -> (String, String) {
    let mut curl = Command::new("/usr/bin/curl")
        .args([
            "-sS",
            "--max-time",
            "20",
            "--proxy",
            &format!("http://127.0.0.1:{port}"),
            "--cacert",
            ca.to_str().expect("utf8 path"),
            "-H",
            "content-type: application/json",
            "-H",
            "x-api-key: client-key",
            "--data-binary",
            "@-",
            "-w",
            "\n%{http_code}",
            "https://api.anthropic.com/v1/messages",
        ])
        .env_clear()
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("run curl");
    curl.stdin
        .as_mut()
        .expect("curl stdin")
        .write_all(body.as_bytes())
        .expect("write curl body");
    let out = curl.wait_with_output().expect("curl output");
    let stdout = String::from_utf8_lossy(&out.stdout).into_owned();
    let stderr = String::from_utf8_lossy(&out.stderr).into_owned();
    (stdout, stderr)
}

#[test]
fn connect_decrypts_and_routes_a_claude_model_to_anthropic() {
    let (anthropic, zai, daemon) = fixture();
    let body = r#"{"model":"claude-opus-5-5","messages":[]}"#;

    let (stdout, stderr) = curl_through_proxy(daemon.port, &ca_path(&daemon), body);

    assert!(
        stdout.ends_with("\n200"),
        "stdout: {stdout}\nstderr: {stderr}"
    );
    assert!(stdout.contains("anthropic"), "{stdout}");
    let got = anthropic.requests();
    assert_eq!(got.len(), 1, "stderr: {stderr}");
    assert_eq!(got[0].path, "/v1/messages");
    assert_eq!(got[0].body, body.as_bytes().to_vec());
    assert!(zai.requests().is_empty());
}

#[test]
fn connect_decrypts_and_routes_a_glm_model_to_the_lane() {
    let (anthropic, zai, daemon) = fixture();
    let body = r#"{"model":"glm-5.3","messages":[]}"#;

    let (stdout, stderr) = curl_through_proxy(daemon.port, &ca_path(&daemon), body);

    assert!(
        stdout.ends_with("\n200"),
        "stdout: {stdout}\nstderr: {stderr}"
    );
    assert!(stdout.contains("zai"), "{stdout}");
    assert_eq!(zai.requests().len(), 1);
    assert!(anthropic.requests().is_empty());
}

#[test]
fn a_large_glm_body_goes_through_the_lane_unchanged() {
    let (anthropic, zai, daemon) = fixture();
    let body = format!(
        r#"{{"model":"glm-5.3","max_tokens":9999999,"messages":[{{"role":"user","content":"{}"}}]}}"#,
        "x".repeat(128 * 1024)
    );

    let (stdout, stderr) = curl_through_proxy(daemon.port, &ca_path(&daemon), &body);

    assert!(
        stdout.ends_with("\n200"),
        "stdout: {stdout}\nstderr: {stderr}"
    );
    assert_eq!(
        zai.requests()[0].body,
        body.as_bytes(),
        "the lane forwards the bytes unchanged"
    );
    assert!(anthropic.requests().is_empty());
}

#[test]
fn a_connect_naming_this_machine_is_refused() {
    let (_anthropic, _zai, daemon) = fixture();
    let reply = raw_exchange(
        daemon.port,
        "CONNECT 127.0.0.1:9 HTTP/1.1\r\nHost: 127.0.0.1:9\r\n\r\n",
    );
    assert!(reply.starts_with("HTTP/1.1 403"), "{reply}");
    assert!(reply.contains("may not name this machine"), "{reply}");
}
