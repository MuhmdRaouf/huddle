//! huddle-router: upstream proxy environment.
//!
//! Which proxy an upstream call rides, read from the environment: the variables in either case,
//! `NO_PROXY` exemptions, the CONNECT tunnel with Basic auth, and the deadline on it.

use huddle_config::env::Env;
use huddle_router::upstream::{
    IDLE_TIMEOUT, ProxyTarget, RequestBody, UpstreamClient, UpstreamConfig, UpstreamRequest,
    dial_tunnel, forward_headers, is_loopback_host, no_proxy, proxy_for,
};
use huddle_testkit::fake_upstream::{FakeUpstream, Step};
use hyper::body::Bytes;
use hyper::header::{HeaderMap, HeaderValue};
use hyper::{Method, Uri};
use rustls::RootCertStore;
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpListener, TcpStream};

fn uri(s: &str) -> Uri {
    s.parse().expect("uri")
}

#[test]
fn no_proxy_honours_hosts_suffixes_ports_and_stars() {
    let env = Env::with(&[(
        "NO_PROXY",
        "anthropic.com, .corp *.lan corp2:8080 foo.bar:443:9",
    )]);
    assert!(no_proxy("anthropic.com", 443, &env));
    assert!(no_proxy("api.anthropic.com", 443, &env));
    assert!(!no_proxy("notanthropic.com", 443, &env));
    assert!(no_proxy("x.corp", 443, &env));
    assert!(no_proxy("box.lan", 443, &env));
    assert!(!no_proxy("corp2", 443, &env), "port-scoped entry");
    assert!(no_proxy("corp2", 8080, &env));
    assert!(
        no_proxy("foo.bar", 443, &env),
        "the second segment is the port"
    );
    assert!(no_proxy("anything", 80, &Env::with(&[("no_proxy", "*")])));
    assert!(!no_proxy("anything", 80, &Env::with(&[])));
}

#[test]
fn no_proxy_reads_lowercase_first() {
    let env = Env::with(&[("no_proxy", "a.test"), ("NO_PROXY", "b.test")]);
    assert!(no_proxy("a.test", 443, &env));
    assert!(!no_proxy("b.test", 443, &env));
}

#[test]
fn no_proxy_matches_ipv6_literals_and_cidr_ranges() {
    assert!(no_proxy("::1", 443, &Env::with(&[("NO_PROXY", "::1")])));
    assert!(no_proxy(
        "::1",
        443,
        &Env::with(&[("NO_PROXY", "[::1]:443")])
    ));
    assert!(no_proxy(
        "10.1.2.3",
        443,
        &Env::with(&[("NO_PROXY", "10.0.0.0/8")])
    ));
    assert!(!no_proxy(
        "11.1.2.3",
        443,
        &Env::with(&[("NO_PROXY", "10.0.0.0/8")])
    ));
    assert!(no_proxy(
        "2001:db8::9",
        443,
        &Env::with(&[("NO_PROXY", "2001:db8::/32")])
    ));
    assert!(!no_proxy(
        "example.com",
        443,
        &Env::with(&[("NO_PROXY", "10.0.0.0/8")])
    ));
}

#[test]
fn the_right_variable_is_chosen_by_scheme_and_case() {
    let env = Env::with(&[
        ("HTTPS_PROXY", "http://corp:3128"),
        ("HTTP_PROXY", "http://other:3128"),
    ]);
    assert_eq!(
        proxy_for(&uri("https://api.z.ai/"), &env)
            .expect("proxy")
            .host,
        "corp"
    );
    assert_eq!(
        proxy_for(&uri("http://api.z.ai/"), &env)
            .expect("proxy")
            .host,
        "other"
    );
    let lower = Env::with(&[
        ("https_proxy", "http://low:1"),
        ("HTTPS_PROXY", "http://up:1"),
    ]);
    assert_eq!(
        proxy_for(&uri("https://api.z.ai/"), &lower)
            .expect("proxy")
            .host,
        "low"
    );
    assert!(
        proxy_for(
            &uri("https://api.z.ai/"),
            &Env::with(&[("HTTPS_PROXY", "  ")])
        )
        .is_none()
    );
}

