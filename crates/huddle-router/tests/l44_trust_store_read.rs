//! L44: the OS trust store is read when the first upstream TLS connection needs it, once, and
//! never by an idle router.

use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};

use http_body_util::BodyExt;
use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_router::dashboard::DashboardInputs;
use huddle_router::dispatch::{Router, RouterConfig, Tuning};
use huddle_router::upstream::{
    FailureCode, IDLE_TIMEOUT, RequestBody, RootsSource, UpstreamClient, UpstreamRequest,
    forward_headers, log_failures,
};
use huddle_testkit::fake_upstream::{FakeUpstream, Step};
use huddle_testkit::temp_home::TempHome;
use hyper::body::Bytes;
use hyper::header::HeaderMap;
use hyper::{Method, Uri};
use rustls::RootCertStore;

/// A roots source that counts its reads and answers `answer` each time.
fn counting(
    reads: &Arc<AtomicUsize>,
    answer: impl Fn(usize) -> Result<RootCertStore, String> + Send + Sync + 'static,
) -> RootsSource {
    let reads = Arc::clone(reads);
    Arc::new(move || answer(reads.fetch_add(1, Ordering::SeqCst) + 1))
}

fn trusting(upstream: &FakeUpstream) -> RootCertStore {
    let mut roots = RootCertStore::empty();
    roots
        .add(rustls::pki_types::CertificateDer::from(upstream.ca_der()))
        .expect("root");
    roots
}

fn post(url: &str) -> UpstreamRequest {
    let uri: Uri = url.parse().expect("uri");
    UpstreamRequest {
        method: Method::POST,
        headers: forward_headers(&HeaderMap::new(), &uri, 2),
        url: uri,
        body: RequestBody::Whole(Bytes::from_static(b"{}")),
    }
}

#[tokio::test]
async fn l44_an_idle_router_never_reads_the_trust_store() {
    let home = TempHome::new();
    let root = home.root().to_str().expect("utf8").to_string();
    let env = Env::with(&[("HOME", root.as_str())]);
    let paths = Paths::from_env(&env).expect("paths");
    let reads = Arc::new(AtomicUsize::new(0));
    let router = Router::with_roots(
        RouterConfig {
            env,
            paths,
            port: 0,
            anthropic_url: "http://127.0.0.1:9".parse::<Uri>().expect("uri"),
            log: Arc::new(|_| {}),
            history: None,
            dashboard: DashboardInputs::default(),
        },
        &[],
        Tuning::default(),
        counting(&reads, |_| Err("must not be read".to_owned())),
    );
    tokio::time::sleep(std::time::Duration::from_millis(200)).await;
    assert_eq!(reads.load(Ordering::SeqCst), 0);
    drop(router);
}

#[tokio::test]
async fn l44_concurrent_first_requests_share_one_trust_store_read() {
    let upstream = FakeUpstream::start_tls(|_, _| Step::answer(200, "tls ok")).await;
    let roots = trusting(&upstream);
    let reads = Arc::new(AtomicUsize::new(0));
    let client = Arc::new(UpstreamClient::lazy(
        Env::with(&[]),
        IDLE_TIMEOUT,
        counting(&reads, move |_| Ok(roots.clone())),
    ));
    let url = format!("{}/v1/messages", upstream.url());
    let sends: Vec<_> = (0..8)
        .map(|_| {
            let (client, url) = (Arc::clone(&client), url.clone());
            tokio::spawn(async move {
                let answer = client.send(post(&url)).await.expect("send");
                let status = answer.status.as_u16();
                let _ = answer.body.collect().await.expect("body");
                status
            })
        })
        .collect();
    for send in sends {
        assert_eq!(send.await.expect("join"), 200);
    }
    assert_eq!(reads.load(Ordering::SeqCst), 1);
}

#[tokio::test]
async fn l44_a_failed_trust_store_read_fails_the_request_and_is_tried_again() {
    let upstream = FakeUpstream::start_tls(|_, _| Step::answer(200, "tls ok")).await;
    let roots = trusting(&upstream);
    let reads = Arc::new(AtomicUsize::new(0));
    let lines: Arc<std::sync::Mutex<Vec<String>>> = Arc::default();
    let sink = Arc::clone(&lines);
    let client = UpstreamClient::lazy(
        Env::with(&[]),
        IDLE_TIMEOUT,
        log_failures(
            counting(&reads, move |read| {
                if read == 1 {
                    Err("reading the OS trust store failed: denied".to_owned())
                } else {
                    Ok(roots.clone())
                }
            }),
            Arc::new(move |line| sink.lock().expect("lines").push(line)),
        ),
    );
    let url = format!("{}/v1/messages", upstream.url());
    let failed = client.send(post(&url)).await.err().expect("first fails");
    assert_eq!(failed.code, FailureCode::ECert);
    let answer = client.send(post(&url)).await.expect("second succeeds");
    assert_eq!(answer.status.as_u16(), 200);
    assert_eq!(reads.load(Ordering::SeqCst), 2);
    assert_eq!(
        *lines.lock().expect("lines"),
        ["huddle-router: reading the OS trust store failed: denied"]
    );
}
