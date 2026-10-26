package convo

// The background shells of one session: the Bash calls its transcript runs in the background,
// the notification that ends each one, and the output file Claude Code keeps for it under the
// session's own tasks folder. The output file's path stays in this package; the routes hand out
// only its pages.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"unicode/utf8"
)

// MaxShellRead is the most bytes one output page takes.
const MaxShellRead = 64 << 10

// Shell is one background command a transcript ran, as its lines and its notification describe
// it. State is running until a notification ends it; Status and ExitCode come from that
// notification.
type Shell struct {
	ID          string `json:"id"`
	Command     string `json:"command"`
	Description string `json:"description,omitempty"`
	State       string `json:"state"`
	Status      string `json:"status,omitempty"`
	ExitCode    *int   `json:"exit_code,omitempty"`
	StartedAt   string `json:"started_at"`
	EndedAt     string `json:"ended_at,omitempty"`
	output      string
}

// OutputPage is one page of a shell's output: the text, the byte offset the next page starts
// at, and the output file's whole size.
type OutputPage struct {
	Text string `json:"text"`
	Next int64  `json:"next"`
	Size int64  `json:"size"`
}

// shellLine is one transcript line a shell scan may need. claude.go's transcriptLine carries no
// session id and no attachment, and the output file's checks need the session id.
type shellLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	SessionID string `json:"sessionId"`
	Message   struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	Attachment struct {
		Type   string `json:"type"`
		Prompt string `json:"prompt"`
	} `json:"attachment"`
}

// shellUseInput is the input of a Bash tool call.
type shellUseInput struct {
	Command     string `json:"command"`
	Description string `json:"description"`
	Background  bool   `json:"run_in_background"`
}

// A line without one of these marks holds no shell news, so the scan never decodes it.
const (
	shellMarkUse  = "run_in_background"
	shellMarkID   = "Command running in background"
	shellMarkDone = "task-notification"
)

// shellResultPattern takes the id and the output file's path from a result's text. The id is
// Claude Code's own: lowercase letters and digits.
var shellResultPattern = regexp.MustCompile(`Command running in background with ID: ([a-z0-9]{1,32})\. Output is being written to: (.+?)\. You will be notified`)

var (
	shellNotificationUse    = regexp.MustCompile(`<tool-use-id>([^<]+)</tool-use-id>`)
	shellNotificationStatus = regexp.MustCompile(`<status>([^<]+)</status>`)
	shellExitCodePattern    = regexp.MustCompile(`\(exit code (\d+)\)`)
)

// Shells answers the background commands the transcript at path ran, in transcript order: each
// starts at its Bash call, takes its id and output file from the answer, and stays running
// until a notification names its call. Only complete lines count, and a line without a shell's
// marks is never decoded.
func Shells(ctx context.Context, path string) ([]Shell, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	shells := []Shell{}
	at := map[string]int{}
	reader := bufio.NewReader(file)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := reader.ReadBytes('\n')
		if len(raw) > 0 && raw[len(raw)-1] == '\n' {
			scanShellLine(raw, &shells, at)
			if err == nil {
				continue
			}
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		break
	}
	return shells, nil
}

