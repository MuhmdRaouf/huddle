//! huddle-router: body.
//!
//! One byte-scanner for provider-bound request bodies: a body's top-level members as spans of the
//! body's own bytes, and the strip deletions a provider's rejected names need. No parsed tree; a
//! body the scanner refuses is the caller's signal to keep the bytes as they are.

use std::borrow::Cow;

/// How deep the scan follows nesting. Past it the bytes are refused and the caller keeps the
/// parsed path.
const MAX_SCAN_DEPTH: usize = 1024;

/// Why a scan stopped: `Truncated` means the bytes ended inside a token, `Bad` means the bytes
/// are not JSON.
enum Stop {
    Truncated,
    Bad,
}

/// The word-at-a-time string skip's byte lanes: the quote and the backslash a string's inside can
/// hold, the space every raw control byte ends below, and the borrow and high-bit masks of the
/// byte-equality test.
const QUOTE_EVERY: u64 = 0x2222222222222222; // '"'
const SLASH_EVERY: u64 = 0x5c5c5c5c5c5c5c5c; // '\\'
const SPACE_EVERY: u64 = 0x2020202020202020; // ' '
const ONES_EVERY: u64 = 0x0101010101010101;
const HI_BITS_EVERY: u64 = 0x8080808080808080;

/// One top-level member: its key as the body's text decodes it, the index of the key's opening
/// quote, and the half-open span of the value's bytes.
pub struct Member<'b> {
    pub key: Cow<'b, str>,
    pub member_start: usize,
    pub value_start: usize,
    pub value_end: usize,
}

/// A whole object's members and the index of its closing brace.
pub struct Scan<'b> {
    pub members: Vec<Member<'b>>,
    /// The index of the object's closing brace.
    pub close: usize,
}

/// Every top-level member of one whole JSON object, as spans of the body's own bytes. None for
/// anything else: no object, bytes after the closing brace, a duplicate key, or JSON the grammar
/// refuses.
pub fn scan(body: &[u8]) -> Option<Scan<'_>> {
    scan_with_strip(body, &[]).map(|scanned| scanned.scan)
}

/// A scan with the strip deletions a provider's rejected names need, from one walk over the body.
pub struct Scanned<'b> {
    pub scan: Scan<'b>,
    /// Non-overlapping deletions for every stripped member, already comma-correct.
    pub strip_edits: Vec<Edit>,
}

/// Every top-level member of one whole JSON object, plus one deletion per run of members whose
/// key is in `strip`, at the positions the Anthropic Messages API defines: the top level, `system`
/// and `tools` entries, each message, message content blocks, and the content blocks of a
/// `tool_result`. Inside `input_schema`, a `tool_use`'s `input`, or any other value, a stripped
/// name stays. One pass over the body. None for anything else: no object, bytes after the closing
/// brace, a duplicate key, or JSON the grammar refuses.
pub fn scan_with_strip<'b>(body: &'b [u8], strip: &[&str]) -> Option<Scanned<'b>> {
    let mut at = skip_space(body, 0);
    if at >= body.len() || body[at] != b'{' {
        return None;
    }
    at = skip_space(body, at + 1);
    let mut members: Vec<Member<'_>> = Vec::new();
    let mut spans: Vec<(usize, usize, bool)> = Vec::new();
    let mut edits: Vec<Edit> = Vec::new();
    if at < body.len() && body[at] == b'}' {
        let scan = whole_object(Scan { members, close: at }, body)?;
        return Some(Scanned {
            scan,
            strip_edits: edits,
        });
    }
    loop {
        at = skip_space(body, at);
        if at >= body.len() || body[at] != b'"' {
            return None;
        }
        let member_start = at;
        let (raw, next) = scan_string(body, at).ok()?;
        let key = member_key(raw)?;
        at = skip_space(body, next);
        if at >= body.len() || body[at] != b':' {
            return None;
        }
        at = skip_space(body, at + 1);
        let value_start = at;
        let stripped = strip.iter().any(|name| key == *name);
        let value_end = if stripped {
            // The member goes away whole: its value is measured, never read.
            scan_value(body, at, 1).ok()?
        } else {
            match &*key {
                "system" | "tools" if !strip.is_empty() => {
                    walk_strip_array(body, at, 1, strip, &Drill::Leaf, &mut edits).ok()?
                }
                "messages" if !strip.is_empty() => {
                    walk_strip_array(body, at, 1, strip, &Drill::Message, &mut edits).ok()?
                }
                _ => scan_value(body, at, 1).ok()?,
            }
        };
        if members.iter().any(|seen| seen.key == key) {
            return None;
        }
        members.push(Member {
            key,
            member_start,
            value_start,
            value_end,
        });
        spans.push((member_start, value_end, stripped));
        at = skip_space(body, value_end);
        match body.get(at) {
            Some(b',') => at += 1,
            Some(b'}') => {
                strip_runs(&spans, &mut edits);
                let scan = whole_object(Scan { members, close: at }, body)?;
                return Some(Scanned {
                    scan,
                    strip_edits: edits,
                });
            }
            _ => return None,
        }
    }
}

