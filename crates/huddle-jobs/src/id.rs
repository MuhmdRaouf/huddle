//! huddle-jobs: id.

use std::fs::File;
use std::io::Read;
use std::path::Path;
use std::time::{SystemTime, UNIX_EPOCH};

/// Crockford base32, lowercase: no i, l, o or u, so ids survive being read aloud or retyped.
const BASE32: &[u8; 32] = b"0123456789abcdefghjkmnpqrstvwxyz";

/// Milliseconds in one day.
const MS_PER_DAY: u64 = 86_400_000;

/// Milliseconds since the Unix epoch; 0 when the clock is before it.
pub fn now_ms() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|since| since.as_millis() as u64)
        .unwrap_or(0)
}

/// "<yymmdd UTC>-<6 chars of 0123456789abcdefghjkmnpqrstvwxyz>", each char a random byte mod 32.
pub fn new_job_id(now_ms: u64) -> std::io::Result<String> {
    let (year, month, day) = civil_from_days((now_ms / MS_PER_DAY) as i64);
    let random = random_bytes(6)?;
    let suffix: String = random
        .iter()
        .map(|byte| BASE32[usize::from(byte % 32)] as char)
        .collect();
    Ok(format!(
        "{:02}{:02}{:02}-{}",
        year % 100,
        month,
        day,
        suffix
    ))
}

/// RFC 4122 v4 UUID, lowercase hex with dashes: bytes from /dev/urandom, byte 6 high nibble 4,
/// byte 8 high bits 10.
pub fn new_session_id() -> std::io::Result<String> {
    let mut bytes = random_bytes(16)?;
    bytes[6] = (bytes[6] & 0x0f) | 0x40;
    bytes[8] = (bytes[8] & 0x3f) | 0x80;
    let hex: String = bytes.iter().map(|byte| format!("{byte:02x}")).collect();
    Ok(format!(
        "{}-{}-{}-{}-{}",
        &hex[0..8],
        &hex[8..12],
        &hex[12..16],
        &hex[16..20],
        &hex[20..32]
    ))
}

/// "2026-11-09T10:00:00.000Z"
pub fn iso_ms(ms: u64) -> String {
    let (year, month, day) = civil_from_days((ms / MS_PER_DAY) as i64);
    let rest = ms % MS_PER_DAY;
    let hour = rest / 3_600_000;
    let minute = (rest % 3_600_000) / 60_000;
    let second = (rest % 60_000) / 1000;
    let milli = rest % 1000;
    format!("{year:04}-{month:02}-{day:02}T{hour:02}:{minute:02}:{second:02}.{milli:03}Z")
}

/// Inverse of iso_ms; None for anything else. Month 1..=12, day 1..=31, hour <24, minute and
/// second <60.
pub fn parse_iso_ms(text: &str) -> Option<u64> {
    let bytes = text.as_bytes();
    if bytes.len() != 24
        || bytes[4] != b'-'
        || bytes[7] != b'-'
        || bytes[10] != b'T'
        || bytes[13] != b':'
        || bytes[16] != b':'
        || bytes[19] != b'.'
        || bytes[23] != b'Z'
    {
        return None;
    }
    let year = digits(&text[0..4])?;
    let month = digits(&text[5..7])?;
    let day = digits(&text[8..10])?;
    let hour = digits(&text[11..13])?;
    let minute = digits(&text[14..16])?;
    let second = digits(&text[17..19])?;
    let milli = digits(&text[20..23])?;
    if !(1..=12).contains(&month)
        || !(1..=31).contains(&day)
        || hour >= 24
        || minute >= 60
        || second >= 60
    {
        return None;
    }
    let days = days_from_civil(i64::from(year), month, day);
    Some(
        u64::try_from(days).ok()? * MS_PER_DAY
            + u64::from(hour) * 3_600_000
            + u64::from(minute) * 60_000
            + u64::from(second) * 1000
            + u64::from(milli),
    )
}

/// "<basename, every char not [A-Za-z0-9._-] replaced by '-', cut to 40 chars>-<first 16 hex of
/// the SHA-256 of the canonical path>". Canonicalize failure: the path as given. SHA-256 through
/// ring::digest (a huddle-jobs dependency).
pub fn repo_slug(repo_root: &Path) -> String {
    let canonical = repo_root
        .canonicalize()
        .unwrap_or_else(|_| repo_root.to_path_buf());
    let base = canonical
        .file_name()
        .map(|name| name.to_string_lossy().into_owned())
        .unwrap_or_default();
    let name: String = base
        .chars()
        .map(|c| {
            if c.is_ascii_alphanumeric() || matches!(c, '.' | '_' | '-') {
                c
            } else {
                '-'
            }
        })
        .take(40)
        .collect();
    let path_text = canonical.to_string_lossy();
    let digest = ring::digest::digest(&ring::digest::SHA256, path_text.as_bytes());
    let mut hex = String::new();
    for byte in digest.as_ref().iter().take(8) {
        hex.push_str(&format!("{byte:02x}"));
    }
    format!("{name}-{hex}")
}

/// `count` fresh bytes from /dev/urandom.
fn random_bytes(count: usize) -> std::io::Result<Vec<u8>> {
    let mut bytes = vec![0_u8; count];
    File::open("/dev/urandom")?.read_exact(&mut bytes)?;
    Ok(bytes)
}

/// Digits only, so a parse of one fixed-width field never reads another field's text.
fn digits(text: &str) -> Option<u32> {
    if !text.bytes().all(|b| b.is_ascii_digit()) {
        return None;
    }
    text.parse().ok()
}

/// The days-to-civil conversion (Howard Hinnant's algorithm): day 0 is 1970-01-01.
fn civil_from_days(z: i64) -> (i64, u32, u32) {
    let z = z + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097);
    let yoe = (doe - doe / 1460 + doe / 36524 - doe / 146096) / 365;
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = (doy - (153 * mp + 2) / 5 + 1) as u32;
    let m = if mp < 10 { mp + 3 } else { mp - 9 } as u32;
    (y + i64::from(m <= 2), m, d)
}

/// The civil-to-days conversion, the inverse of civil_from_days.
fn days_from_civil(y: i64, m: u32, d: u32) -> i64 {
    let y = y - i64::from(m <= 2);
    let era = y.div_euclid(400);
    let yoe = y - era * 400;
    let mp = if m > 2 {
        i64::from(m) - 3
    } else {
        i64::from(m) + 9
    };
    let doy = (153 * mp + 2) / 5 + i64::from(d) - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    era * 146_097 + doe - 719_468
}