#[test]
fn all_proxy_applies_when_the_specific_one_is_unset() {
    let env = Env::with(&[("ALL_PROXY", "http://corp:3128")]);
    assert_eq!(
        proxy_for(&uri("https://api.z.ai/"), &env)
            .expect("proxy")
            .host,
        "corp"
    );
    assert_eq!(
        proxy_for(&uri("http://api.z.ai/"), &env)
            .expect("proxy")
            .host,
        "corp"
    );
    let specific = Env::with(&[
        ("ALL_PROXY", "http://corp:1"),
        ("HTTPS_PROXY", "http://spec:1"),
    ]);
    assert_eq!(
        proxy_for(&uri("https://api.z.ai/"), &specific)
            .expect("proxy")
            .host,
        "spec"
    );
}

#[test]
fn scheme_less_values_and_portless_proxies_work() {
    let p = proxy_for(
        &uri("https://api.z.ai/"),
        &Env::with(&[("HTTPS_PROXY", "corp:3128")]),
    )
    .expect("proxy");
    assert_eq!((p.host.as_str(), p.port), ("corp", 3128));
    let p = proxy_for(
        &uri("https://api.z.ai/"),
        &Env::with(&[("HTTPS_PROXY", "http://corp")]),
    )
    .expect("proxy");
    assert_eq!(p.port, 80, "portless proxy means 80");
}

#[test]
fn credentials_are_read_and_percent_decoded() {
    let p = proxy_for(
        &uri("https://api.z.ai/"),
        &Env::with(&[("HTTPS_PROXY", "http://me%40corp:p%21ss@corp:3128")]),
    )
    .expect("proxy");
    assert_eq!(p.user, Some(("me@corp".to_string(), "p!ss".to_string())));
}

#[test]
fn no_proxy_stops_the_proxy() {
    let env = Env::with(&[
        ("HTTPS_PROXY", "http://corp:3128"),
        ("NO_PROXY", "api.z.ai"),
    ]);
    assert!(proxy_for(&uri("https://api.z.ai/"), &env).is_none());
}

#[test]
fn a_proxy_naming_huddle_router_is_no_proxy() {
    for value in [
        "http://127.0.0.1:4835",
        "http://localhost:4835",
        "127.0.0.1:4835",
    ] {
        assert!(
            proxy_for(
                &uri("https://api.z.ai/"),
                &Env::with(&[("HTTPS_PROXY", value)])
            )
            .is_none(),
            "{value}"
        );
    }
    assert!(
        proxy_for(
            &uri("https://api.z.ai/"),
            &Env::with(&[("HTTPS_PROXY", "http://127.0.0.1:4836")])
        )
        .is_some(),
        "another loopback port is kept"
    );
}

#[test]
fn loopback_means_a_real_loopback_address_only() {
    assert!(is_loopback_host("localhost"));
    assert!(is_loopback_host("127.0.0.1"));
    assert!(is_loopback_host("127.5.6.7"));
    assert!(is_loopback_host("::1"));
    assert!(!is_loopback_host("127.example.com"));
    assert!(!is_loopback_host("127.0.0.1.nip.io"));
}

