package server

// The conversation route and the transcript watch (convo.go): a session's transcript served as
// conversation items off a byte cursor, and the registry stream told when a live transcript
// grows. Every test runs its own server child against its own transcript files.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// reportConvoSession registers one live session; a path names its transcript.
func reportConvoSession(t *testing.T, srv *childServer, id, path string) {
	t.Helper()
	body := fmt.Sprintf(`{"session_id":%q,"machine":"box","last_event":"UserPromptSubmit","last_event_at":%q`, id, isoAgo(time.Second))
	if path != "" {
		body += fmt.Sprintf(`,"transcript_path":%q`, path)
	}
	body += "}"
	code, out := srv.post(t, "/api/registry/report", body, nil)
	if code != 200 {
		t.Fatalf("report %s: %d %s", id, code, out)
	}
}

// writeTranscript writes one transcript of the given lines, each already newline-terminated.
func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(joinLines(lines)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func joinLines(lines []string) string {
	out := ""
	for _, line := range lines {
		out += line + "\n"
	}
	return out
}

// the prompt line and the answer line the transcript fixtures use
const (
	convoPromptLine = `{"type":"user","uuid":"u1","timestamp":"2026-10-26T10:00:01.000Z","message":{"role":"user","content":"run the tests"}}`
	convoAnswerLine = `{"type":"assistant","uuid":"a1","timestamp":"2026-10-26T10:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Running them."}]}}`
)

// a subagent's prompt and answer, sidechain lines like Claude Code writes them
const (
	subagentPromptLine = `{"type":"user","uuid":"bu1","timestamp":"2026-10-26T10:00:05.000Z","isSidechain":true,"message":{"role":"user","content":"tab work"}}`
	subagentAnswerLine = `{"type":"assistant","uuid":"ba1","timestamp":"2026-10-26T10:00:06.000Z","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"Done."}]}}`
)

// writeAgentLive writes one live session with one subagent beside it, Claude Code's layout, and
// answers the main transcript's path.
func writeAgentLive(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	main := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(main, []byte(convoPromptLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "s", "subagents")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	agent := filepath.Join(sub, "agent-b2.jsonl")
	if err := os.WriteFile(agent, []byte(subagentPromptLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return main
}

// itemsOf reads one conversation answer's items.
func itemsOf(t *testing.T, body string) []map[string]any {
	t.Helper()
	page := bodyMap(t, body)
	raw, _ := page["items"].([]any)
	items := make([]map[string]any, 0, len(raw))
	for _, one := range raw {
		items = append(items, one.(map[string]any))
	}
	return items
}

// nextOf reads one conversation answer's cursor.
func nextOf(t *testing.T, body string) int64 {
	t.Helper()
	next, _ := bodyMap(t, body)["next"].(float64)
	return int64(next)
}

func TestTheConversationRouteAnswersItemsAfterTheCursor(t *testing.T) {
	srv := startServer(t, map[string]string{"HUDDLE_TRANSCRIPT_CHECK_MS": "50"})
	path := writeTranscript(t, convoPromptLine, convoAnswerLine)
	reportConvoSession(t, srv, "s-1", path)
	whole := joinLines([]string{convoPromptLine, convoAnswerLine})

	// the first read answers both lines from the start
	code, _, body := srv.get(t, "/api/sessions/s-1/conversation", nil)
	if code != 200 {
		t.Fatalf("conversation: %d %s", code, body)
	}
	items := itemsOf(t, body)
	if len(items) != 2 || items[0]["kind"] != "user" || items[0]["text"] != "run the tests" || items[1]["kind"] != "assistant" {
		t.Errorf("items = %v, want the prompt then the answer", items)
	}
	if next := nextOf(t, body); next != int64(len(whole)) {
		t.Errorf("next = %d, want the end of the second line (%d)", next, len(whole))
	}

	// a read from that cursor answers what came after, here nothing, and the cursor holds
	code, _, body = srv.get(t, fmt.Sprintf("/api/sessions/s-1/conversation?after=%d", len(whole)), nil)
	if code != 200 {
		t.Fatalf("conversation after the cursor: %d %s", code, body)
	}
	if items := itemsOf(t, body); len(items) != 0 {
		t.Errorf("items after the cursor = %v, want none", items)
	}
	if next := nextOf(t, body); next != int64(len(whole)) {
		t.Errorf("next after the cursor = %d, want %d", next, len(whole))
	}

	// a torn last line stays unconsumed: the read from the cursor takes none of it
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"type":"user","uuid":"u2"`); err != nil {
		t.Fatal(err)
	}
	file.Close()
	code, _, body = srv.get(t, fmt.Sprintf("/api/sessions/s-1/conversation?after=%d", len(whole)), nil)
	if code != 200 {
		t.Fatalf("conversation over a torn line: %d %s", code, body)
	}
	if items := itemsOf(t, body); len(items) != 0 {
		t.Errorf("items over a torn line = %v, want none", items)
	}
	if next := nextOf(t, body); next != int64(len(whole)) {
		t.Errorf("next over a torn line = %d, want %d", next, len(whole))
	}

	// a transcript the registry names but the disk no longer has answers an empty page
	reportConvoSession(t, srv, "s-gone", filepath.Join(t.TempDir(), "missing.jsonl"))
	code, _, body = srv.get(t, "/api/sessions/s-gone/conversation", nil)
	if code != 200 {
		t.Fatalf("conversation over a missing file: %d %s", code, body)
	}
	if items := itemsOf(t, body); len(items) != 0 || nextOf(t, body) != 0 {
		t.Errorf("page over a missing file = %s, want an empty page at 0", body)
	}

	// a session whose reports never named a transcript answers an empty page too
	reportConvoSession(t, srv, "s-bare", "")
	code, _, body = srv.get(t, "/api/sessions/s-bare/conversation", nil)
	if code != 200 {
		t.Fatalf("conversation without a transcript: %d %s", code, body)
	}
	if items := itemsOf(t, body); len(items) != 0 || nextOf(t, body) != 0 {
		t.Errorf("page without a transcript = %s, want an empty page at 0", body)
	}

	// the route reads the session's own transcript and nothing a query names
	code, _, body = srv.get(t, "/api/sessions/s-bare/conversation?path=/etc/hosts", nil)
	if code != 200 {
		t.Fatalf("conversation with a path in the query: %d %s", code, body)
	}
	if items := itemsOf(t, body); len(items) != 0 {
		t.Errorf("a query's path reached a file: %v", items)
	}
}

func TestTheConversationRouteRefusesWhatItCannotRead(t *testing.T) {
	srv := startServer(t, nil)
	reportConvoSession(t, srv, "s-1", filepath.Join(t.TempDir(), "s-1.jsonl"))

	for _, after := range []string{"yesterday", "-1", "1.5"} {
		code, _, body := srv.get(t, "/api/sessions/s-1/conversation?after="+after, nil)
		if code != 400 {
			t.Errorf("after=%s: %d %s, want 400", after, code, body)
			continue
		}
		if !strings.Contains(body, "after") {
			t.Errorf("after=%s: refusal = %s, want it to name after", after, body)
		}
	}

	code, _, body := srv.get(t, "/api/sessions/s-none/conversation", nil)
	if code != 404 || !strings.Contains(body, "no such session") {
		t.Errorf("unknown session: %d %s, want 404 naming it", code, body)
	}
}

func TestTheRegistryStreamSaysWhenATranscriptGrows(t *testing.T) {
	srv := startServer(t, map[string]string{"HUDDLE_TRANSCRIPT_CHECK_MS": "50"})
	path := writeTranscript(t, convoPromptLine)
	reportConvoSession(t, srv, "s-1", path)
	frames := openRegistryLive(t, srv)
	nextRegistryEvent(t, frames, "hello")

	// let a tick pass so the watch has seen the transcript at its first size; a first sighting
	// records the size and fans nothing
	time.Sleep(250 * time.Millisecond)

	whole := joinLines([]string{convoPromptLine, convoAnswerLine})
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(convoAnswerLine + "\n"); err != nil {
		t.Fatal(err)
	}
	file.Close()

	data := nextRegistryEvent(t, frames, "conversation.grew")
	if data["session_id"] != "s-1" {
		t.Errorf("grew = %v, want session s-1", data)
	}
	if data["agent"] != "main" {
		t.Errorf("grew = %v, want the main agent", data)
	}
	if size, _ := data["size"].(float64); int64(size) != int64(len(whole)) {
		t.Errorf("grew size = %v, want %d", data["size"], len(whole))
	}
}

func TestTheRegistryStreamSaysWhenASubagentTranscriptGrows(t *testing.T) {
	srv := startServer(t, map[string]string{"HUDDLE_TRANSCRIPT_CHECK_MS": "50"})
	main := writeAgentLive(t)
	agent := filepath.Join(filepath.Dir(main), "s", "subagents", "agent-b2.jsonl")
	reportConvoSession(t, srv, "s-1", main)
	frames := openRegistryLive(t, srv)
	nextRegistryEvent(t, frames, "hello")

	// let a tick pass so the watch has seen both transcripts at their first sizes
	time.Sleep(250 * time.Millisecond)

	whole := joinLines([]string{subagentPromptLine, subagentAnswerLine})
	file, err := os.OpenFile(agent, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(subagentAnswerLine + "\n"); err != nil {
		t.Fatal(err)
	}
	file.Close()

	data := nextRegistryEvent(t, frames, "conversation.grew")
	if data["session_id"] != "s-1" || data["agent"] != "b2" {
		t.Errorf("grew = %v, want session s-1's agent b2", data)
	}
	if size, _ := data["size"].(float64); int64(size) != int64(len(whole)) {
		t.Errorf("grew size = %v, want %d", data["size"], len(whole))
	}
}