/// How deep a drilled object still holds strip positions: a message's `content` blocks hold one
/// more level (a `tool_result`'s own content blocks), and those hold none.
enum Drill {
    /// No member value is drilled into.
    Leaf,
    /// An array `content` member is drilled into leaf blocks.
    Block,
    /// An array `content` member is drilled into blocks that may drill once more.
    Message,
}

/// An array whose object elements carry strip positions: `system` or `tools` entries, a message's
/// `content` blocks. Elements that are no objects are skipped whole.
fn walk_strip_array(
    body: &[u8],
    at: usize,
    depth: usize,
    strip: &[&str],
    drill: &Drill,
    edits: &mut Vec<Edit>,
) -> Result<usize, Stop> {
    if body.get(at) != Some(&b'[') {
        // A `content` member may be one string: no member positions in it.
        return scan_value(body, at, depth);
    }
    if depth >= MAX_SCAN_DEPTH {
        return Err(Stop::Bad);
    }
    let mut at = skip_space(body, at + 1);
    if at < body.len() && body[at] == b']' {
        return Ok(at + 1);
    }
    loop {
        at = skip_space(body, at);
        if at >= body.len() {
            return Err(Stop::Truncated);
        }
        let next = if body[at] == b'{' {
            walk_strip_object(body, at, depth + 1, strip, drill, edits)?
        } else {
            scan_value(body, at, depth + 1)?
        };
        at = skip_space(body, next);
        if at >= body.len() {
            return Err(Stop::Truncated);
        }
        match body[at] {
            b',' => at += 1,
            b']' => return Ok(at + 1),
            _ => return Err(Stop::Bad),
        }
    }
}

/// One object whose members carry strip positions: a `system` or `tools` entry, a message, or a
/// content block. A member whose key is in `strip` is deleted whole, its value skipped; `drill`
/// says whether an array `content` member is itself a strip position.
fn walk_strip_object(
    body: &[u8],
    at: usize,
    depth: usize,
    strip: &[&str],
    drill: &Drill,
    edits: &mut Vec<Edit>,
) -> Result<usize, Stop> {
    if depth >= MAX_SCAN_DEPTH {
        return Err(Stop::Bad);
    }
    let mut members: Vec<(usize, usize, bool)> = Vec::new();
    let mut at = skip_space(body, at + 1);
    if at < body.len() && body[at] == b'}' {
        return Ok(at + 1);
    }
    loop {
        at = skip_space(body, at);
        if at >= body.len() || body[at] != b'"' {
            return Err(Stop::Bad);
        }
        let member_start = at;
        let (raw, next) = scan_string(body, at)?;
        let key = member_key(raw).ok_or(Stop::Bad)?;
        at = skip_space(body, next);
        if at >= body.len() || body[at] != b':' {
            return Err(Stop::Bad);
        }
        at = skip_space(body, at + 1);
        let stripped = strip.iter().any(|name| key == *name);
        let value_end = if stripped {
            scan_value(body, at, depth + 1)?
        } else {
            match (&*key, drill) {
                ("content", Drill::Message) => {
                    walk_strip_array(body, at, depth + 1, strip, &Drill::Block, edits)?
                }
                ("content", Drill::Block) => {
                    walk_strip_array(body, at, depth + 1, strip, &Drill::Leaf, edits)?
                }
                _ => scan_value(body, at, depth + 1)?,
            }
        };
        members.push((member_start, value_end, stripped));
        at = skip_space(body, value_end);
        match body.get(at) {
            Some(b',') => at += 1,
            Some(b'}') => {
                strip_runs(&members, edits);
                return Ok(at + 1);
            }
            _ => return Err(Stop::Bad),
        }
    }
}

