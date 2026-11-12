//! huddle-router: trace.
//!
//! Where an upstream attempt stood and how it got there: the phase it reached, the peer it
//! dialled, whether the socket was pooled, and the time each finished phase took. The log lines
//! that name a failure are built from it.

use std::net::SocketAddr;

/// The step of an upstream attempt a failure or an answer belongs to.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Phase {
    Dns,
    Connect,
    Tls,
    Send,
    Headers,
    Body,
}

impl Phase {
    pub fn as_str(&self) -> &'static str {
        match self {
            Phase::Dns => "dns",
            Phase::Connect => "connect",
            Phase::Tls => "tls",
            Phase::Send => "send",
            Phase::Headers => "headers",
            Phase::Body => "body",
        }
    }

    /// What to check when an attempt ends in this phase.
    pub fn hint(&self) -> &'static str {
        match self {
            Phase::Dns => "check the DNS resolver or the VPN",
            Phase::Connect => "check the network, the firewall or the VPN",
            Phase::Tls => "check for a filtering proxy or an antivirus that rewrites TLS",
            Phase::Send | Phase::Headers | Phase::Body => "Anthropic's side is not answering",
        }
    }
}

/// Whether an attempt ran on a pooled socket or a fresh dial.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Conn {
    Fresh,
    Reused,
}

impl Conn {
    pub fn as_str(&self) -> &'static str {
        match self {
            Conn::Fresh => "fresh",
            Conn::Reused => "reused",
        }
    }
}

/// One attempt's phase, peer and connection, with the milliseconds each finished dial phase took.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Trace {
    pub phase: Phase,
    pub peer: Option<SocketAddr>,
    pub conn: Conn,
    pub dns_ms: Option<u64>,
    pub connect_ms: Option<u64>,
    pub tls_ms: Option<u64>,
}

impl Default for Trace {
    /// An attempt that ended before anything was dialled.
    fn default() -> Trace {
        Trace {
            phase: Phase::Send,
            peer: None,
            conn: Conn::Fresh,
            dns_ms: None,
            connect_ms: None,
            tls_ms: None,
        }
    }
}

impl Trace {
    /// The same attempt, ended in `phase`.
    pub fn at(mut self, phase: Phase) -> Trace {
        self.phase = phase;
        self
    }

    /// A body that broke after the head: nothing else is known.
    pub fn body() -> Trace {
        Trace::default().at(Phase::Body)
    }

    /// The log suffix: `phase=connect peer=1.2.3.4:443 conn=fresh dns_ms=4`.
    pub fn line(&self) -> String {
        let peer = self.peer.map_or_else(|| "-".to_owned(), |p| p.to_string());
        let mut out = format!(
            "phase={} peer={peer} conn={}",
            self.phase.as_str(),
            self.conn.as_str()
        );
        for (name, ms) in [
            ("dns_ms", self.dns_ms),
            ("connect_ms", self.connect_ms),
            ("tls_ms", self.tls_ms),
        ] {
            if let Some(ms) = ms {
                out.push_str(&format!(" {name}={ms}"));
            }
        }
        out
    }

    /// Where it failed, for the failing line: `at connect to 1.2.3.4:443`.
    pub fn place(&self) -> String {
        match self.peer {
            Some(peer) => format!("at {} to {peer}", self.phase.as_str()),
            None => format!("at {}", self.phase.as_str()),
        }
    }

    /// The peer and the connection kind, for the recovered line: `1.2.3.4:443 (fresh)`.
    pub fn route(&self) -> String {
        let peer = self.peer.map_or_else(|| "-".to_owned(), |p| p.to_string());
        format!("{peer} ({})", self.conn.as_str())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_connect_failure_lists_only_the_finished_phases() {
        let trace = Trace {
            phase: Phase::Connect,
            peer: Some("160.79.104.10:443".parse().expect("addr")),
            conn: Conn::Fresh,
            dns_ms: Some(4),
            connect_ms: None,
            tls_ms: None,
        };
        assert_eq!(
            trace.line(),
            "phase=connect peer=160.79.104.10:443 conn=fresh dns_ms=4"
        );
        assert_eq!(trace.place(), "at connect to 160.79.104.10:443");
    }

    #[test]
    fn no_peer_reads_as_a_dash() {
        assert_eq!(Trace::default().line(), "phase=send peer=- conn=fresh");
        assert_eq!(Trace::body().place(), "at body");
    }
}
