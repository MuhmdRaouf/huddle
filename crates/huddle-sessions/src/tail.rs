//! Bounded reads of a log's start and end.

use std::fs::File;
use std::io::{Read, Seek, SeekFrom};
use std::path::Path;

const HEAD_BYTES: u64 = 64 * 1024;

/// The last `cap` bytes as text, starting at a line boundary.
pub(crate) fn read_tail(path: &Path, cap: u64) -> Option<String> {
    let mut file = File::open(path).ok()?;
    let len = file.metadata().ok()?.len();
    let start = len.saturating_sub(cap);
    file.seek(SeekFrom::Start(start)).ok()?;
    let mut bytes = Vec::new();
    file.take(cap).read_to_end(&mut bytes).ok()?;
    let text = String::from_utf8_lossy(&bytes);
    if start == 0 {
        return Some(text.into_owned());
    }
    // The first line is cut mid-way: drop it.
    Some(
        text.split_once('\n')
            .map(|(_, rest)| rest.to_string())
            .unwrap_or_default(),
    )
}

/// The first bytes as text, ending at a line boundary.
pub(crate) fn read_head(path: &Path) -> Option<String> {
    let mut file = File::open(path).ok()?;
    let len = file.metadata().ok()?.len();
    let mut bytes = Vec::new();
    (&mut file).take(HEAD_BYTES).read_to_end(&mut bytes).ok()?;
    let text = String::from_utf8_lossy(&bytes).into_owned();
    if len <= HEAD_BYTES {
        return Some(text);
    }
    text.rsplit_once('\n').map(|(whole, _)| whole.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;

    #[test]
    fn tail_drops_the_cut_line_and_head_keeps_whole_lines() {
        let dir = huddle_testkit::temp_home::TempHome::new();
        let path = dir.root().join("log.jsonl");
        let mut file = File::create(&path).expect("create");
        file.write_all(b"first line\nsecond line\nthird line\n")
            .expect("write");
        assert_eq!(read_tail(&path, 14).as_deref(), Some("third line\n"));
        assert_eq!(
            read_tail(&path, 1_000).as_deref(),
            Some("first line\nsecond line\nthird line\n")
        );
        assert_eq!(
            read_head(&path).as_deref(),
            Some("first line\nsecond line\nthird line\n")
        );
    }
}
