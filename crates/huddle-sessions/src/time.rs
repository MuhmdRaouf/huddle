//! RFC 3339 UTC timestamps (`2026-11-11T11:43:01.903Z`) as epoch milliseconds.

/// Parses `YYYY-MM-DDTHH:MM:SS[.fff]Z`; anything else is `None`.
pub fn parse_iso_ms(text: &str) -> Option<u64> {
    let text = text.strip_suffix('Z')?;
    let (date, time) = text.split_once('T')?;
    let mut date = date.split('-');
    let year: i64 = date.next()?.parse().ok()?;
    let month: i64 = date.next()?.parse().ok()?;
    let day: i64 = date.next()?.parse().ok()?;
    let (clock, fraction) = time.split_once('.').unwrap_or((time, ""));
    let mut clock = clock.split(':');
    let hour: i64 = clock.next()?.parse().ok()?;
    let minute: i64 = clock.next()?.parse().ok()?;
    let second: i64 = clock.next()?.parse().ok()?;
    if !(1..=12).contains(&month)
        || !(1..=31).contains(&day)
        || hour > 23
        || minute > 59
        || second > 60
    {
        return None;
    }
    let millis: i64 = match fraction {
        "" => 0,
        digits => {
            let head: String = digits.chars().take(3).collect();
            format!("{head:0<3}").parse().ok()?
        }
    };
    let seconds = days_from_civil(year, month, day) * 86_400 + hour * 3_600 + minute * 60 + second;
    u64::try_from(seconds * 1_000 + millis).ok()
}

/// Days since 1970-01-01 in the proleptic Gregorian calendar.
fn days_from_civil(year: i64, month: i64, day: i64) -> i64 {
    let year = if month <= 2 { year - 1 } else { year };
    let era = year.div_euclid(400);
    let year_of_era = year - era * 400;
    let shifted_month = (month + 9) % 12;
    let day_of_year = (153 * shifted_month + 2) / 5 + day - 1;
    let day_of_era = year_of_era * 365 + year_of_era / 4 - year_of_era / 100 + day_of_year;
    era * 146_097 + day_of_era - 719_468
}

#[cfg(test)]
mod tests {
    use super::parse_iso_ms;

    #[test]
    fn parses_known_instants() {
        assert_eq!(parse_iso_ms("1970-01-01T00:00:00Z"), Some(0));
        assert_eq!(
            parse_iso_ms("2000-03-01T00:00:00.5Z"),
            Some(951_868_800_500)
        );
        assert_eq!(
            parse_iso_ms("2026-11-11T11:43:01.903Z"),
            Some(1_794_397_381_903)
        );
    }

    #[test]
    fn rejects_other_shapes() {
        for bad in [
            "",
            "yesterday",
            "2026-11-11T11:43:01+01:00",
            "2026-13-01T00:00:00Z",
        ] {
            assert_eq!(parse_iso_ms(bad), None, "{bad}");
        }
    }
}
