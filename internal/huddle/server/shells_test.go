package server

// The shells routes: one agent's background shells listed from its transcript, and one shell's
// output paged off a byte cursor like the conversation is. Every test runs its own server child
// against its own transcript files.

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shellsUseLine is one assistant line calling Bash in the background, sidechain or not.
func shellsUseLine(session string, side bool, uuid, ts, toolUse, command, description string) string {
	sideJSON := ""
	if side {
		sideJSON = `,"isSidechain":true`
	}
	return fmt.Sprintf(`{"type":"assistant","uuid":%q,"timestamp":%q,"sessionId":%q%s,"message":{"role":"assistant","content":[{"type":"tool_use","id":%q,"name":"Bash","input":{"command":%q,"description":%q,"run_in_background":true}}]}}`, uuid, ts, session, sideJSON, toolUse, command, description)
}

// shellsResultLine is one user line answering a tool call, sidechain or not.
func shellsResultLine(session string, side bool, uuid, ts, toolUse, text string) string {
	sideJSON := ""
	if side {
		sideJSON = `,"isSidechain":true`
	}
	return fmt.Sprintf(`{"type":"user","uuid":%q,"timestamp":%q,"sessionId":%q%s,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":%q}]}}`, uuid, ts, session, sideJSON, toolUse, text)
}

// shellsNotificationLine is one attachment line telling a background command ended.
func shellsNotificationLine(session, ts, shellID, toolUse, status, summary string) string {
	prompt := fmt.Sprintf("<task-notification>\n<task-id>%s</task-id>\n<tool-use-id>%s</tool-use-id>\n<status>%s</status>\n<summary>%s\n</summary>\n</task-notification>", shellID, toolUse, status, summary)
	return fmt.Sprintf(`{"type":"attachment","uuid":%q,"timestamp":%q,"sessionId":%q,"attachment":{"type":"queued_command","prompt":%q}}`, "n-"+shellID, ts, session, prompt)
}

// backgroundedText is the tool_result text Claude Code writes for a background command.
func backgroundedText(dir, session, shellID string) string {
	return fmt.Sprintf("Command running in background with ID: %s. Output is being written to: %s. You will be notified when it completes.", shellID, filepath.Join(dir, session, "tasks", shellID+".output"))
}

