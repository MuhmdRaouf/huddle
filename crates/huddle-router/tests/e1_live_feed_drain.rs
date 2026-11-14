//! E1: a drain ends the dashboard's live feed, so an open dashboard never holds the old router.

use huddle_router::connect::{Phase, TunnelTracker};
use huddle_router::dashboard::Dashboard;
use huddle_router::live::LiveState;
use std::sync::Arc;
use std::time::Duration;

use http_body_util::BodyExt;
use huddle_router::dashboard::DashboardInputs;

#[tokio::test]
async fn e1_the_live_feed_ends_when_the_drain_starts() {
    let tracker = TunnelTracker::new();
    let dashboard = Arc::new(
        Dashboard::new(
            Arc::new(LiveState::new(0)),
            DashboardInputs::default(),
            Arc::new(Vec::new),
        )
        .ending_with(tracker.phase_signal()),
    );
    let mut body = dashboard.live_answer(false).into_body();
    let first = tokio::time::timeout(Duration::from_secs(5), body.frame())
        .await
        .expect("timely");
    assert!(first.is_some(), "the feed opens with a snapshot");
    let draining = {
        let tracker = Arc::clone(&tracker);
        tokio::spawn(async move { tracker.drain(Duration::from_secs(30)).await })
    };
    let ended = tokio::time::timeout(Duration::from_secs(5), async {
        while body.frame().await.is_some() {}
    })
    .await;
    assert!(ended.is_ok(), "the feed ends once the drain has started");
    assert_ne!(*tracker.phase_signal().borrow(), Phase::Serving);
    draining.abort();
}
