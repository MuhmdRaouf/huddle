package server

// The agent send route (agent_send.go): the owner's words to one of a session's subagents, one
// forward request to the main, which passes them on with SendMessage. Every test runs its own
// server child against its own transcript files and its own inbox socket.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// the subagent transcripts the fixture writes beside its session, and the meta files that
// describe them: a1 a running Explore, a2 a finished one
var (
	sendA1Body = joinLines([]string{
		`{"type":"user","uuid":"su1","timestamp":"2026-10-26T10:00:05.000Z","isSidechain":true,"message":{"role":"user","content":"scan the files"}}`,
	})
	sendA1Meta = `{"agentType":"Explore","description":"File scanner","spawnDepth":1,"finished":false}`
	sendA2Body = joinLines([]string{
		`{"type":"user","uuid":"su2","timestamp":"2026-10-26T10:00:06.000Z","isSidechain":true,"message":{"role":"user","content":"earlier work"}}`,
	})
	sendA2Meta = `{"agentType":"Explore","description":"Earlier scanner","spawnDepth":1,"finished":true}`
)

// writeSendSession writes one session transcript with its subagents beside it, Claude Code's
// layout, and answers the main transcript's path.
func writeSendSession(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	main := filepath.Join(dir, "s1.jsonl")
	if err := os.WriteFile(main, []byte(joinLines([]string{convoPromptLine, convoAnswerLine})), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "s1", "subagents")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"agent-a1.jsonl":     sendA1Body,
		"agent-a1.meta.json": sendA1Meta,
		"agent-a2.jsonl":     sendA2Body,
		"agent-a2.meta.json": sendA2Meta,
	} {
		if err := os.WriteFile(filepath.Join(sub, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return main
}

// reportSendSession lands one report for a live session on this machine naming both its inbox
// socket and its transcript, as the SessionStart hook's report does.
func reportSendSession(t *testing.T, srv *childServer, id, socket, transcript string) {
	t.Helper()
	body := fmt.Sprintf(`{"session_id":%q,"pid":%d,"inbox_socket":%q,"transcript_path":%q,"last_event":"UserPromptSubmit","last_event_at":%q}`,
		id, os.Getpid(), socket, transcript, isoAgo(2*time.Second))
	if code, out := srv.post(t, "/api/registry/report", body, nil); code != 200 {
		t.Fatalf("report %s: %d %s", id, code, out)
	}
}

func TestASendReachesASubagentThroughTheMain(t *testing.T) {
	srv, config := inboxServer(t)
	sock, ln := inboxListener(t)
	lines := inboxLines(t, ln)

	const id = "7b1d0e58-9a34-4f2c-b1a0-3f5d6e7c8a01"
	reportSendSession(t, srv, id, sock, writeSendSession(t))
	pid := os.Getpid()
	writeInboxSessions(t, config, pid,
		fmt.Sprintf(`{"sessionId":%q,"pid":%d,"messagingSocketPath":%q,"status":"idle"}`, id, pid, sock))
	keys := home.KeyFiles{Dir: filepath.Join(srv.home, "keys")}
	if err := keys.Set(context.Background(), "cc-messaging:"+id, "tok-5e91"); err != nil {
		t.Fatalf("set the token: %v", err)
	}

	code, out := srv.post(t, "/api/sessions/"+id+"/agents/a1/send", `{"text":"scan web/hub for dead code"}`, nil)
	if code != 200 {
		t.Fatalf("send to a1 = %d %s", code, out)
	}
	resp := bodyMap(t, out)
	if resp["ok"] != true || resp["agent"] != "a1" || resp["route"] != "inbox" {
		t.Fatalf("the send's answer = %s, want ok, agent a1, the inbox route", out)
	}
	if _, ok := resp["message"].(float64); !ok {
		t.Errorf("the send's message = %v, want the queued message's number", resp["message"])
	}

	// the wire carries the auth line with the session's token, then the forward request as one
	// user turn to the main
	data := <-lines
	if data == nil {
		t.Fatal("nothing reached the inbox socket")
	}
	got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(got) != 2 {
		t.Fatalf("the wire carries %d lines (%q), want the auth line then the user line", len(got), data)
	}
	var user struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(got[1]), &user); err != nil {
		t.Fatalf("decode the second line %q: %v", got[1], err)
	}
	for _, want := range []string{
		"subagent a1 (Explore: File scanner)",
		"SendMessage",
		"scan web/hub for dead code",
	} {
		if !strings.Contains(user.Message.Content, want) {
			t.Errorf("the forward = %q, want it to hold %q", user.Message.Content, want)
		}
	}
}

func TestASendRefusesWhatTheMainCannotForward(t *testing.T) {
	srv, _ := inboxServer(t)
	const id = "8c2e1f69-0b45-4d3d-c2b1-4a6e7f8d9b02"
	reportSendSession(t, srv, id, filepath.Join(t.TempDir(), "gone.sock"), writeSendSession(t))

	// a finished subagent is beyond the main's reach
	code, out := srv.post(t, "/api/sessions/"+id+"/agents/a2/send", `{"text":"one more scan"}`, nil)
	if code != 409 || !strings.Contains(out, "finished") {
		t.Errorf("a2: %d %s, want 409 naming finished", code, out)
	}
	// an id no agent of the session answers
	code, out = srv.post(t, "/api/sessions/"+id+"/agents/zz/send", `{"text":"hello"}`, nil)
	if code != 404 || !strings.Contains(out, "no such agent") {
		t.Errorf("zz: %d %s, want 404 no such agent", code, out)
	}
	// the main has its own route
	code, out = srv.post(t, "/api/sessions/"+id+"/agents/main/send", `{"text":"hello"}`, nil)
	if code != 400 || !strings.Contains(out, "answer route") {
		t.Errorf("main: %d %s, want 400 naming the answer route", code, out)
	}
	// an empty text
	code, out = srv.post(t, "/api/sessions/"+id+"/agents/a1/send", `{"text":""}`, nil)
	if code != 400 || !strings.Contains(out, "text is required") {
		t.Errorf("an empty text: %d %s, want 400 naming it", code, out)
	}
	// a session the registry does not know
	code, out = srv.post(t, "/api/sessions/s-none/agents/a1/send", `{"text":"hello"}`, nil)
	if code != 404 || !strings.Contains(out, "no such session") {
		t.Errorf("an unknown session: %d %s, want 404 naming it", code, out)
	}
}

func TestASendDemandsACredential(t *testing.T) {
	srv := startServer(t, nil)
	code, _, body, err := srv.request(context.Background(), http.MethodPost,
		"/api/sessions/s-1/agents/a1/send", `{"text":"hello"}`,
		map[string]string{"Content-Type": "application/json"})
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if code != 401 {
		t.Errorf("POST without a token: %d %s, want 401", code, body)
	}
}
