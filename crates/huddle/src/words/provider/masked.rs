//! One line read from a terminal with the kernel's echo and line editing off. The editor is a
//! small state machine over bytes; the terminal mode is held by a guard that puts it back.

use std::io::Write;

use huddle_keystore::port::SecretString;

/// What the typed characters look like on screen.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Echo {
    /// One `*` per character.
    Stars,
    /// The characters themselves.
    Text,
}

/// How a read ended.
pub enum Entry {
    /// Enter ended the line; the line may be empty.
    Line(SecretString),
    /// Ctrl-C, Ctrl-D on an empty line, or the end of input.
    Aborted,
}

/// What one byte did to the line.
#[derive(Debug, PartialEq, Eq)]
enum Step {
    /// Nothing to show.
    Quiet,
    /// Bytes to print.
    Show(Vec<u8>),
    Done,
    Abort,
}

/// Where the editor is inside an escape sequence (arrow keys, bracketed paste markers), which
/// it drops whole.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Escape {
    None,
    Start,
    Sequence,
}

/// The line typed so far, as UTF-8 bytes.
struct Editor {
    bytes: Vec<u8>,
    echo: Echo,
    escape: Escape,
}

impl Editor {
    fn new(echo: Echo) -> Editor {
        Editor {
            bytes: Vec::new(),
            echo,
            escape: Escape::None,
        }
    }

    fn feed(&mut self, byte: u8) -> Step {
        match self.escape {
            Escape::Start => {
                self.escape = if byte == b'[' {
                    Escape::Sequence
                } else {
                    Escape::None
                };
                return Step::Quiet;
            }
            Escape::Sequence => {
                if (0x40..=0x7e).contains(&byte) {
                    self.escape = Escape::None;
                }
                return Step::Quiet;
            }
            Escape::None => {}
        }
        match byte {
            0x1b => {
                self.escape = Escape::Start;
                Step::Quiet
            }
            b'\n' | b'\r' => Step::Done,
            0x03 => Step::Abort,
            0x04 if self.bytes.is_empty() => Step::Abort,
            0x7f | 0x08 => self.erase(),
            byte if byte < 0x20 => Step::Quiet,
            byte => {
                self.bytes.push(byte);
                match (self.echo, is_continuation(byte)) {
                    (Echo::Stars, true) => Step::Quiet,
                    (Echo::Stars, false) => Step::Show(vec![b'*']),
                    (Echo::Text, _) => Step::Show(vec![byte]),
                }
            }
        }
    }

    /// Drops the last character, however many bytes it took.
    fn erase(&mut self) -> Step {
        let Some(last) = self.bytes.pop() else {
            return Step::Quiet;
        };
        let mut lead = last;
        while is_continuation(lead) {
            match self.bytes.pop() {
                Some(byte) => lead = byte,
                None => break,
            }
        }
        Step::Show(b"\x08 \x08".to_vec())
    }

    fn into_secret(self) -> SecretString {
        SecretString::new(String::from_utf8_lossy(&self.bytes).into_owned())
    }
}

fn is_continuation(byte: u8) -> bool {
    byte & 0xc0 == 0x80
}

/// The terminal's mode on stdin, put back on drop. `panic=abort` skips drops on a panic, so
/// callers also end a read by dropping the guard before they return.
struct RawMode {
    saved: libc::termios,
}

impl RawMode {
    /// Echo, line editing and signal keys off; one byte per read. None when stdin has no
    /// terminal mode to change.
    fn enter() -> Option<RawMode> {
        let mut saved = unsafe { std::mem::zeroed::<libc::termios>() };
        if unsafe { libc::tcgetattr(0, &mut saved) } != 0 {
            return None;
        }
        let mut raw = saved;
        raw.c_lflag &= !((libc::ECHO | libc::ICANON | libc::ISIG) as libc::tcflag_t);
        raw.c_cc[libc::VMIN] = 1;
        raw.c_cc[libc::VTIME] = 0;
        if unsafe { libc::tcsetattr(0, libc::TCSANOW, &raw) } != 0 {
            return None;
        }
        Some(RawMode { saved })
    }
}

impl Drop for RawMode {
    fn drop(&mut self) {
        unsafe { libc::tcsetattr(0, libc::TCSANOW, &self.saved) };
    }
}

/// The next byte of stdin; None at the end of input or on a read error.
fn read_byte() -> Option<u8> {
    let mut byte = 0u8;
    loop {
        let count = unsafe { libc::read(0, (&mut byte as *mut u8).cast(), 1) };
        match count {
            1 => return Some(byte),
            -1 if std::io::Error::last_os_error().kind() == std::io::ErrorKind::Interrupted => {}
            _ => return None,
        }
    }
}

/// Prints `prompt`, reads one line from the terminal and ends the line on screen. The terminal
/// mode is back before this returns.
pub fn read_entry(prompt: &str, echo: Echo) -> Entry {
    let mut out = std::io::stdout();
    let _ = write!(out, "{prompt}");
    let _ = out.flush();
    let mode = RawMode::enter();
    let mut editor = Editor::new(echo);
    let entry = loop {
        let Some(byte) = read_byte() else {
            break Entry::Aborted;
        };
        match editor.feed(byte) {
            Step::Quiet => {}
            Step::Show(bytes) => {
                let _ = out.write_all(&bytes);
                let _ = out.flush();
            }
            Step::Done => break Entry::Line(editor.into_secret()),
            Step::Abort => break Entry::Aborted,
        }
    };
    drop(mode);
    let _ = writeln!(out);
    entry
}

#[cfg(test)]
mod tests {
    use super::*;

    fn type_all(editor: &mut Editor, bytes: &[u8]) -> Vec<Step> {
        bytes.iter().map(|byte| editor.feed(*byte)).collect()
    }

    #[test]
    fn a_character_shows_one_star_even_when_it_takes_two_bytes() {
        let mut editor = Editor::new(Echo::Stars);
        let steps = type_all(&mut editor, "aé".as_bytes());
        assert_eq!(
            steps,
            vec![Step::Show(vec![b'*']), Step::Show(vec![b'*']), Step::Quiet]
        );
    }

    #[test]
    fn backspace_removes_a_whole_character() {
        let mut editor = Editor::new(Echo::Stars);
        type_all(&mut editor, "aé".as_bytes());
        assert_eq!(editor.feed(0x7f), Step::Show(b"\x08 \x08".to_vec()));
        assert_eq!(editor.bytes, b"a");
        assert_eq!(editor.feed(0x08), Step::Show(b"\x08 \x08".to_vec()));
        assert_eq!(editor.feed(0x7f), Step::Quiet);
    }

    #[test]
    fn ctrl_d_aborts_only_an_empty_line() {
        let mut editor = Editor::new(Echo::Text);
        assert_eq!(editor.feed(0x04), Step::Abort);
        editor.feed(b'a');
        assert_eq!(editor.feed(0x04), Step::Quiet);
        assert_eq!(editor.feed(0x03), Step::Abort);
    }

    #[test]
    fn escape_sequences_are_dropped_whole() {
        let mut editor = Editor::new(Echo::Stars);
        type_all(&mut editor, b"\x1b[200~ab\x1b[201~");
        assert_eq!(editor.bytes, b"ab");
    }

    #[test]
    fn enter_ends_the_line_and_text_echo_shows_the_character() {
        let mut editor = Editor::new(Echo::Text);
        assert_eq!(editor.feed(b'y'), Step::Show(vec![b'y']));
        assert_eq!(editor.feed(b'\r'), Step::Done);
    }
}
