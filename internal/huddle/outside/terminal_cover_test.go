package outside

// Reading a secret from something that is not the process's terminal: a string reader, a pipe
// and a plain file are read as they are, and the echo toggle's own failure is only an error.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSecretReadsOnePlainLineWithoutItsBreak(t *testing.T) {
	secret, err := ReadSecret(strings.NewReader("tok-123\nmore"))
	if err != nil {
		t.Fatalf("ReadSecret: %v", err)
	}
	if secret != "tok-123" {
		t.Errorf("ReadSecret = %q, want the first line only", secret)
	}
	// a line that never ends still reads to the end of what is there
	secret, err = ReadSecret(strings.NewReader("no-break"))
	if err != nil {
		t.Fatalf("ReadSecret without a break: %v", err)
	}
	if secret != "no-break" {
		t.Errorf("ReadSecret = %q, want the whole line", secret)
	}
	// \r\n ends a line the same way
	secret, err = ReadSecret(strings.NewReader("win-line\r\n"))
	if err != nil || secret != "win-line" {
		t.Errorf("ReadSecret = (%q, %v), want the line without its carriage return", secret, err)
	}
}

func TestReadSecretOfAnEmptyReaderAnswersItsError(t *testing.T) {
	secret, err := ReadSecret(strings.NewReader(""))
	if err == nil {
		t.Error("ReadSecret of nothing answered nil, want the reader's error")
	}
	if secret != "" {
		t.Errorf("ReadSecret = %q, want nothing", secret)
	}
}

func TestAFileOrPipeIsNeverATerminal(t *testing.T) {
	plain := filepath.Join(t.TempDir(), "plain.txt")
	if err := os.WriteFile(plain, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(plain)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if isTerminal(file) {
		t.Error("a regular file reads as a terminal")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	if isTerminal(reader) {
		t.Error("a pipe reads as a terminal")
	}
}

func TestSttyWithoutATerminalAnswersItsError(t *testing.T) {
	_, writer, err := os.Pipe() // stty acts on the terminal its stdin is; a pipe is none
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	if err := sttyEcho(writer, false); err == nil {
		t.Error("stty on a pipe answered nil, want an error")
	} else if errors.Is(err, os.ErrNotExist) {
		t.Skip("no stty on this machine")
	}
}
