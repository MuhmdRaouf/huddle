//! `huddle provider <p> key-page`: the helper behind the key page. It serves one page on a
//! loopback port, takes the key in one `POST`, hands it to `store_and_finish` and exits. Only
//! the keystore ever holds the key: the helper logs no request, writes no body and answers with
//! step names and states alone. The one-time token arrives on stdin; the record it leaves for
//! `setup` holds the port, the pid and the expiry, never the token.

use std::convert::Infallible;
use std::fs::File;
use std::io::{self, Read};
use std::net::SocketAddr;
use std::path::PathBuf;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Duration;

use http_body_util::{BodyExt, Full, Limited};
use hyper::body::{Bytes, Incoming};
use hyper::header::{
    CACHE_CONTROL, CONTENT_SECURITY_POLICY, CONTENT_TYPE, HOST, HeaderValue, ORIGIN,
    REFERRER_POLICY, X_CONTENT_TYPE_OPTIONS,
};
use hyper::service::service_fn;
use hyper::{Method, Request, Response, StatusCode};
use hyper_util::rt::{TokioIo, TokioTimer};
use serde::{Deserialize, Serialize};
use tokio::net::TcpListener;
use tokio::sync::Notify;
use tokio::task::JoinSet;

use huddle_catalog::providers::Provider;
use huddle_config::atomic::write_atomic;
use huddle_config::env::Env;
use huddle_config::files::PRIVATE_FILE_MODE;
use huddle_config::paths::Paths;
use huddle_jobs::id::now_ms;
use huddle_keystore::port::{SecretString, is_valid_key};

use super::finish::{KeyVerdict, Outcome, StepName, StepState, store_and_finish};

/// How long a key page link lives.
pub const LIFE: Duration = Duration::from_secs(600);

/// The most key bytes a `POST` may carry.
const BODY_CAP: usize = 4096;

/// The most bytes read from a `POST` before the request is answered 413, so a refusal never
/// leaves unread bytes behind that would reset the connection.
const DRAIN_CAP: usize = 64 * 1024;

/// How long a connection may take to send its request head.
const HEAD_TIMEOUT: Duration = Duration::from_secs(5);

/// How long the last answers get to flush before the helper exits.
const FLUSH_GRACE: Duration = Duration::from_secs(2);

const PAGE: &str = include_str!("key_page.html");

/// What `setup` reads to find the helper.
#[derive(Debug, Serialize, Deserialize)]
pub struct Record {
    pub port: u16,
    pub pid: u32,
    pub expires_at_ms: u64,
}

/// `<run_dir>/key-page-<p>.toml`.
pub fn record_path(paths: &Paths, provider: &Provider) -> PathBuf {
    paths.run_dir.join(format!("key-page-{}.toml", provider.id))
}

pub fn read_record(paths: &Paths, provider: &Provider) -> Option<Record> {
    let text = std::fs::read_to_string(record_path(paths, provider)).ok()?;
    toml::from_str(&text).ok()
}

/// `bytes` fresh bytes from the OS random source, as lowercase hex.
pub fn random_hex(bytes: usize) -> io::Result<String> {
    let mut buffer = vec![0_u8; bytes];
    File::open("/dev/urandom")?.read_exact(&mut buffer)?;
    Ok(buffer.iter().map(|byte| format!("{byte:02x}")).collect())
}

/// What the helper reads from stdin: the token on the first line, then optional `life_ms=<n>`
/// (a shorter life, for tests).
struct Input {
    token: String,
    life: Duration,
}

fn parse_input(text: &str) -> Option<Input> {
    let mut lines = text.lines();
    let token = lines.next()?.trim().to_owned();
    if token.len() != 64 || !token.bytes().all(|b| b.is_ascii_hexdigit()) {
        return None;
    }
    let life = lines
        .filter_map(|line| line.trim().strip_prefix("life_ms="))
        .find_map(|value| value.parse::<u64>().ok())
        .map_or(LIFE, Duration::from_millis)
        .min(LIFE);
    Some(Input { token, life })
}

/// Runs the helper: reads the token, serves until a key is stored or the life ends. Exit 0
/// after either, 1 when it cannot start, 2 on a bad token.
pub fn run(provider: &'static Provider, env: &Env) -> i32 {
    let mut text = String::new();
    if io::stdin().take(1024).read_to_string(&mut text).is_err() {
        return 2;
    }
    let Some(input) = parse_input(&text) else {
        return 2;
    };
    let Ok(paths) = Paths::from_env(env) else {
        return 1;
    };
    let Ok(executable) = std::env::current_exe() else {
        return 1;
    };
    let Ok(runtime) = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
    else {
        return 1;
    };
    let code = runtime.block_on(serve(provider, env, paths, executable, input));
    runtime.shutdown_timeout(FLUSH_GRACE);
    code
}

