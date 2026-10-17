package outside

// Reading a secret from the terminal: the echo is turned off while the line is read, so the
// secret never lands on the screen (or in a terminal capture). The echo is toggled through
// stty, which works on the terminal its own stdin is; a reader that is not this process's
// terminal — a test's string reader, a pipe — is read plainly, because it echoes nothing.

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"strings"
)

// ReadSecret reads one line from r and answers it without its line break. When r is the
// process's terminal, the terminal's echo is off for the read and back on after it.
func ReadSecret(r io.Reader) (string, error) {
	if file, ok := r.(*os.File); ok && isTerminal(file) {
		if err := sttyEcho(file, false); err == nil {
			defer func() { _ = sttyEcho(file, true) }()
		}
	}
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// isTerminal answers whether the file is a character device — a terminal, never a pipe or a
// file.
func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// sttyEcho turns the terminal's echo off and on through stty, which acts on the terminal its own
// stdin is: the child inherits ours. A failure answers as an error, and the read goes on with
// the echo as it was — a machine without stty still gets its secret, shown.
func sttyEcho(tty *os.File, on bool) error {
	arg := "echo"
	if !on {
		arg = "-echo"
	}
	cmd := exec.Command("stty", arg)
	cmd.Stdin = tty
	return cmd.Run()
}