/// Deletion runs for one object's members: consecutive stripped members form one run, so the
/// commas around the deletions stay valid.
fn strip_runs(members: &[(usize, usize, bool)], edits: &mut Vec<Edit>) {
    let mut at = 0;
    while at < members.len() {
        if !members[at].2 {
            at += 1;
            continue;
        }
        let run_start = at;
        while at < members.len() && members[at].2 {
            at += 1;
        }
        if at < members.len() {
            // A kept member follows: the run and its trailing comma go.
            edits.push(Edit {
                start: members[run_start].0,
                end: members[at].0,
                with: Vec::new(),
            });
        } else if run_start > 0 {
            // The run reaches the object's end: the kept member before it keeps its comma.
            edits.push(Edit {
                start: members[run_start - 1].1,
                end: members[members.len() - 1].1,
                with: Vec::new(),
            });
        } else {
            // Every member is gone: the object stays as an empty one, whitespace and all.
            edits.push(Edit {
                start: members[0].0,
                end: members[members.len() - 1].1,
                with: Vec::new(),
            });
        }
    }
}

/// A scan is whole only when the body ends at the object's closing brace.
fn whole_object<'a>(scanned: Scan<'a>, body: &'a [u8]) -> Option<Scan<'a>> {
    if skip_space(body, scanned.close + 1) == body.len() {
        Some(scanned)
    } else {
        None
    }
}

/// A member's key: the raw bytes between the quotes when they carry no escape, else the text the
/// quoted string decodes to.
fn member_key<'b>(raw: &'b [u8]) -> Option<Cow<'b, str>> {
    if raw.contains(&b'\\') {
        serde_json::from_slice::<String>(raw).ok().map(Cow::Owned)
    } else {
        std::str::from_utf8(&raw[1..raw.len() - 1])
            .ok()
            .map(Cow::Borrowed)
    }
}

/// The model string of a whole body, "" for anything else. Bytes after the model are never read.
pub fn model_of(body: &[u8]) -> String {
    model_head(body).0
}

/// `model_of` for the first bytes of a body still arriving. The bool is true when these bytes
/// settle the answer: the model, or its absence, or a shape that can never have one.
pub fn model_head(head: &[u8]) -> (String, bool) {
    let mut at = skip_space(head, 0);
    if at >= head.len() {
        return (String::new(), false);
    }
    if head[at] != b'{' {
        return (String::new(), true);
    }
    at = skip_space(head, at + 1);
    loop {
        at = skip_space(head, at);
        if at >= head.len() {
            return (String::new(), false);
        }
        match head[at] {
            b'"' => {}
            _ => return (String::new(), true),
        }
        let (raw, next) = match scan_string(head, at) {
            Ok(whole) => whole,
            Err(Stop::Truncated) => return (String::new(), false),
            Err(Stop::Bad) => return (String::new(), true),
        };
        let is_model = member_key(raw).is_some_and(|key| key == "model");
        at = skip_space(head, next);
        if at >= head.len() {
            return (String::new(), false);
        }
        if head[at] != b':' {
            return (String::new(), true);
        }
        at = skip_space(head, at + 1);
        let value_start = at;
        let end = match scan_value(head, at, 1) {
            Ok(end) => end,
            Err(Stop::Truncated) => return (String::new(), false),
            Err(Stop::Bad) => return (String::new(), true),
        };
        if is_model {
            let text =
                serde_json::from_slice::<String>(&head[value_start..end]).unwrap_or_default();
            return (text, true);
        }
        at = skip_space(head, end);
        match head.get(at) {
            Some(b',') => at += 1,
            Some(b'}') => return (String::new(), true),
            None => return (String::new(), false),
            Some(_) => return (String::new(), true),
        }
    }
}