// scanShellLine takes one complete transcript line into the shells: a background Bash call adds
// one, its answer gives it the id and the output file, a notification ends it.
func scanShellLine(raw []byte, shells *[]Shell, at map[string]int) {
	if !bytes.Contains(raw, []byte(shellMarkUse)) &&
		!bytes.Contains(raw, []byte(shellMarkID)) &&
		!bytes.Contains(raw, []byte(shellMarkDone)) {
		return
	}
	var line shellLine
	if json.Unmarshal(raw, &line) != nil {
		return
	}
	switch line.Type {
	case "assistant":
		var blocks []contentBlock
		if json.Unmarshal(line.Message.Content, &blocks) != nil {
			return
		}
		for _, block := range blocks {
			if block.Type != "tool_use" || block.Name != "Bash" {
				continue
			}
			var input shellUseInput
			if json.Unmarshal(block.Input, &input) != nil || !input.Background {
				continue
			}
			*shells = append(*shells, Shell{
				Command:     input.Command,
				Description: input.Description,
				State:       "running",
				StartedAt:   line.Timestamp,
			})
			at[block.ID] = len(*shells) - 1
		}
	case "user":
		var blocks []contentBlock
		if json.Unmarshal(line.Message.Content, &blocks) != nil {
			return
		}
		for _, block := range blocks {
			if block.Type != "tool_result" {
				continue
			}
			i, ok := at[block.ToolUseID]
			if !ok {
				continue
			}
			if id, output, ok := shellOutputOf(resultText(block.Content), line.SessionID); ok {
				(*shells)[i].ID = id
				(*shells)[i].output = output
			}
		}
	case "attachment":
		if line.Attachment.Type != "queued_command" {
			return
		}
		prompt := line.Attachment.Prompt
		if !bytes.Contains([]byte(prompt), []byte(shellMarkDone)) {
			return
		}
		use := shellNotificationUse.FindStringSubmatch(prompt)
		if use == nil {
			return
		}
		i, ok := at[use[1]]
		if !ok {
			return
		}
		(*shells)[i].State = "ended"
		(*shells)[i].EndedAt = line.Timestamp
		if status := shellNotificationStatus.FindStringSubmatch(prompt); status != nil {
			(*shells)[i].Status = status[1]
		}
		if code := shellExitCodePattern.FindStringSubmatch(prompt); code != nil {
			(*shells)[i].ExitCode = shellCodeOf(code[1])
		}
	}
}

// shellOutputOf takes the shell id and the output file's path from a background command's
// result text. The path counts only when it is a clean absolute path sitting in the session's
// own tasks folder as <id>.output; anything else lists the shell with no readable output.
func shellOutputOf(text, session string) (string, string, bool) {
	m := shellResultPattern.FindStringSubmatch(text)
	if m == nil {
		return "", "", false
	}
	id, path := m[1], m[2]
	if !filepath.IsAbs(path) ||
		filepath.Clean(path) != path ||
		filepath.Base(path) != id+".output" ||
		filepath.Base(filepath.Dir(path)) != "tasks" ||
		filepath.Base(filepath.Dir(filepath.Dir(path))) != session {
		return id, "", true
	}
	return id, path, true
}

// shellCodeOf answers one exit code's digits as a number.
func shellCodeOf(digits string) *int {
	code, err := strconv.Atoi(digits)
	if err != nil {
		return nil
	}
	return &code
}

// ShellOutput answers one page of a shell's output from its own file, after bytes read so far,
// at most MaxShellRead and cut at a newline when the window holds one. ok is false when no
// shell carries the id. A shell with no output file — one its result never named under the
// session's own tasks folder, or one the disk lost — is an empty page at after.
func ShellOutput(ctx context.Context, shells []Shell, id string, after int64) (OutputPage, bool, error) {
	if err := ctx.Err(); err != nil {
		return OutputPage{}, false, err
	}
	if id == "" {
		return OutputPage{}, false, nil
	}
	var output string
	found := false
	for _, shell := range shells {
		if shell.ID == id {
			output, found = shell.output, true
			break
		}
	}
	if !found {
		return OutputPage{}, false, nil
	}
	if output == "" {
		return OutputPage{Next: after}, true, nil
	}
	file, err := os.Open(output)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return OutputPage{Next: after}, true, nil
		}
		return OutputPage{}, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return OutputPage{}, false, err
	}
	if after < 0 || after >= info.Size() {
		return OutputPage{Next: after, Size: info.Size()}, true, nil
	}
	if _, err := file.Seek(after, io.SeekStart); err != nil {
		return OutputPage{}, false, err
	}
	window := make([]byte, min(int64(MaxShellRead), info.Size()-after))
	read, err := io.ReadFull(file, window)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return OutputPage{}, false, err
	}
	text := cleanShellWindow(string(window[:read]))
	if cut := bytes.LastIndexByte([]byte(text), '\n'); cut >= 0 {
		text = text[:cut+1]
	}
	return OutputPage{Text: text, Next: after + int64(len(text)), Size: info.Size()}, true, nil
}

// cleanShellWindow backs a window off a rune its end split, so the page stays valid UTF-8; the
// rune's own bytes are whole on the next page.
func cleanShellWindow(window string) string {
	for len(window) > 0 {
		if r, size := utf8.DecodeLastRuneInString(window); r != utf8.RuneError || size != 1 {
			break
		}
		window = window[:len(window)-1]
	}
	return window
}