/// Everything a request handler reads.
struct State {
    provider: &'static Provider,
    env: Env,
    paths: Paths,
    executable: PathBuf,
    token: String,
    port: u16,
    expires_at_ms: u64,
    /// A key was stored: the page is spent.
    stored: AtomicBool,
    /// A key is being checked: a second `POST` waits for it.
    busy: AtomicBool,
    done: Notify,
}

async fn serve(
    provider: &'static Provider,
    env: &Env,
    paths: Paths,
    executable: PathBuf,
    input: Input,
) -> i32 {
    let Ok(listener) = TcpListener::bind(SocketAddr::from(([127, 0, 0, 1], 0))).await else {
        return 1;
    };
    let Ok(local) = listener.local_addr() else {
        return 1;
    };
    let expires_at_ms = now_ms() + input.life.as_millis() as u64;
    let record = Record {
        port: local.port(),
        pid: std::process::id(),
        expires_at_ms,
    };
    let path = record_path(&paths, provider);
    let written = toml::to_string(&record)
        .ok()
        .and_then(|text| write_atomic(&path, text.as_bytes(), PRIVATE_FILE_MODE).ok());
    if written.is_none() {
        return 1;
    }
    let state = Arc::new(State {
        provider,
        env: env.clone(),
        paths,
        executable,
        token: input.token,
        port: local.port(),
        expires_at_ms,
        stored: AtomicBool::new(false),
        busy: AtomicBool::new(false),
        done: Notify::new(),
    });

    let mut connections = JoinSet::new();
    let deadline = tokio::time::sleep(input.life);
    tokio::pin!(deadline);
    loop {
        tokio::select! {
            accepted = listener.accept() => {
                let Ok((stream, _)) = accepted else { continue };
                let state = state.clone();
                connections.spawn(async move {
                    let service = service_fn(move |request| handle(request, state.clone()));
                    let _ = hyper::server::conn::http1::Builder::new()
                        .keep_alive(false)
                        .timer(TokioTimer::new())
                        .header_read_timeout(HEAD_TIMEOUT)
                        .serve_connection(TokioIo::new(stream), service)
                        .await;
                });
            }
            _ = state.done.notified() => break,
            _ = &mut deadline => break,
        }
    }
    let _ = tokio::time::timeout(FLUSH_GRACE, async {
        while connections.join_next().await.is_some() {}
    })
    .await;
    let _ = std::fs::remove_file(&path);
    0
}

async fn handle(
    request: Request<Incoming>,
    state: Arc<State>,
) -> Result<Response<Full<Bytes>>, Infallible> {
    Ok(route(request, &state).await)
}

async fn route(request: Request<Incoming>, state: &Arc<State>) -> Response<Full<Bytes>> {
    let own_host = format!("127.0.0.1:{}", state.port);
    let host_ok = header_text(&request, HOST.as_str()) == Some(own_host.as_str());
    if !host_ok {
        return plain(StatusCode::FORBIDDEN, "forbidden");
    }
    let expected = format!("/key/{}/{}", state.provider.id, state.token);
    if !same(request.uri().path().as_bytes(), expected.as_bytes()) {
        return plain(StatusCode::NOT_FOUND, "not found");
    }
    let spent = state.stored.load(Ordering::SeqCst);
    match *request.method() {
        Method::GET => page(state, spent),
        Method::POST if spent => plain(StatusCode::GONE, "closed"),
        Method::POST => submit(request, state).await,
        _ => plain(StatusCode::METHOD_NOT_ALLOWED, "method not allowed"),
    }
}

/// The key `POST`: origin, type and size checks, then the check and the store.
async fn submit(request: Request<Incoming>, state: &Arc<State>) -> Response<Full<Bytes>> {
    let own_origin = format!("http://127.0.0.1:{}", state.port);
    if header_text(&request, ORIGIN.as_str()) != Some(own_origin.as_str()) {
        return plain(StatusCode::FORBIDDEN, "forbidden");
    }
    let is_json = header_text(&request, CONTENT_TYPE.as_str())
        .is_some_and(|value| value.starts_with("application/json"));
    if !is_json {
        return plain(StatusCode::UNSUPPORTED_MEDIA_TYPE, "expected json");
    }
    let Ok(collected) = Limited::new(request.into_body(), DRAIN_CAP).collect().await else {
        return plain(StatusCode::PAYLOAD_TOO_LARGE, "too large");
    };
    let body = collected.to_bytes();
    if body.len() > BODY_CAP {
        return plain(StatusCode::PAYLOAD_TOO_LARGE, "too large");
    }
    let Ok(KeyBody { key }) = serde_json::from_slice(&body) else {
        return plain(StatusCode::BAD_REQUEST, "bad request");
    };
    let key = key.trim().to_owned();
    if !is_valid_key(&key) {
        return json(&Answer::bare("refused"));
    }
    if state.busy.swap(true, Ordering::SeqCst) {
        return plain(StatusCode::CONFLICT, "busy");
    }
    let worker = state.clone();
    let finished = tokio::task::spawn_blocking(move || {
        store_and_finish(
            worker.provider,
            &worker.env,
            &worker.paths,
            &worker.executable,
            SecretString::new(key),
        )
    })
    .await;
    state.busy.store(false, Ordering::SeqCst);
    match finished {
        Ok(Ok(outcome)) => {
            let answer = answer(state.provider, &outcome);
            if answer.state != "refused" {
                state.stored.store(true, Ordering::SeqCst);
                state.done.notify_one();
            }
            json(&answer)
        }
        Ok(Err(_)) | Err(_) => json(&Answer::bare("error")),
    }
}

