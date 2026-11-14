//! What the dashboard derives from the recent rows, counted by the server so the snapshot can go
//! without the rows: the window counts, each session's totals and each lane's failure causes.

use std::collections::BTreeMap;

use crate::live::{LaneRow, RecentRow};

/// The window the live pages read, in milliseconds.
const WINDOW_MS: u64 = 15 * 60 * 1000;
const CLIENT_LEFT: u16 = 499;

#[derive(Debug, Clone, Default, PartialEq, Eq, serde::Serialize)]
pub struct RecentCounts {
    /// Finished requests in the window that ended with 499.
    pub cancelled_15m: u32,
    /// Finished requests in the window whose status is not 200.
    pub failed_15m: u32,
    /// Every finished request in the ring, by the session that sent it.
    pub by_session: BTreeMap<String, SessionCounts>,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, serde::Serialize)]
pub struct SessionCounts {
    pub requests: u32,
    pub errors: u32,
    /// Fresh input plus cache writes plus cache reads.
    pub tokens_in: u64,
    pub cache_read: u64,
    pub tokens_out: u64,
}

/// One failure cause of a lane in the window: the router's reason, else the status.
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize)]
pub struct CauseCount {
    pub cause: String,
    pub count: u32,
}

fn is_error(status: u16) -> bool {
    status >= 400 && status != CLIENT_LEFT
}

/// The ring is newest first.
pub fn count(recent: &[RecentRow], now_ms: u64) -> RecentCounts {
    let since = now_ms.saturating_sub(WINDOW_MS);
    let mut counts = RecentCounts::default();
    for row in recent {
        if row.at_ms >= since {
            counts.cancelled_15m += u32::from(row.status == CLIENT_LEFT);
            counts.failed_15m += u32::from(row.status != 200);
        }
        let Some(session) = &row.session else {
            continue;
        };
        let entry = counts.by_session.entry(session.clone()).or_default();
        entry.requests += 1;
        entry.errors += u32::from(is_error(row.status));
        let (read, write) = (row.cache_read.unwrap_or(0), row.cache_write.unwrap_or(0));
        let fresh = row
            .tokens_in
            .map_or(0, |total| total.saturating_sub(read + write));
        entry.tokens_in += fresh + write + read;
        entry.cache_read += read;
        entry.tokens_out += row.tokens_out.unwrap_or(0);
    }
    counts
}

/// Sets each lane's `quota_out` and `causes` from the rows in the window.
pub fn mark_lanes(lanes: &mut [LaneRow], recent: &[RecentRow], now_ms: u64) {
    let since = now_ms.saturating_sub(WINDOW_MS);
    for lane in lanes {
        let rows: Vec<&RecentRow> = recent
            .iter()
            .filter(|row| row.lane == lane.id && row.at_ms >= since)
            .collect();
        lane.quota_out = rows
            .iter()
            .max_by_key(|row| row.at_ms)
            .is_some_and(|row| row.status == 429 && row.reason.as_deref() == Some("quota"));
        let mut causes: Vec<CauseCount> = Vec::new();
        // Oldest first, so equal counts keep the order the causes first appeared in.
        for row in rows.into_iter().rev() {
            if !is_error(row.status) {
                continue;
            }
            let cause = row.reason.clone().unwrap_or_else(|| row.status.to_string());
            match causes.iter_mut().find(|c| c.cause == cause) {
                Some(found) => found.count += 1,
                None => causes.push(CauseCount { cause, count: 1 }),
            }
        }
        causes.sort_by_key(|c| std::cmp::Reverse(c.count));
        lane.causes = causes;
    }
}