async fn test_proxy(reject: bool) -> (u16, Arc<Mutex<Vec<String>>>) {
    let listener = TcpListener::bind(("127.0.0.1", 0)).await.expect("bind");
    let port = listener.local_addr().expect("addr").port();
    let heads: Arc<Mutex<Vec<String>>> = Arc::default();
    let seen = heads.clone();
    tokio::spawn(async move {
        while let Ok((mut sock, _)) = listener.accept().await {
            let seen = seen.clone();
            tokio::spawn(async move {
                let mut head = Vec::new();
                let mut byte = [0u8; 1];
                while !head.ends_with(b"\r\n\r\n") {
                    if sock.read_exact(&mut byte).await.is_err() {
                        return;
                    }
                    head.push(byte[0]);
                }
                let text = String::from_utf8_lossy(&head).into_owned();
                seen.lock().unwrap().push(text.clone());
                if reject {
                    let _ = sock
                        .write_all(b"HTTP/1.1 407 Proxy Auth Required\r\n\r\n")
                        .await;
                    return;
                }
                let target = text.split_whitespace().nth(1).expect("target").to_string();
                let mut upstream = TcpStream::connect(&target).await.expect("reach upstream");
                let _ = sock
                    .write_all(b"HTTP/1.1 200 Connection established\r\n\r\n")
                    .await;
                let _ = tokio::io::copy_bidirectional(&mut sock, &mut upstream).await;
            });
        }
    });
    (port, heads)
}

fn client(env: Env) -> UpstreamClient {
    UpstreamClient::new(UpstreamConfig {
        env,
        roots: RootCertStore::empty(),
        idle: IDLE_TIMEOUT,
    })
}

fn post(url: &str) -> UpstreamRequest {
    let uri: Uri = url.parse().expect("uri");
    let mut headers = HeaderMap::new();
    headers.insert("x-test", HeaderValue::from_static("1"));
    UpstreamRequest {
        method: Method::POST,
        headers: forward_headers(&headers, &uri, 2),
        url: uri,
        body: RequestBody::Whole(Bytes::from_static(b"{}")),
    }
}

#[tokio::test]
async fn upstream_traffic_rides_a_connect_tunnel_with_basic_auth() {
    let upstream = FakeUpstream::start(|_, _| Step::answer(200, "via proxy")).await;
    let (proxy_port, heads) = test_proxy(false).await;
    let env = Env::with(&[(
        "HTTP_PROXY",
        &format!("http://me:pw@127.0.0.1:{proxy_port}"),
    )]);
    let answer = client(env)
        .send(post(&format!("{}/v1/messages", upstream.url())))
        .await
        .expect("send");
    assert_eq!(answer.status.as_u16(), 200);
    let head = heads.lock().unwrap()[0].clone();
    let upstream_addr = upstream.url().trim_start_matches("http://").to_string();
    assert!(
        head.starts_with(&format!("CONNECT {upstream_addr} HTTP/1.1\r\n")),
        "{head}"
    );
    assert!(
        head.contains(&format!("Host: {upstream_addr}\r\n")),
        "{head}"
    );
    assert!(
        head.contains("Proxy-Authorization: Basic bWU6cHc=\r\n"),
        "{head}"
    );
}

#[tokio::test]
async fn a_proxy_that_refuses_the_connect_is_an_error() {
    let (proxy_port, _heads) = test_proxy(true).await;
    let proxy = ProxyTarget {
        host: "127.0.0.1".into(),
        port: proxy_port,
        user: None,
    };
    let err = dial_tunnel(&proxy, "api.z.ai", 443, Duration::from_secs(3))
        .await
        .expect_err("refused");
    assert!(
        err.to_string()
            .contains("proxy CONNECT api.z.ai:443 answered 407"),
        "{err}"
    );
}

#[tokio::test]
async fn a_proxy_that_never_answers_hits_the_deadline() {
    let listener = TcpListener::bind(("127.0.0.1", 0)).await.expect("bind");
    let port = listener.local_addr().expect("addr").port();
    tokio::spawn(async move {
        let accepted = listener.accept().await;
        tokio::time::sleep(Duration::from_secs(5)).await;
        drop(accepted);
    });
    let proxy = ProxyTarget {
        host: "127.0.0.1".into(),
        port,
        user: None,
    };
    let started = std::time::Instant::now();
    assert!(
        dial_tunnel(&proxy, "api.z.ai", 443, Duration::from_millis(100))
            .await
            .is_err()
    );
    assert!(started.elapsed() < Duration::from_secs(2));
}