#[derive(Deserialize)]
struct KeyBody {
    key: String,
}

/// What the page needs to draw a result.
#[derive(Debug, Serialize)]
struct Answer {
    state: &'static str,
    steps: Vec<StepView>,
}

#[derive(Debug, Serialize)]
struct StepView {
    name: String,
    detail: String,
    state: &'static str,
}

impl Answer {
    fn bare(state: &'static str) -> Answer {
        Answer {
            state,
            steps: Vec::new(),
        }
    }
}

/// The page's view of an outcome: `ready` when the provider takes the key, `unchecked` when it
/// could not be asked, `refused` when it said no. The rows follow the mockup.
fn answer(provider: &Provider, outcome: &Outcome) -> Answer {
    let ok = "ok";
    let warn = "warn";
    let step = |name: &str, detail: &str, state: &'static str| StepView {
        name: name.to_owned(),
        detail: detail.to_owned(),
        state,
    };
    let (state, check) = match outcome.verdict {
        KeyVerdict::Refused => return Answer::bare("refused"),
        KeyVerdict::Accepted => ("ready", step(provider.display, "accepted the key", ok)),
        KeyVerdict::Limited => (
            "ready",
            step(
                provider.display,
                "knows the key, takes no requests yet",
                warn,
            ),
        ),
        KeyVerdict::Unexpected(code) => (
            "unchecked",
            step(
                provider.display,
                &format!("answered HTTP {code} to the check"),
                warn,
            ),
        ),
        KeyVerdict::Unknown => ("unchecked", step(provider.display, "no answer", warn)),
    };
    let routed = outcome
        .step(StepName::RouterRoutes)
        .is_some_and(|s| s.state == StepState::Done);
    let router = if routed {
        step("huddle-router", &format!("routes {}", provider.id), ok)
    } else {
        step("huddle-router", "not serving yet", warn)
    };
    let models = format!("{}, {}", provider.main.id, provider.flash.id);
    let mut steps = vec![
        step("Key", &format!("in the {}", store_name()), ok),
        check,
        router,
    ];
    if state == "ready" {
        steps.push(step("/model", &models, ok));
        steps.push(step("Agents", &models, ok));
    } else {
        steps.push(step("/model and agents", &models, ok));
    }
    Answer { state, steps }
}

fn store_name() -> &'static str {
    if cfg!(target_os = "macos") {
        "Keychain"
    } else {
        "key store"
    }
}

fn page(state: &State, closed: bool) -> Response<Full<Bytes>> {
    let Ok(nonce) = random_hex(16) else {
        return plain(StatusCode::INTERNAL_SERVER_ERROR, "no randomness");
    };
    let html = render_page(state.provider, &nonce, state.expires_at_ms, closed);
    let policy = format!(
        "default-src 'none'; style-src 'nonce-{nonce}'; script-src 'nonce-{nonce}'; \
         connect-src 'self'; form-action 'none'; frame-ancestors 'none'; base-uri 'none'"
    );
    let Ok(policy) = HeaderValue::from_str(&policy) else {
        return plain(StatusCode::INTERNAL_SERVER_ERROR, "bad policy");
    };
    let mut response = reply(
        if closed {
            StatusCode::GONE
        } else {
            StatusCode::OK
        },
        "text/html; charset=utf-8",
        html,
    );
    response
        .headers_mut()
        .insert(CONTENT_SECURITY_POLICY, policy);
    response
}

