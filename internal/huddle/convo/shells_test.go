package convo

// The shells of one session: the background commands a transcript's lines describe, the state
// each notification ends, and the output file only the session's own tasks folder may name.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// shellUseLine is one assistant line calling Bash in the background.
func shellUseLine(uuid, ts, toolUse, command, description string) string {
	return fmt.Sprintf(`{"type":"assistant","uuid":%q,"timestamp":%q,"sessionId":"s1","message":{"role":"assistant","content":[{"type":"tool_use","id":%q,"name":"Bash","input":{"command":%q,"description":%q,"run_in_background":true}}]}}`, uuid, ts, toolUse, command, description)
}

// shellResultLine is one user line answering a tool call.
func shellResultLine(uuid, ts, toolUse, text string) string {
	return fmt.Sprintf(`{"type":"user","uuid":%q,"timestamp":%q,"sessionId":"s1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":%q}]}}`, uuid, ts, toolUse, text)
}

// shellNotificationLine is one attachment line telling a background command ended.
func shellNotificationLine(ts, shellID, toolUse, outputFile, status, summary string) string {
	prompt := fmt.Sprintf("<task-notification>\n<task-id>%s</task-id>\n<tool-use-id>%s</tool-use-id>\n<output-file>%s</output-file>\n<status>%s</status>\n<summary>%s\n</summary>\n</task-notification>", shellID, toolUse, outputFile, status, summary)
	return fmt.Sprintf(`{"type":"attachment","uuid":%q,"timestamp":%q,"sessionId":"s1","attachment":{"type":"queued_command","prompt":%q}}`, "n-"+shellID, ts, prompt)
}

// backgroundText is the tool_result text Claude Code writes for a background command.
func backgroundText(dir, session, shellID string) string {
	return fmt.Sprintf("Command running in background with ID: %s. Output is being written to: %s. You will be notified when it completes.", shellID, filepath.Join(dir, session, "tasks", shellID+".output"))
}

// backgroundTextAt is the same text naming its output path verbatim.
func backgroundTextAt(shellID, path string) string {
	return fmt.Sprintf("Command running in background with ID: %s. Output is being written to: %s. You will be notified when it completes.", shellID, path)
}