// writeShellsLive lays out one live session the way Claude Code writes it: the main transcript
// s1.jsonl with one ended shell and one running, and a subagent agent-a1.jsonl with one running
// shell of its own, every output under s1/tasks. It answers the main transcript's path.
func writeShellsLive(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	tasks := filepath.Join(dir, "s1", "tasks")
	if err := os.MkdirAll(tasks, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"b4io7vyqm.output":  "PASS ok  huddle/convo\n",
		"a1b2c3d4e5.output": "scanning\n",
	} {
		if err := os.WriteFile(filepath.Join(tasks, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	main := filepath.Join(dir, "s1.jsonl")
	mainLines := []string{
		shellsUseLine("s1", false, "a1", "2026-10-26T10:00:01.000Z", "toolu_1", "go test ./...", "Run the gates"),
		shellsResultLine("s1", false, "u1", "2026-10-26T10:00:02.000Z", "toolu_1", backgroundedText(dir, "s1", "b4io7vyqm")),
		shellsUseLine("s1", false, "a2", "2026-10-26T10:00:03.000Z", "toolu_2", "mise run check", "Wait for the gates"),
		shellsResultLine("s1", false, "u2", "2026-10-26T10:00:04.000Z", "toolu_2", backgroundedText(dir, "s1", "k7w2n9x4d1")),
		shellsNotificationLine("s1", "2026-10-26T10:00:20.000Z", "b4io7vyqm", "toolu_1", "completed", `Background command "Run the gates" completed (exit code 1)`),
	}
	if err := os.WriteFile(main, []byte(joinLines(mainLines)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "s1", "subagents"), 0o700); err != nil {
		t.Fatal(err)
	}
	subLines := []string{
		shellsUseLine("s1", true, "ba1", "2026-10-26T10:00:05.000Z", "toolu_s1", "rg TODO internal", "Scan the tree"),
		shellsResultLine("s1", true, "bu1", "2026-10-26T10:00:06.000Z", "toolu_s1", backgroundedText(dir, "s1", "a1b2c3d4e5")),
	}
	agent := filepath.Join(dir, "s1", "subagents", "agent-a1.jsonl")
	if err := os.WriteFile(agent, []byte(joinLines(subLines)), 0o600); err != nil {
		t.Fatal(err)
	}
	return main
}

// shellsOf reads one shells answer's list.
func shellsOf(t *testing.T, body string) []map[string]any {
	t.Helper()
	raw, _ := bodyMap(t, body)["shells"].([]any)
	shells := make([]map[string]any, 0, len(raw))
	for _, one := range raw {
		shells = append(shells, one.(map[string]any))
	}
	return shells
}

func TestTheShellsRoutesAnswerASessionAndItsAgents(t *testing.T) {
	srv := startServer(t, nil)
	reportConvoSession(t, srv, "s-1", writeShellsLive(t))

	// the main agent's shells: the ended one with its exit code, the running one bare
	code, _, body := srv.get(t, "/api/sessions/s-1/agents/main/shells", nil)
	if code != 200 {
		t.Fatalf("main shells: %d %s", code, body)
	}
	shells := shellsOf(t, body)
	if len(shells) != 2 {
		t.Fatalf("main shells = %v, want two", shells)
	}
	if shells[0]["id"] != "b4io7vyqm" || shells[0]["state"] != "ended" || shells[0]["status"] != "completed" || shells[0]["exit_code"] != float64(1) {
		t.Errorf("first shell = %v, want the ended b4io7vyqm at exit code 1", shells[0])
	}
	if _, there := shells[0]["output"]; there {
		t.Errorf("first shell leaks an output path: %v", shells[0])
	}
	if shells[1]["id"] != "k7w2n9x4d1" || shells[1]["state"] != "running" {
		t.Errorf("second shell = %v, want the running k7w2n9x4d1", shells[1])
	}
	if _, there := shells[1]["exit_code"]; there {
		t.Errorf("a running shell carries an exit code: %v", shells[1])
	}

	// the subagent's shells, from its own transcript's sidechain lines
	code, _, body = srv.get(t, "/api/sessions/s-1/agents/a1/shells", nil)
	if code != 200 {
		t.Fatalf("subagent shells: %d %s", code, body)
	}
	if shells = shellsOf(t, body); len(shells) != 1 || shells[0]["id"] != "a1b2c3d4e5" || shells[0]["state"] != "running" {
		t.Errorf("subagent shells = %v, want the running a1b2c3d4e5", shells)
	}

	// one shell's output, paged from a byte cursor
	code, _, body = srv.get(t, "/api/sessions/s-1/agents/main/shells/b4io7vyqm/output", nil)
	if code != 200 {
		t.Fatalf("main shell output: %d %s", code, body)
	}
	page := bodyMap(t, body)
	const text = "PASS ok  huddle/convo\n"
	if page["text"] != text {
		t.Errorf("output text = %q, want %q", page["text"], text)
	}
	if next, _ := page["next"].(float64); int64(next) != int64(len(text)) {
		t.Errorf("output next = %v, want %d", page["next"], len(text))
	}
	if size, _ := page["size"].(float64); int64(size) != int64(len(text)) {
		t.Errorf("output size = %v, want %d", page["size"], len(text))
	}

	// a later cursor resumes
	code, _, body = srv.get(t, fmt.Sprintf("/api/sessions/s-1/agents/main/shells/b4io7vyqm/output?after=%d", len(text)), nil)
	if code != 200 {
		t.Fatalf("main shell output after the cursor: %d %s", code, body)
	}
	if page = bodyMap(t, body); page["text"] != "" || int64(nextOf(t, body)) != int64(len(text)) {
		t.Errorf("output after the cursor = %s, want an empty page at %d", body, len(text))
	}

	// the subagent's own output
	code, _, body = srv.get(t, "/api/sessions/s-1/agents/a1/shells/a1b2c3d4e5/output", nil)
	if code != 200 {
		t.Fatalf("subagent shell output: %d %s", code, body)
	}
	if page = bodyMap(t, body); page["text"] != "scanning\n" {
		t.Errorf("subagent output text = %q, want the scanning", page["text"])
	}
}

func TestTheShellsOutputRouteAnswersAShellWithNoOutputFile(t *testing.T) {
	srv := startServer(t, nil)
	dir := t.TempDir()
	main := filepath.Join(dir, "s1.jsonl")
	lines := []string{
		shellsUseLine("s1", false, "a1", "2026-10-26T10:00:01.000Z", "toolu_1", "sleep 30", "Hold on"),
		shellsResultLine("s1", false, "u1", "2026-10-26T10:00:02.000Z", "toolu_1", backgroundedText(dir, "other", "q5f8t3h6j0")),
	}
	if err := os.WriteFile(main, []byte(joinLines(lines)), 0o600); err != nil {
		t.Fatal(err)
	}
	reportConvoSession(t, srv, "s-1", main)

	// the shell lists
	code, _, body := srv.get(t, "/api/sessions/s-1/agents/main/shells", nil)
	if code != 200 {
		t.Fatalf("main shells: %d %s", code, body)
	}
	if shells := shellsOf(t, body); len(shells) != 1 || shells[0]["id"] != "q5f8t3h6j0" {
		t.Fatalf("main shells = %v, want the q5f8t3h6j0", shells)
	}

	// and its output route answers an empty page, not a refusal
	code, _, body = srv.get(t, "/api/sessions/s-1/agents/main/shells/q5f8t3h6j0/output", nil)
	if code != 200 {
		t.Fatalf("the rejected path's output: %d %s, want 200", code, body)
	}
	if page := bodyMap(t, body); page["text"] != "" {
		t.Errorf("the rejected path's output = %s, want an empty text", body)
	}
}

func TestTheShellsRoutesRefuseWhatIsNotThere(t *testing.T) {
	srv := startServer(t, nil)
	reportConvoSession(t, srv, "s-1", writeShellsLive(t))

	code, _, body := srv.get(t, "/api/sessions/s-none/agents/main/shells", nil)
	if code != 404 || !strings.Contains(body, "no such session") {
		t.Errorf("unknown session: %d %s, want 404 naming it", code, body)
	}

	code, _, body = srv.get(t, "/api/sessions/s-1/agents/zz/shells", nil)
	if code != 404 || !strings.Contains(body, "no such agent") {
		t.Errorf("unknown agent: %d %s, want 404 naming it", code, body)
	}

	code, _, body = srv.get(t, "/api/sessions/s-1/agents/main/shells/zzzzzzzz/output", nil)
	if code != 404 || !strings.Contains(body, "no such shell") {
		t.Errorf("unknown shell: %d %s, want 404 naming it", code, body)
	}
}

func TestTheShellsRoutesDemandACredential(t *testing.T) {
	srv := startServer(t, nil)
	for _, path := range []string{
		"/api/sessions/s-1/agents/main/shells",
		"/api/sessions/s-1/agents/main/shells/b4io7vyqm/output",
	} {
		code, _, body, err := srv.request(bg, http.MethodGet, path, "", map[string]string{})
		if err != nil {
			t.Fatal(err)
		}
		if code != 401 {
			t.Errorf("%s without a token: %d %s, want 401", path, code, body)
		}
	}
}
