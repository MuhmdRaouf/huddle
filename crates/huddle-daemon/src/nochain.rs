//! The no-chain rule: a router never routes upstream through a router. `clean_proxy_env` drops
//! every proxy variable that names a router — this router at huddle-router's canonical address
//! or on its own port, or any loopback proxy whose health answer names huddle-router — and keeps
//! the rest, so the user's own proxy survives.

use huddle_config::env::Env;
use huddle_router::health::HEALTH_PATH;
use huddle_router::server::ROUTER_NAME;
use std::io::{Read, Write};
use std::net::{IpAddr, TcpStream, ToSocketAddrs};
use std::time::Duration;

/// The variables an outbound proxy is read from, both spellings.
pub const PROXY_VARS: [&str; 6] = [
    "HTTPS_PROXY",
    "https_proxy",
    "HTTP_PROXY",
    "http_proxy",
    "ALL_PROXY",
    "all_proxy",
];

pub const CANONICAL_PORT: u16 = 4835;

/// How long one health probe of a loopback proxy may take.
pub const PROBE_WAIT: Duration = Duration::from_millis(300);

/// A probe stops reading past this: a health answer is small, and a flood is not a router.
const MAX_PROBE_ANSWER: usize = 64 * 1024;

/// Whether only this machine may reach the host: exactly `localhost` (lowercase), or an IP that
/// says loopback. `127.example.com` and `LocalHost` are names, not loopback.
pub fn is_loopback(host: &str) -> bool {
    if host == "localhost" {
        return true;
    }
    let bare = host
        .strip_prefix('[')
        .and_then(|rest| rest.strip_suffix(']'))
        .unwrap_or(host);
    bare.parse::<IpAddr>().is_ok_and(|ip| ip.is_loopback())
}

/// Whether a proxy variable's value names a router: the canonical port or `own_port` on a
/// loopback host. Another port, a non-loopback host, an empty or unparsable value is not one.
pub fn is_router_proxy(raw: &str, own_port: u16) -> bool {
    match proxy_parts(raw) {
        Some((host, Some(port))) => {
            is_loopback(host) && (port == CANONICAL_PORT || port == own_port)
        }
        _ => false,
    }
}

/// A copy of the environment without the proxy variables that name a router, each dropped
/// variable logged once. A loopback proxy is recognised by asking it for the health answer; a
/// non-loopback proxy is kept and never contacted.
pub fn clean_proxy_env(env: &Env, own_port: u16, log: &dyn Fn(String)) -> Env {
    let mut dropped: Vec<&str> = Vec::new();
    for name in PROXY_VARS {
        let Some(value) = env.get(name) else {
            continue;
        };
        if value.trim().is_empty() {
            continue;
        }
        if is_router_proxy(value, own_port) || answers_router_health(value) {
            dropped.push(name);
            log(format!(
                "huddle-router: {name}={value} is a router; going direct"
            ));
        }
    }
    env.without(&dropped)
}

/// A proxy value's host and port: `None` when the value does not parse or its port is not a
/// number 1..65535. A value without a scheme reads as `http`; user info, a path, a query and a
/// fragment are cut; an IPv6 host keeps its brackets.
fn proxy_parts(raw: &str) -> Option<(&str, Option<u16>)> {
    let rest = match raw.split_once("://") {
        Some((_, after)) => after,
        None => raw,
    };
    let rest = rest.split(['/', '?', '#']).next().unwrap_or(rest);
    let rest = match rest.rfind('@') {
        Some(at) => &rest[at + 1..],
        None => rest,
    };
    let (host, port) = match rest.rfind(']') {
        Some(close) if rest.starts_with('[') => {
            (&rest[..=close], rest[close + 1..].strip_prefix(':'))
        }
        _ => match rest.rfind(':') {
            Some(colon) => (&rest[..colon], Some(&rest[colon + 1..])),
            None => (rest, None),
        },
    };
    let port = match port {
        Some(text) => match text.parse::<u16>() {
            Ok(number) if number >= 1 => Some(number),
            _ => return None,
        },
        None => None,
    };
    Some((host, port))
}

/// Whether a proxy value names a loopback host with a port whose health answer names
/// huddle-router. Anything else is never dialled.
fn answers_router_health(raw: &str) -> bool {
    match proxy_parts(raw) {
        Some((host, Some(port))) => is_loopback(host) && probe_health(host, port),
        _ => false,
    }
}

/// One plain `GET` of the health path over a direct TCP connection: no proxy applies. True only
/// when the answer is status 200 and its JSON body names huddle-router.
fn probe_health(host: &str, port: u16) -> bool {
    let bare = host
        .strip_prefix('[')
        .and_then(|rest| rest.strip_suffix(']'))
        .unwrap_or(host);
    let Ok(mut addresses) = (bare, port).to_socket_addrs() else {
        return false;
    };
    let Some(address) = addresses.next() else {
        return false;
    };
    let Ok(mut stream) = TcpStream::connect_timeout(&address, PROBE_WAIT) else {
        return false;
    };
    if stream.set_read_timeout(Some(PROBE_WAIT)).is_err() {
        return false;
    }
    let request = format!("GET {HEALTH_PATH} HTTP/1.0\r\nHost: {bare}:{port}\r\n\r\n");
    if stream.write_all(request.as_bytes()).is_err() {
        return false;
    }
    let mut answer = Vec::new();
    let mut buffer = [0u8; 1024];
    loop {
        match stream.read(&mut buffer) {
            Ok(0) => break,
            Ok(read) => answer.extend_from_slice(&buffer[..read]),
            Err(_) => return false,
        }
        if answer.len() > MAX_PROBE_ANSWER {
            return false;
        }
    }
    names_router(&answer)
}

