mod e2e_support;

use e2e_support::{Daemon, request};
use huddle_history::{Filter, History, Page, Range};
use std::time::{Duration, Instant};

const GLM_BODY: &[u8] = br#"{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}"#;
const CLAUDE_BODY: &[u8] =
    br#"{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}"#;
const JSON: (&str, &str) = ("content-type", "application/json");

#[test]
fn t5_every_finished_request_becomes_a_row_and_no_events_folder_appears() {
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
    for n in 0..5 {
        let body = if n % 2 == 0 { GLM_BODY } else { CLAUDE_BODY };
        let reply = request(daemon.port, "POST", "/v1/messages", &[JSON], body);
        assert_eq!(reply.status, 200, "{}", reply.body);
    }

    let state = daemon.home.root().join(".local/state/huddle");
    let db = state.join("huddle.db");
    let filter = Filter::new(Range {
        from_ms: 0,
        to_ms: u64::MAX / 2,
    });
    let deadline = Instant::now() + Duration::from_secs(15);
    let found = loop {
        let found = History::open(&db)
            .and_then(|history| {
                history.search(
                    filter.clone(),
                    Page {
                        limit: 100,
                        offset: 0,
                    },
                )
            })
            .map(|result| result.rows)
            .unwrap_or_default();
        if found.len() >= 5 || Instant::now() > deadline {
            break found;
        }
        std::thread::sleep(Duration::from_millis(100));
    };

    assert_eq!(found.len(), 5, "{found:?}");
    assert_eq!(found.iter().filter(|r| r.lane == "zai").count(), 3);
    assert_eq!(found.iter().filter(|r| r.lane == "anthropic").count(), 2);
    assert!(found.iter().all(|r| r.status == 200));
    assert!(!state.join("events").exists());
}
