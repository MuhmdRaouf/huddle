//! Wall-clock helpers: epoch milliseconds now and the UTC stamp the logs print.

use std::time::{SystemTime, UNIX_EPOCH};

/// `2026-11-09T10:15:30.123Z` from epoch milliseconds, UTC.
pub fn iso_utc_ms(epoch_ms: i64) -> String {
    let secs = epoch_ms.div_euclid(1_000);
    let (year, month, day) = civil_from_days(secs.div_euclid(86_400));
    let clock = secs.rem_euclid(86_400);
    let (hour, min, sec) = (clock / 3_600, (clock % 3_600) / 60, clock % 60);
    let ms = epoch_ms.rem_euclid(1_000);
    format!("{year:04}-{month:02}-{day:02}T{hour:02}:{min:02}:{sec:02}.{ms:03}Z")
}

/// Epoch milliseconds now.
pub fn now_ms() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|since| since.as_millis() as i64)
        .unwrap_or(0)
}

/// `(year, month, day)` for a day count from 1970-01-01.
fn civil_from_days(days: i64) -> (i64, u32, u32) {
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1_460 + doe / 36_524 - doe / 146_096) / 365;
    let year = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let day = (doy - (153 * mp + 2) / 5 + 1) as u32;
    let month = if mp < 10 { mp + 3 } else { mp - 9 } as u32;
    (year + i64::from(month <= 2), month, day)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn dates_print_like_iso_utc() {
        assert_eq!(iso_utc_ms(1_794_220_530_123), "2026-11-09T10:35:30.123Z");
        assert_eq!(iso_utc_ms(0), "1970-01-01T00:00:00.000Z");
        assert_eq!(iso_utc_ms(951_782_400_000), "2000-02-29T00:00:00.000Z");
    }
}
