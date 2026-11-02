package convo

// The background shells of one session: the Bash calls its transcript runs in the background
// and the Monitor calls it starts, the notification that ends each one, and the output file
// Claude Code keeps for it under the session's own tasks folder. The output file's path stays in
// this package; the routes hand out only its pages.

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
	"unicode/utf8"
)

// MaxShellRead is the most bytes one output page takes.
const MaxShellRead = 64 << 10

// The kinds of background shell.
const (
	ShellKindBash    = "bash"
	ShellKindMonitor = "monitor"
)

// Shell is one background command or Monitor a transcript ran, as its lines and its
// notification describe it. State is running until a notification ends it; Status and ExitCode
// come from that notification. Events counts the event notifications a Monitor streamed.
type Shell struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Command     string `json:"command"`
	Description string `json:"description,omitempty"`
	State       string `json:"state"`
	Status      string `json:"status,omitempty"`
	ExitCode    *int   `json:"exit_code,omitempty"`
	Events      int    `json:"events,omitempty"`
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

// shellUseInput is the input of a Bash or Monitor tool call; only Bash carries Background.
type shellUseInput struct {
	Command     string `json:"command"`
	Description string `json:"description"`
	Background  bool   `json:"run_in_background"`
}

// A line without one of these marks holds no shell news, so the scan never decodes it.
const (
	shellMarkUse   = "run_in_background"
	shellMarkMon   = `"Monitor"`
	shellMarkMonID = "Monitor started"
	shellMarkID    = "Command running in background"
	shellMarkDone  = "task-notification"
)

// shellResultPattern takes the id and the output file's path from a result's text. The id is
// Claude Code's own: lowercase letters and digits.
var shellResultPattern = regexp.MustCompile(`Command running in background with ID: ([a-z0-9]{1,32})\. Output is being written to: (.+?)\. You will be notified`)

// monitorResultPattern takes the task id from a Monitor call's result text.
var monitorResultPattern = regexp.MustCompile(`Monitor started \(task ([a-z0-9]{1,32})[,)]`)

// Shells answers the background commands and Monitors the transcript at path ran, in transcript
// order: each starts at its call, takes its id and output file from the answer, and stays
// running until a notification names its call. Only complete lines count, and a line without a
// shell's marks is never decoded.
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
	scan := shellScan{at: map[string]int{}, heard: map[int]string{}}
	reader := bufio.NewReader(file)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := reader.ReadBytes('\n')
		if len(raw) > 0 && raw[len(raw)-1] == '\n' {
			scan.line(raw, &shells)
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

// shellScan is what a scan keeps between lines. at finds a shell's row by its call's id and,
// once the answer names it, by its task id. heard holds, per Monitor row, the text of the event
// an attachment delivered last, until the user line that repeats it is read.
type shellScan struct {
	at    map[string]int
	heard map[int]string
}

// line takes one complete transcript line into the shells: a background Bash call or a Monitor
// call adds one, its answer gives it the id and the output file, a notification ends it or, for
// a Monitor's event, counts into it.
func (scan shellScan) line(raw []byte, shells *[]Shell) {
	if !bytes.Contains(raw, []byte(shellMarkUse)) &&
		!bytes.Contains(raw, []byte(shellMarkMon)) &&
		!bytes.Contains(raw, []byte(shellMarkMonID)) &&
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
			if block.Type != "tool_use" || (block.Name != "Bash" && block.Name != "Monitor") {
				continue
			}
			var input shellUseInput
			if json.Unmarshal(block.Input, &input) != nil {
				continue
			}
			kind := ShellKindMonitor
			if block.Name == "Bash" {
				if !input.Background {
					continue
				}
				kind = ShellKindBash
			}
			*shells = append(*shells, Shell{
				Kind:        kind,
				Command:     input.Command,
				Description: input.Description,
				State:       "running",
				StartedAt:   line.Timestamp,
			})
			scan.at[block.ID] = len(*shells) - 1
		}
	case "user":
		var text string
		if json.Unmarshal(line.Message.Content, &text) == nil {
			// a string content is a prompt or, while the session is idle, the task
			// notification itself
			if note, ok := parseNotification(text); ok {
				scan.note(note, line.Timestamp, false, shells)
			}
			return
		}
		var blocks []contentBlock
		if json.Unmarshal(line.Message.Content, &blocks) != nil {
			return
		}
		for _, block := range blocks {
			if block.Type != "tool_result" {
				continue
			}
			i, ok := scan.at[block.ToolUseID]
			if !ok {
				continue
			}
			text := resultText(block.Content)
			if id, output, ok := shellOutputOf(text, line.SessionID); ok {
				(*shells)[i].ID = id
				(*shells)[i].output = output
				scan.at[id] = i
			} else if m := monitorResultPattern.FindStringSubmatch(text); m != nil {
				(*shells)[i].ID = m[1]
				scan.at[m[1]] = i
			}
		}
	case "attachment":
		if line.Attachment.Type != "queued_command" {
			return
		}
		if note, ok := parseNotification(line.Attachment.Prompt); ok {
			scan.note(note, line.Timestamp, true, shells)
		}
	}
}

// note applies one notification. A shell ends at the first notification that names its call
// with a final status, at that line's timestamp, taking the notification's status and exit
// code; a notification naming an unknown call, or none, ends nothing. A Monitor's event
// notification, found by its task id, ends nothing and counts once: an attachment's event is
// remembered so the user line that repeats it adds nothing.
func (scan shellScan) note(note notification, endedAt string, attachment bool, shells *[]Shell) {
	if i, ok := scan.at[note.ToolUseID]; ok && !(note.Status == "running" && (*shells)[i].Kind == ShellKindMonitor) {
		endShell(i, note, endedAt, shells)
		return
	}
	i, ok := scan.at[note.TaskID]
	if !ok || note.Event == "" || (*shells)[i].Kind != ShellKindMonitor || (*shells)[i].State == "ended" {
		return
	}
	if attachment {
		scan.heard[i] = note.Event
	} else {
		repeat := scan.heard[i] == note.Event
		delete(scan.heard, i)
		if repeat {
			return
		}
	}
	(*shells)[i].Events++
}

// endShell ends shell i with the notification's status and exit code.
func endShell(i int, note notification, endedAt string, shells *[]Shell) {
	shell := &(*shells)[i]
	if shell.State == "ended" {
		return
	}
	shell.State = "ended"
	shell.EndedAt = endedAt
	if note.Status != "" {
		shell.Status = note.Status
	}
	if note.ExitCode != nil {
		shell.ExitCode = note.ExitCode
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
	var output, kind string
	found := false
	for _, shell := range shells {
		if shell.ID == id {
			output, kind, found = shell.output, shell.Kind, true
			break
		}
	}
	if !found {
		return OutputPage{}, false, nil
	}
	if output == "" {
		// a Monitor streams events; its result never names an output file unless the
		// transcript says so, and then there is nothing to page
		return OutputPage{Next: after}, kind != ShellKindMonitor, nil
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