/// Whether a raw HTTP answer is status 200 with a JSON body whose `name` is exactly
/// huddle-router. A 200 alone is any web server; the name is ours.
fn names_router(answer: &[u8]) -> bool {
    let Ok(text) = std::str::from_utf8(answer) else {
        return false;
    };
    let Some((head, body)) = text.split_once("\r\n\r\n") else {
        return false;
    };
    let Some(status) = head.lines().next() else {
        return false;
    };
    if !status.starts_with("HTTP/") || status.split_whitespace().nth(1) != Some("200") {
        return false;
    }
    let Ok(json) = serde_json::from_slice::<serde_json::Value>(body.as_bytes()) else {
        return false;
    };
    json.get("name").and_then(serde_json::Value::as_str) == Some(ROUTER_NAME)
}

#[cfg(test)]
mod tests {
    use super::*;
    use huddle_config::env::Env;
    use std::{
        io::{Read, Write},
        net::TcpListener,
        sync::{Arc, Mutex},
        thread,
    };

    fn fake_health(answer: &'static str) -> (u16, Arc<Mutex<u32>>) {
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind");
        let port = listener.local_addr().expect("addr").port();
        let hits = Arc::new(Mutex::new(0));
        let counted = Arc::clone(&hits);
        thread::spawn(move || {
            for stream in listener.incoming() {
                let Ok(mut stream) = stream else { return };
                *counted.lock().expect("hits") += 1;
                let mut buffer = [0u8; 1024];
                let _ = stream.read(&mut buffer);
                let _ = stream.write_all(answer.as_bytes());
            }
        });
        (port, hits)
    }

    fn logged() -> (Arc<Mutex<Vec<String>>>, impl Fn(String)) {
        let lines = Arc::new(Mutex::new(Vec::new()));
        let sink = Arc::clone(&lines);
        (lines, move |line: String| {
            sink.lock().expect("lines").push(line)
        })
    }

    #[test]
    fn loopback_is_a_real_ip_or_localhost() {
        for host in ["localhost", "127.0.0.1", "127.0.0.2", "::1", "[::1]"] {
            assert!(is_loopback(host), "{host}");
        }
        for host in [
            "127.example.com",
            "LocalHost",
            "localhost.example",
            "::ffff:127.0.0.1",
            "example.com",
            "",
        ] {
            assert!(!is_loopback(host), "{host}");
        }
    }

    #[test]
    fn the_canonical_address_and_our_own_port_are_routers() {
        for value in [
            "http://127.0.0.1:4835",
            "127.0.0.1:4835",
            "localhost:4835",
            "http://localhost:4835/",
        ] {
            assert!(is_router_proxy(value, 20000), "{value}");
        }
        assert!(is_router_proxy("http://127.0.0.1:20000", 20000));
        assert!(is_router_proxy("http://[::1]:20000", 20000));
        assert!(!is_router_proxy("http://127.0.0.1:20001", 20000));
        assert!(!is_router_proxy("http://corp.example:4835", 20000));
        assert!(!is_router_proxy("", 20000));
    }

    #[test]
    fn a_proxy_on_our_own_port_is_dropped_without_a_request() {
        let (lines, log) = logged();
        let env = Env::with(&[("HTTPS_PROXY", "http://127.0.0.1:20000"), ("NO_PROXY", "x")]);
        let cleaned = clean_proxy_env(&env, 20000, &log);
        assert_eq!(cleaned.get("HTTPS_PROXY"), None);
        assert_eq!(cleaned.get("NO_PROXY"), Some("x"));
        assert_eq!(
            *lines.lock().expect("lines"),
            vec![
                "huddle-router: HTTPS_PROXY=http://127.0.0.1:20000 is a router; going direct"
                    .to_string()
            ]
        );
    }

    #[test]
    fn a_loopback_proxy_answering_our_health_name_is_dropped() {
        let (port, _hits) = fake_health(
            "HTTP/1.1 200 OK\r\nContent-Length: 25\r\n\r\n{\"name\":\"huddle-router\"}",
        );
        let value = format!("http://127.0.0.1:{port}");
        let env = Env::with(&[("HTTPS_PROXY", value.as_str())]);
        let (_lines, log) = logged();
        assert_eq!(clean_proxy_env(&env, 20000, &log).get("HTTPS_PROXY"), None);
    }

    #[test]
    fn a_loopback_proxy_answering_any_other_200_is_kept() {
        let (port, _hits) = fake_health("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n{}");
        let value = format!("http://127.0.0.1:{port}");
        let env = Env::with(&[("HTTPS_PROXY", value.as_str())]);
        let (_lines, log) = logged();
        assert_eq!(
            clean_proxy_env(&env, 20000, &log).get("HTTPS_PROXY"),
            Some(value.as_str())
        );
    }

    #[test]
    fn a_non_loopback_proxy_is_kept_and_never_contacted() {
        let env = Env::with(&[("HTTPS_PROXY", "http://proxy.corp.example:3128")]);
        let (lines, log) = logged();
        let cleaned = clean_proxy_env(&env, 20000, &log);
        assert_eq!(
            cleaned.get("HTTPS_PROXY"),
            Some("http://proxy.corp.example:3128")
        );
        assert!(lines.lock().expect("lines").is_empty());
    }
}