/// One edit on the body's own bytes: the half-open span `[start, end)` replaced by `with`.
pub struct Edit {
    pub start: usize,
    pub end: usize,
    pub with: Vec<u8>,
}

/// One pass: copies the gaps and the replacements into one buffer of the exact final length.
/// The edits land sorted by `start`; their ranges never overlap.
#[allow(clippy::ptr_arg)] // the signature sorts the caller's Vec in place, as the body contract reads
pub fn apply(body: &[u8], edits: &mut Vec<Edit>) -> Vec<u8> {
    edits.sort_by_key(|edit| edit.start);
    let mut final_len = body.len();
    for edit in edits.iter() {
        final_len = final_len - (edit.end - edit.start) + edit.with.len();
    }
    let mut out = Vec::with_capacity(final_len);
    let mut prev = 0;
    for edit in edits.iter() {
        out.extend_from_slice(&body[prev..edit.start]);
        out.extend_from_slice(&edit.with);
        prev = edit.end;
    }
    out.extend_from_slice(&body[prev..]);
    out
}

/// The index of the first byte at or after `at` that is no JSON whitespace.
fn skip_space(body: &[u8], at: usize) -> usize {
    let mut at = at;
    while at < body.len() {
        match body[at] {
            b' ' | b'\t' | b'\n' | b'\r' => at += 1,
            _ => return at,
        }
    }
    at
}

/// One JSON value opening at `at`, `depth` brackets in; the index of the first byte past it.
fn scan_value(body: &[u8], at: usize, depth: usize) -> Result<usize, Stop> {
    let Some(&c) = body.get(at) else {
        return Err(Stop::Truncated);
    };
    match c {
        b'"' => Ok(scan_string(body, at)?.1),
        b'{' => scan_compound(body, at, depth, b'{', b'}'),
        b'[' => scan_compound(body, at, depth, b'[', b']'),
        b't' => literal_end(body, at, "true"),
        b'f' => literal_end(body, at, "false"),
        b'n' => literal_end(body, at, "null"),
        b'-' | b'0'..=b'9' => scan_number(body, at),
        _ => Err(Stop::Bad),
    }
}

/// Past a literal's last byte when the bytes at `at` are exactly it; `Truncated` when the bytes
/// stop inside it.
fn literal_end(body: &[u8], at: usize, literal: &str) -> Result<usize, Stop> {
    let lit = literal.as_bytes();
    if body[at..].starts_with(lit) {
        return Ok(at + lit.len());
    }
    if body.len() - at < lit.len() && lit.starts_with(&body[at..]) {
        return Err(Stop::Truncated);
    }
    Err(Stop::Bad)
}

/// One whole object or array opening at `at`: every member a string key, a colon and a value,
/// every element a value, commas between them, strings and nesting aware.
fn scan_compound(body: &[u8], at: usize, depth: usize, open: u8, shut: u8) -> Result<usize, Stop> {
    if depth >= MAX_SCAN_DEPTH {
        return Err(Stop::Bad);
    }
    let mut at = skip_space(body, at + 1);
    if at < body.len() && body[at] == shut {
        return Ok(at + 1);
    }
    loop {
        at = skip_space(body, at);
        if at >= body.len() {
            return Err(Stop::Truncated);
        }
        if open == b'{' {
            if body[at] != b'"' {
                return Err(Stop::Bad);
            }
            let (_, next) = scan_string(body, at)?;
            at = skip_space(body, next);
            if at >= body.len() {
                return Err(Stop::Truncated);
            }
            if body[at] != b':' {
                return Err(Stop::Bad);
            }
            at = skip_space(body, at + 1);
        }
        let next = scan_value(body, at, depth + 1)?;
        at = skip_space(body, next);
        if at >= body.len() {
            return Err(Stop::Truncated);
        }
        match body[at] {
            b',' => at += 1,
            c if c == shut => return Ok(at + 1),
            _ => return Err(Stop::Bad),
        }
    }
}

