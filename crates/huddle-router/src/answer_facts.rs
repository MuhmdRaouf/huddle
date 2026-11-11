//! huddle-router: what the dashboard reads off an Anthropic answer's head. Only the `request-id`
//! and the four unified rate-limit numbers leave the head; no other header value is kept.

use hyper::header::HeaderMap;

const REQUEST_ID: &str = "request-id";
const FIVE_HOUR_USE: &str = "anthropic-ratelimit-unified-5h-utilization";
const FIVE_HOUR_RESET: &str = "anthropic-ratelimit-unified-5h-reset";
const WEEK_USE: &str = "anthropic-ratelimit-unified-7d-utilization";
const WEEK_RESET: &str = "anthropic-ratelimit-unified-7d-reset";

/// The longest `request-id` kept.
const ID_CHARS: usize = 64;
/// Utilization is a fraction of the limit; a value far above 1 is not one.
const MAX_UTILIZATION: f64 = 10.0;

/// Plan usage as the headers state it: percentages and reset times in epoch milliseconds.
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Serialize)]
pub struct PlanUsage {
    pub five_hour_pct: Option<u64>,
    pub five_hour_reset_ms: Option<u64>,
    pub week_pct: Option<u64>,
    pub week_reset_ms: Option<u64>,
}

/// The facts one answer head carries.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct AnswerFacts {
    pub upstream_id: Option<String>,
    pub plan: Option<PlanUsage>,
}

pub fn read(headers: &HeaderMap) -> AnswerFacts {
    let plan = PlanUsage {
        five_hour_pct: utilization(headers, FIVE_HOUR_USE),
        five_hour_reset_ms: reset_ms(headers, FIVE_HOUR_RESET),
        week_pct: utilization(headers, WEEK_USE),
        week_reset_ms: reset_ms(headers, WEEK_RESET),
    };
    let any = plan.five_hour_pct.is_some()
        || plan.five_hour_reset_ms.is_some()
        || plan.week_pct.is_some()
        || plan.week_reset_ms.is_some();
    AnswerFacts {
        upstream_id: request_id(headers),
        plan: any.then_some(plan),
    }
}

/// The id when it is a short run of letters, digits, `_` and `-`.
fn request_id(headers: &HeaderMap) -> Option<String> {
    let value = headers.get(REQUEST_ID)?.to_str().ok()?;
    let plain = !value.is_empty()
        && value.len() <= ID_CHARS
        && value
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-');
    plain.then(|| value.to_owned())
}

fn number(headers: &HeaderMap, name: &str) -> Option<f64> {
    headers
        .get(name)?
        .to_str()
        .ok()?
        .trim()
        .parse::<f64>()
        .ok()
        .filter(|value| value.is_finite() && *value >= 0.0)
}

fn utilization(headers: &HeaderMap, name: &str) -> Option<u64> {
    number(headers, name)
        .filter(|fraction| *fraction <= MAX_UTILIZATION)
        .map(|fraction| (fraction * 100.0).round() as u64)
}

/// The header carries epoch seconds.
fn reset_ms(headers: &HeaderMap, name: &str) -> Option<u64> {
    let seconds = headers
        .get(name)?
        .to_str()
        .ok()?
        .trim()
        .parse::<u64>()
        .ok()?;
    seconds.checked_mul(1000)
}

#[cfg(test)]
mod tests {
    use super::*;
    use hyper::header::{HeaderName, HeaderValue};

    fn headers(pairs: &[(&str, &str)]) -> HeaderMap {
        let mut map = HeaderMap::new();
        for (name, value) in pairs {
            map.insert(
                HeaderName::from_bytes(name.as_bytes()).expect("name"),
                HeaderValue::from_str(value).expect("value"),
            );
        }
        map
    }

    #[test]
    fn the_four_plan_numbers_and_the_id_are_read() {
        let facts = read(&headers(&[
            ("request-id", "req_011CVabc"),
            (FIVE_HOUR_USE, "0.625"),
            (FIVE_HOUR_RESET, "1794303600"),
            (WEEK_USE, "0.41"),
            (WEEK_RESET, "1794800000"),
        ]));
        assert_eq!(facts.upstream_id.as_deref(), Some("req_011CVabc"));
        assert_eq!(
            facts.plan,
            Some(PlanUsage {
                five_hour_pct: Some(63),
                five_hour_reset_ms: Some(1_794_303_600_000),
                week_pct: Some(41),
                week_reset_ms: Some(1_794_800_000_000),
            })
        );
    }

    #[test]
    fn no_headers_mean_no_id_and_no_plan() {
        assert_eq!(read(&HeaderMap::new()), AnswerFacts::default());
    }

    #[test]
    fn a_window_may_carry_only_part_of_the_numbers() {
        let facts = read(&headers(&[(WEEK_USE, "0.1")]));
        let plan = facts.plan.expect("plan");
        assert_eq!((plan.week_pct, plan.five_hour_pct), (Some(10), None));
    }

    #[test]
    fn values_that_are_not_numbers_are_left_out() {
        let facts = read(&headers(&[
            (FIVE_HOUR_USE, "high"),
            (FIVE_HOUR_RESET, "tomorrow"),
            (WEEK_USE, "-1"),
            (WEEK_RESET, "99999999999999999999"),
        ]));
        assert_eq!(facts.plan, None);
    }

    #[test]
    fn an_id_with_other_characters_or_too_long_is_dropped() {
        assert_eq!(read(&headers(&[("request-id", "a b")])).upstream_id, None);
        assert_eq!(
            read(&headers(&[("request-id", &"a".repeat(65))])).upstream_id,
            None
        );
    }
}
