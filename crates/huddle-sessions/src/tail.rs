//! Line-by-line reads of a log's start and of the bytes past a cursor. A line is decoded as it
//! is read: no read holds more than one line of the log.

use std::fs::File;
use std::io::{BufRead, BufReader, Read, Seek, SeekFrom};
use std::path::Path;

use crate::log::Summary;

const HEAD_BYTES: u64 = 64 * 1024;

/// What one read of a log saw.
pub(crate) struct Fed {
    /// Every byte read, counting a partial last line.
    pub bytes: u64,
    /// The end of the last whole line.
    pub end: u64,
}

/// Feeds the whole lines from `offset` on into `summary`. While `cut` is set, the first line
/// started before `offset` and is skipped, which clears `cut`. A partial last line stays unread.
pub(crate) fn feed_lines(
    path: &Path,
    offset: u64,
    cut: &mut bool,
    summary: &mut Summary,
) -> Option<Fed> {
    let mut file = File::open(path).ok()?;
    file.seek(SeekFrom::Start(offset)).ok()?;
    let mut fed = Fed {
        bytes: 0,
        end: offset,
    };
    each_line(BufReader::new(file), &mut fed, |line| {
        if std::mem::take(cut) {
            return true;
        }
        summary.feed(&String::from_utf8_lossy(line));
        true
    });
    Some(fed)
}

/// The first timestamp among the whole lines of the log's first 64 KiB, and the bytes read to
/// find it.
pub(crate) fn first_ms(path: &Path) -> (Option<u64>, u64) {
    let Ok(file) = File::open(path) else {
        return (None, 0);
    };
    let mut fed = Fed { bytes: 0, end: 0 };
    let mut head = Summary::default();
    each_line(BufReader::new(file.take(HEAD_BYTES)), &mut fed, |line| {
        head.feed(&String::from_utf8_lossy(line));
        head.first_ms.is_none()
    });
    (head.first_ms, fed.bytes)
}

/// Calls `line` with each whole line, newline included, while it returns true.
fn each_line(mut reader: impl BufRead, fed: &mut Fed, mut line: impl FnMut(&[u8]) -> bool) {
    let mut bytes = Vec::new();
    loop {
        bytes.clear();
        let Ok(n) = reader.read_until(b'\n', &mut bytes) else {
            return;
        };
        fed.bytes += n as u64;
        if n == 0 || bytes.last() != Some(&b'\n') {
            return;
        }
        fed.end += n as u64;
        if !line(&bytes) {
            return;
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;

    const LINE: &str = r#"{"type":"user","timestamp":"2026-11-11T11:00:00.000Z"}"#;

    #[test]
    fn a_cut_line_is_skipped_and_a_partial_line_stays_unread() {
        let dir = huddle_testkit::temp_home::TempHome::new();
        let path = dir.root().join("log.jsonl");
        let mut file = File::create(&path).expect("create");
        write!(file, "ut line\n{LINE}\n{{\"type\":").expect("write");
        let mut summary = Summary::default();
        let mut cut = true;
        let fed = feed_lines(&path, 0, &mut cut, &mut summary).expect("read");
        assert!(!cut);
        assert_eq!(fed.end, 8 + LINE.len() as u64 + 1);
        assert_eq!(fed.bytes, fed.end + 8);
        assert!(summary.first_ms.is_some());
    }

    #[test]
    fn the_head_stops_at_its_first_timestamp() {
        let dir = huddle_testkit::temp_home::TempHome::new();
        let path = dir.root().join("log.jsonl");
        let mut file = File::create(&path).expect("create");
        write!(file, "{{}}\n{LINE}\n{LINE}\n").expect("write");
        let (at, bytes) = first_ms(&path);
        assert!(at.is_some());
        assert_eq!(bytes, 3 + LINE.len() as u64 + 1);
    }
}