/// The mockup's page with this provider's words filled in.
fn render_page(provider: &Provider, nonce: &str, expires_at_ms: u64, closed: bool) -> String {
    let (where_, this) = if cfg!(target_os = "macos") {
        ("your macOS Keychain on this Mac", "Mac")
    } else {
        ("your system key store on this computer", "computer")
    };
    let keys_host = provider
        .keys_url
        .trim_start_matches("https://")
        .split('/')
        .next()
        .unwrap_or_default();
    let initial: String = provider.display.chars().take(1).collect();
    let fills = [
        ("{{NONCE}}", nonce.to_owned()),
        (
            "{{START}}",
            if closed { "closed" } else { "form" }.to_owned(),
        ),
        ("{{EXPIRES_MS}}", expires_at_ms.to_string()),
        ("{{INITIAL}}", escape(&initial)),
        ("{{DISPLAY}}", escape(provider.display)),
        ("{{ID}}", escape(provider.id)),
        ("{{KEYS_URL}}", escape(provider.keys_url)),
        ("{{KEYS_HOST}}", escape(keys_host)),
        ("{{WHERE}}", where_.to_owned()),
        ("{{THIS}}", this.to_owned()),
    ];
    fills.iter().fold(PAGE.to_owned(), |html, (marker, value)| {
        html.replace(marker, value)
    })
}

fn escape(text: &str) -> String {
    text.replace('&', "&amp;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
        .replace('"', "&quot;")
        .replace('\'', "&#39;")
}

fn header_text<'a>(request: &'a Request<Incoming>, name: &str) -> Option<&'a str> {
    request.headers().get(name)?.to_str().ok()
}

/// Equal bytes, compared without stopping at the first difference.
fn same(left: &[u8], right: &[u8]) -> bool {
    left.len() == right.len()
        && left
            .iter()
            .zip(right)
            .fold(0_u8, |acc, (a, b)| acc | (a ^ b))
            == 0
}

fn json(answer: &Answer) -> Response<Full<Bytes>> {
    match serde_json::to_string(answer) {
        Ok(text) => reply(StatusCode::OK, "application/json", text),
        Err(_) => plain(StatusCode::INTERNAL_SERVER_ERROR, "error"),
    }
}

fn plain(status: StatusCode, text: &str) -> Response<Full<Bytes>> {
    reply(status, "text/plain; charset=utf-8", text.to_owned())
}

/// A response with the headers every answer carries.
fn reply(status: StatusCode, content_type: &'static str, body: String) -> Response<Full<Bytes>> {
    let mut response = Response::new(Full::new(Bytes::from(body)));
    *response.status_mut() = status;
    let headers = response.headers_mut();
    headers.insert(CONTENT_TYPE, HeaderValue::from_static(content_type));
    headers.insert(CACHE_CONTROL, HeaderValue::from_static("no-store"));
    headers.insert(REFERRER_POLICY, HeaderValue::from_static("no-referrer"));
    headers.insert(X_CONTENT_TYPE_OPTIONS, HeaderValue::from_static("nosniff"));
    response
}

#[cfg(test)]
mod tests {
    use super::*;
    use huddle_catalog::providers::provider_by_id;

    #[test]
    fn k10_the_page_names_an_unexpected_status() {
        let zai = provider_by_id("zai").expect("zai");
        let outcome = Outcome {
            verdict: KeyVerdict::Unexpected(400),
            steps: Vec::new(),
            ready: true,
            changed: true,
        };
        let view = answer(zai, &outcome);
        assert_eq!(view.state, "unchecked");
        assert!(
            view.steps
                .iter()
                .any(|step| step.detail == "answered HTTP 400 to the check"),
            "{:?}",
            view.steps.iter().map(|s| &s.detail).collect::<Vec<_>>()
        );
    }

    #[test]
    fn the_input_needs_a_64_digit_hex_token_and_may_shorten_the_life() {
        let token = "ab".repeat(32);
        let input = parse_input(&format!("{token}\nlife_ms=1000\n")).expect("input");
        assert_eq!(input.life, Duration::from_millis(1000));
        assert_eq!(
            parse_input(&format!("{token}\n")).expect("input").life,
            LIFE
        );
        assert!(parse_input("short\n").is_none());
        let longer = parse_input(&format!("{token}\nlife_ms=99999999\n")).expect("input");
        assert_eq!(longer.life, LIFE, "the life never grows past ten minutes");
    }

    #[test]
    fn same_compares_every_byte() {
        assert!(same(b"abc", b"abc"));
        assert!(!same(b"abc", b"abd"));
        assert!(!same(b"abc", b"ab"));
    }

    #[test]
    fn the_page_fills_every_marker_and_escapes() {
        let zai = provider_by_id("zai").expect("zai");
        let html = render_page(zai, "n0nce", 42, false);
        assert!(!html.contains("{{"));
        assert!(html.contains("<style nonce=\"n0nce\">"));
        assert!(html.contains("Z.ai GLM"));
        assert_eq!(escape("<a&\"'>"), "&lt;a&amp;&quot;&#39;&gt;");
    }

    #[test]
    fn a_closed_page_starts_closed() {
        let zai = provider_by_id("zai").expect("zai");
        assert!(render_page(zai, "n", 1, true).contains("data-start=\"closed\""));
    }
}