/// The JSON string opening at `at`, as its raw bytes with quotes and escapes as they came, and
/// the index past its closing quote. Every escape form is a string's inside: `\" \\ \/ \b \f \n
/// \r \t` and `\u` with four hex digits. A raw control byte, a bad escape or an unterminated
/// quote is no string.
fn scan_string(body: &[u8], at: usize) -> Result<(&[u8], usize), Stop> {
    let start = at;
    let mut at = at + 1;
    while at < body.len() {
        // Plain string bytes skip eight at a time: the word test trips on a word holding a
        // quote, a backslash or a byte below the space, and on nothing else, so a quiet word is
        // eight safe bytes at once. A tripped word falls to the byte loop below, which decides
        // every byte exactly.
        while at + 8 <= body.len() {
            let Ok(chunk) = <[u8; 8]>::try_from(&body[at..at + 8]) else {
                break;
            };
            let word = u64::from_le_bytes(chunk);
            let quote = word ^ QUOTE_EVERY;
            let slash = word ^ SLASH_EVERY;
            let trip = ((quote.wrapping_sub(ONES_EVERY) & !quote)
                | (slash.wrapping_sub(ONES_EVERY) & !slash)
                | (word.wrapping_sub(SPACE_EVERY) & !word))
                & HI_BITS_EVERY;
            if trip != 0 {
                break;
            }
            at += 8;
        }
        if at >= body.len() {
            break;
        }
        match body[at] {
            b'"' => return Ok((&body[start..at + 1], at + 1)),
            b'\\' => {
                if at + 1 >= body.len() {
                    return Err(Stop::Truncated);
                }
                match body[at + 1] {
                    b'"' | b'\\' | b'/' | b'b' | b'f' | b'n' | b'r' | b't' => at += 2,
                    b'u' => {
                        if at + 6 > body.len() {
                            return Err(Stop::Truncated);
                        }
                        if !is_hex(&body[at + 2..at + 6]) {
                            return Err(Stop::Bad);
                        }
                        at += 6;
                    }
                    _ => return Err(Stop::Bad),
                }
            }
            c if c < 0x20 => return Err(Stop::Bad),
            _ => at += 1,
        }
    }
    Err(Stop::Truncated)
}

/// Whether the bytes are exactly four hex digits, as a `\u` escape's tail must be.
fn is_hex(digits: &[u8]) -> bool {
    digits.len() == 4 && digits.iter().all(u8::is_ascii_hexdigit)
}

/// One JSON number opening at `at`: no leading zero, a fraction and an exponent each carrying at
/// least one digit.
fn scan_number(body: &[u8], at: usize) -> Result<usize, Stop> {
    let mut at = at;
    if at < body.len() && body[at] == b'-' {
        at += 1;
    }
    let int_start = at;
    while at < body.len() && body[at].is_ascii_digit() {
        at += 1;
    }
    let digits = at - int_start;
    if digits == 0 {
        return if at >= body.len() {
            Err(Stop::Truncated)
        } else {
            Err(Stop::Bad)
        };
    }
    if digits > 1 && body[at - digits] == b'0' {
        return Err(Stop::Bad);
    }
    if at < body.len() && body[at] == b'.' {
        at += 1;
        let fraction_start = at;
        while at < body.len() && body[at].is_ascii_digit() {
            at += 1;
        }
        if at == fraction_start {
            return if at >= body.len() {
                Err(Stop::Truncated)
            } else {
                Err(Stop::Bad)
            };
        }
    }
    if at < body.len() && (body[at] == b'e' || body[at] == b'E') {
        at += 1;
        if at < body.len() && (body[at] == b'+' || body[at] == b'-') {
            at += 1;
        }
        let exponent_start = at;
        while at < body.len() && body[at].is_ascii_digit() {
            at += 1;
        }
        if at == exponent_start {
            return if at >= body.len() {
                Err(Stop::Truncated)
            } else {
                Err(Stop::Bad)
            };
        }
    }
    Ok(at)
}