// writeShellsFixture lays out one session the way Claude Code writes it: the transcript s1.jsonl,
// its outputs under s1/tasks, one result naming another session's folder. It answers the
// transcript's path.
func writeShellsFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	tasks := filepath.Join(dir, "s1", "tasks")
	if err := os.MkdirAll(tasks, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"b4io7vyqm.output":  "PASS ok  huddle/convo\nFAIL ok  huddle/server\n",
		"k7w2n9x4d1.output": "waiting\n",
	} {
		if err := os.WriteFile(filepath.Join(tasks, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	lines := []string{
		shellUseLine("a1", "2026-10-26T10:00:01.000Z", "toolu_1", "go test ./...", "Run the gates"),
		shellResultLine("u1", "2026-10-26T10:00:02.000Z", "toolu_1", backgroundText(dir, "s1", "b4io7vyqm")),
		shellUseLine("a2", "2026-10-26T10:00:03.000Z", "toolu_2", "mise run check", "Wait for the gates"),
		shellResultLine("u2", "2026-10-26T10:00:04.000Z", "toolu_2", backgroundText(dir, "s1", "k7w2n9x4d1")),
		// a foreground Bash call is none of the shells
		`{"type":"assistant","uuid":"a3","timestamp":"2026-10-26T10:00:05.000Z","sessionId":"s1","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_3","name":"Bash","input":{"command":"go build ./...","description":"Build it"}}]}}`,
		shellResultLine("u3", "2026-10-26T10:00:06.000Z", "toolu_3", "done"),
		// a background call whose result names another session's folder lists with no output
		shellUseLine("a4", "2026-10-26T10:00:07.000Z", "toolu_4", "sleep 30", "Hold on"),
		shellResultLine("u4", "2026-10-26T10:00:08.000Z", "toolu_4", backgroundText(dir, "other", "q5f8t3h6j0")),
		shellNotificationLine("2026-10-26T10:00:20.000Z", "b4io7vyqm", "toolu_1", filepath.Join(dir, "s1", "tasks", "b4io7vyqm.output"), "completed", `Background command "Run the gates" completed (exit code 1)`),
	}
	path := filepath.Join(dir, "s1.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// fixtureShellOutput answers one of the fixture's output files, writing body over it when body is
// not empty.
func fixtureShellOutput(t *testing.T, transcript, name, body string) string {
	t.Helper()
	out := filepath.Join(filepath.Dir(transcript), "s1", "tasks", name)
	if body != "" {
		if err := os.WriteFile(out, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// shellsFixture is what the fixture yields, in order, output paths and all: only the shell
// whose result named the session's own tasks folder carries one.
func shellsFixture(dir string) []Shell {
	return []Shell{
		{ID: "b4io7vyqm", Command: "go test ./...", Description: "Run the gates", State: "ended", Status: "completed", ExitCode: intShellCode(1), StartedAt: "2026-10-26T10:00:01.000Z", EndedAt: "2026-10-26T10:00:20.000Z", output: filepath.Join(dir, "s1", "tasks", "b4io7vyqm.output")},
		{ID: "k7w2n9x4d1", Command: "mise run check", Description: "Wait for the gates", State: "running", StartedAt: "2026-10-26T10:00:03.000Z", output: filepath.Join(dir, "s1", "tasks", "k7w2n9x4d1.output")},
		{ID: "q5f8t3h6j0", Command: "sleep 30", Description: "Hold on", State: "running", StartedAt: "2026-10-26T10:00:07.000Z"},
	}
}

func intShellCode(code int) *int { return &code }

// tail answers a string's last few bytes for a failure message.
func tail(s string) string {
	if len(s) > 16 {
		return s[len(s)-16:]
	}
	return s
}

func TestShellsAnswersTheBackgroundShells(t *testing.T) {
	transcript := writeShellsFixture(t)
	shells, err := Shells(context.Background(), transcript)
	if err != nil {
		t.Fatalf("shells: %v", err)
	}
	want := shellsFixture(filepath.Dir(transcript))
	if !reflect.DeepEqual(shells, want) {
		t.Fatalf("shells:\ngot  %+v\nwant %+v", shells, want)
	}
	// the output path never leaves the package
	raw, err := json.Marshal(shells)
	if err != nil {
		t.Fatalf("marshal the shells: %v", err)
	}
	if strings.Contains(string(raw), `"output"`) {
		t.Errorf("the shells answer carries an output path: %s", raw)
	}
}

func TestShellsAnswersTheMissingTranscript(t *testing.T) {
	if _, err := Shells(context.Background(), filepath.Join(t.TempDir(), "gone.jsonl")); err == nil {
		t.Fatalf("the missing transcript answered no error, want one")
	}
}

func TestShellsCountOnlyCleanAbsoluteOutputPaths(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		shellUseLine("a1", "2026-10-26T10:00:01.000Z", "toolu_1", "go vet ./...", "Vet it"),
		// a relative path inside the session's own layout is still not an output file
		shellResultLine("u1", "2026-10-26T10:00:02.000Z", "toolu_1", backgroundTextAt("r9e2l4a6t8", "s1/tasks/r9e2l4a6t8.output")),
		shellUseLine("a2", "2026-10-26T10:00:03.000Z", "toolu_2", "go build ./...", "Build it"),
		// neither is one a .. segment makes unclean (built by hand: Join would clean it away)
		shellResultLine("u2", "2026-10-26T10:00:04.000Z", "toolu_2", backgroundTextAt("w7a1t3c5h9", dir+"/x/../"+filepath.Join("s1", "tasks", "w7a1t3c5h9.output"))),
	}
	path := filepath.Join(dir, "s1.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	shells, err := Shells(context.Background(), path)
	if err != nil {
		t.Fatalf("shells: %v", err)
	}
	if len(shells) != 2 {
		t.Fatalf("shells = %v, want two", shells)
	}
	if shells[0].ID != "r9e2l4a6t8" || shells[0].output != "" {
		t.Errorf("the relative path's shell = %q with output %q, want the id and no output", shells[0].ID, shells[0].output)
	}
	if shells[1].ID != "w7a1t3c5h9" || shells[1].output != "" {
		t.Errorf("the dotted path's shell = %q with output %q, want the id and no output", shells[1].ID, shells[1].output)
	}
}

func TestShellOutputPagesTheOutputFile(t *testing.T) {
	ctx := context.Background()
	transcript := writeShellsFixture(t)
	shells, err := Shells(ctx, transcript)
	if err != nil {
		t.Fatalf("shells: %v", err)
	}

	// a bit over 100 KiB of complete lines over the fixture's own output
	var b strings.Builder
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&b, "%04d %s\n", i, strings.Repeat("é", 48))
	}
	body := b.String()
	if len(body) <= MaxShellRead {
		t.Fatalf("the output is %d bytes, want over MaxShellRead (%d)", len(body), MaxShellRead)
	}
	out := fixtureShellOutput(t, transcript, "b4io7vyqm.output", body)
	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat the output: %v", err)
	}

	first, ok, err := ShellOutput(ctx, shells, "b4io7vyqm", 0)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if !ok {
		t.Fatalf("first page: the shell went unknown")
	}
	if len(first.Text) > MaxShellRead {
		t.Errorf("the first page is %d bytes, want at most %d", len(first.Text), MaxShellRead)
	}
	if !strings.HasSuffix(first.Text, "\n") {
		t.Errorf("the first page ends %q, want a newline", tail(first.Text))
	}
	if first.Next != int64(len(first.Text)) {
		t.Errorf("the first page stops at %d, want its text's %d bytes", first.Next, len(first.Text))
	}
	if first.Size != info.Size() {
		t.Errorf("the first page's size is %d, want the file's %d", first.Size, info.Size())
	}

	combined := first.Text
	for next := first.Next; next < info.Size(); {
		page, ok, err := ShellOutput(ctx, shells, "b4io7vyqm", next)
		if err != nil {
			t.Fatalf("page at %d: %v", next, err)
		}
		if !ok {
			t.Fatalf("page at %d: the shell went unknown", next)
		}
		if len(page.Text) > MaxShellRead {
			t.Errorf("the page at %d is %d bytes, want at most %d", next, len(page.Text), MaxShellRead)
		}
		combined += page.Text
		if page.Next == next {
			t.Fatalf("the page at %d stops moving", next)
		}
		next = page.Next
	}
	if combined != body {
		t.Errorf("the pages read %d bytes, want the file's %d", len(combined), len(body))
	}
}

func TestShellOutputAnswersWholeRunesOverALongLine(t *testing.T) {
	ctx := context.Background()
	transcript := writeShellsFixture(t)
	shells, err := Shells(ctx, transcript)
	if err != nil {
		t.Fatalf("shells: %v", err)
	}
	// one line well over the window with no newline in it: the cut lands inside a two-byte rune
	line := "x" + strings.Repeat("é", 40000) + "\n"
	fixtureShellOutput(t, transcript, "b4io7vyqm.output", line)

	first, ok, err := ShellOutput(ctx, shells, "b4io7vyqm", 0)
	if err != nil || !ok {
		t.Fatalf("first page: ok %v err %v", ok, err)
	}
	if !utf8.ValidString(first.Text) {
		t.Errorf("the first page is not valid UTF-8: %q", tail(first.Text))
	}
	if !strings.HasSuffix(first.Text, "é") {
		t.Errorf("the first page ends %q, want a whole rune", tail(first.Text))
	}

	second, ok, err := ShellOutput(ctx, shells, "b4io7vyqm", first.Next)
	if err != nil || !ok {
		t.Fatalf("second page: ok %v err %v", ok, err)
	}
	if !utf8.ValidString(second.Text) {
		t.Errorf("the second page is not valid UTF-8: %q", tail(second.Text))
	}
	if first.Text+second.Text != line {
		t.Errorf("the pages read %d bytes, want the line's %d", len(first.Text)+len(second.Text), len(line))
	}
}

func TestShellOutputRefusesAnUnknownShell(t *testing.T) {
	shells, err := Shells(context.Background(), writeShellsFixture(t))
	if err != nil {
		t.Fatalf("shells: %v", err)
	}
	if _, ok, err := ShellOutput(context.Background(), shells, "zzzzzzzz", 0); ok || err != nil {
		t.Fatalf("an unknown shell answered ok %v err %v, want false and no error", ok, err)
	}
}

func TestShellOutputAnswersTheMissingOutputFile(t *testing.T) {
	transcript := writeShellsFixture(t)
	shells, err := Shells(context.Background(), transcript)
	if err != nil {
		t.Fatalf("shells: %v", err)
	}
	if err := os.Remove(fixtureShellOutput(t, transcript, "k7w2n9x4d1.output", "")); err != nil {
		t.Fatal(err)
	}
	page, ok, err := ShellOutput(context.Background(), shells, "k7w2n9x4d1", 7)
	if err != nil {
		t.Fatalf("the missing file answered %v, want an empty page", err)
	}
	if !ok {
		t.Fatalf("the missing file went unknown")
	}
	if page.Text != "" || page.Next != 7 || page.Size != 0 {
		t.Errorf("the missing file answered %+v, want an empty page at 7", page)
	}
}

func TestShellOutputAnswersAShellWithNoOutputFile(t *testing.T) {
	ctx := context.Background()
	shells, err := Shells(ctx, writeShellsFixture(t))
	if err != nil {
		t.Fatalf("shells: %v", err)
	}
	// the shell whose result named another session's folder lists, and its output answers
	// an empty page exactly as a missing output file does
	page, ok, err := ShellOutput(ctx, shells, "q5f8t3h6j0", 7)
	if err != nil {
		t.Fatalf("the rejected path answered %v, want an empty page", err)
	}
	if !ok {
		t.Fatalf("the rejected path went unknown")
	}
	if page.Text != "" || page.Next != 7 || page.Size != 0 {
		t.Errorf("the rejected path answered %+v, want an empty page at 7", page)
	}
}
