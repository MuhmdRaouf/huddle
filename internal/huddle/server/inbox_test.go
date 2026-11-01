package server

// The hub's own-machine delivery: an owner's answer posted straight through the session's inbox
// socket (route inbox), held while the session's claude keeps the words for the owner's approval
// in its terminal (route held), and the next prompt left as it is when the socket is dead.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
)

// inboxListener opens a unix socket at a short path — the package's TMPDIR sits too deep for a
// socket path under the 104-byte limit, so the folder comes straight from /tmp — and answers it,
// closed with the test.
func inboxListener(t *testing.T) (path string, ln net.Listener) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hi")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path = filepath.Join(dir, "inbox.sock")
	ln, err = net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen on %s: %v", path, err)
	}
	t.Cleanup(func() { ln.Close() })
	return path, ln
}

// inboxLines accepts one connection on ln and answers every line the client wrote, read to its
// close; a failed read or accept answers nil.
func inboxLines(t *testing.T, ln net.Listener) <-chan []byte {
	t.Helper()
	got := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			got <- nil
			return
		}
		defer conn.Close()
		data, err := io.ReadAll(conn)
		if err != nil {
			got <- nil
			return
		}
		got <- data
	}()
	return got
}

// inboxServer starts a child server whose Claude config dir the test owns, and answers both.
func inboxServer(t *testing.T) (*childServer, string) {
	t.Helper()
	config := filepath.Join(t.TempDir(), "claude")
	srv := startServer(t, map[string]string{"CLAUDE_CONFIG_DIR": config})
	return srv, config
}

// reportInboxSession lands one report for a live session on this machine naming its inbox
// socket, as the SessionStart hook's report does.
func reportInboxSession(t *testing.T, srv *childServer, id, socket string, pid int) {
	t.Helper()
	body := fmt.Sprintf(`{"session_id":%q,"channel":"shop","member":"api","pid":%d,"inbox_socket":%q,"last_event":"UserPromptSubmit","last_event_at":%q}`,
		id, pid, socket, isoAgo(2*time.Second))
	if code, out := srv.post(t, "/api/registry/report", body, nil); code != 200 {
		t.Fatalf("report %s: %d %s", id, code, out)
	}
}

// writeInboxSessions lands Claude Code's sessions file for one live pid, the file the hub reads
// to learn whether the session's claude holds a turn for the owner's approval.
func writeInboxSessions(t *testing.T, configDir string, pid int, body string) {
	t.Helper()
	dir := filepath.Join(configDir, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("sessions dir: %v", err)
	}
	file := filepath.Join(dir, fmt.Sprintf("%d.json", pid))
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
}

func TestAnAnswerGoesThroughTheInboxWhenTheSessionHasOne(t *testing.T) {
	srv, config := inboxServer(t)
	sock, ln := inboxListener(t)
	lines := inboxLines(t, ln)

	const id = "5cfe8626-2c6f-4f0d-8ff8-4f7c4a3b9c01"
	pid := os.Getpid()
	reportInboxSession(t, srv, id, sock, pid)
	writeInboxSessions(t, config, pid,
		fmt.Sprintf(`{"sessionId":%q,"pid":%d,"messagingSocketPath":%q,"status":"idle"}`, id, pid, sock))

	// the messaging token the SessionStart hook keeps for the session
	keys := home.KeyFiles{Dir: filepath.Join(srv.home, "keys")}
	if err := keys.Set(context.Background(), "cc-messaging:"+id, "tok-7d21"); err != nil {
		t.Fatalf("set the token: %v", err)
	}

	code, out := srv.post(t, "/api/sessions/"+id+"/answer", `{"text":"ship the fix"}`, nil)
	if code != 200 {
		t.Fatalf("answer = %d %s", code, out)
	}
	resp := bodyMap(t, out)
	if resp["route"] != "inbox" {
		t.Fatalf("the answer's route = %v (%s), want inbox", resp["route"], out)
	}

	// the wire carries the auth line with the session's token, then the owner's words as one
	// user turn
	data := <-lines
	if data == nil {
		t.Fatal("nothing reached the inbox socket")
	}
	got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(got) != 2 {
		t.Fatalf("the wire carries %d lines (%q), want the auth line then the user line", len(got), data)
	}
	var auth struct {
		Type  string `json:"type"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(got[0]), &auth); err != nil {
		t.Fatalf("decode the first line %q: %v", got[0], err)
	}
	if auth.Type != "auth" || auth.Token != "tok-7d21" {
		t.Errorf("the first line = %q, want the auth line naming the session's token", got[0])
	}
	var user struct {
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
		Message   struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(got[1]), &user); err != nil {
		t.Fatalf("decode the second line %q: %v", got[1], err)
	}
	if user.Type != "user" || user.SessionID != id || user.Message.Role != "user" ||
		user.Message.Content != "From the owner via huddle: ship the fix" {
		t.Errorf("the second line = %+v, want the owner's words as one user turn", user)
	}

	rows := messageRows(t, srv)
	if len(rows) != 1 {
		t.Fatalf("messages = %d rows, want one", len(rows))
	}
	if rows[0]["state"] != "delivered" || rows[0]["route"] != "inbox" {
		t.Errorf("the message row = %v, want delivered on the inbox route", rows[0])
	}
}

// answerOnInbox reports a live session with an inbox socket and, when status is not empty, a
// sessions file in that mood, then answers it and returns the answer's decoded body.
func answerOnInbox(t *testing.T, srv *childServer, config, id, status, lastEvent string) map[string]any {
	t.Helper()
	sock, ln := inboxListener(t)
	lines := inboxLines(t, ln)
	pid := os.Getpid()
	body := fmt.Sprintf(`{"session_id":%q,"channel":"shop","member":"api","pid":%d,"inbox_socket":%q,"last_event":%q,"last_event_at":%q}`,
		id, pid, sock, lastEvent, isoAgo(2*time.Second))
	if code, out := srv.post(t, "/api/registry/report", body, nil); code != 200 {
		t.Fatalf("report %s: %d %s", id, code, out)
	}
	if status != "" {
		writeInboxSessions(t, config, pid,
			fmt.Sprintf(`{"sessionId":%q,"pid":%d,"messagingSocketPath":%q,"status":%q}`, id, pid, sock, status))
	}
	code, out := srv.post(t, "/api/sessions/"+id+"/answer", `{"text":"carry on"}`, nil)
	if code != 200 {
		t.Fatalf("answer = %d %s", code, out)
	}
	if data := <-lines; data == nil {
		t.Fatal("nothing reached the inbox socket")
	}
	return bodyMap(t, out)
}

func TestAnAnswerToABusySessionArrivesBetweenToolCalls(t *testing.T) {
	srv, config := inboxServer(t)
	resp := answerOnInbox(t, srv, config, "aaaaaaaa-0000-4000-8000-000000000001", "busy", "UserPromptSubmit")
	if resp["route"] != "inbox" || resp["arrival"] != "between_calls" {
		t.Fatalf("the answer = %v, want route inbox and arrival between_calls", resp)
	}
}

func TestAnAnswerToAnIdleSessionStartsATurn(t *testing.T) {
	srv, config := inboxServer(t)
	resp := answerOnInbox(t, srv, config, "aaaaaaaa-0000-4000-8000-000000000002", "idle", "Stop")
	if resp["route"] != "inbox" || resp["arrival"] != "started_turn" {
		t.Fatalf("the answer = %v, want route inbox and arrival started_turn", resp)
	}
}

func TestNoSessionsFileFallsBackToTheRowState(t *testing.T) {
	for _, tc := range []struct {
		name, id, lastEvent, want string
	}{
		{"an idle row", "aaaaaaaa-0000-4000-8000-000000000003", "Stop", "started_turn"},
		{"a working row", "aaaaaaaa-0000-4000-8000-000000000004", "UserPromptSubmit", "between_calls"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, config := inboxServer(t)
			resp := answerOnInbox(t, srv, config, tc.id, "", tc.lastEvent)
			if resp["route"] != "inbox" || resp["arrival"] != tc.want {
				t.Fatalf("the answer = %v, want route inbox and arrival %s", resp, tc.want)
			}
		})
	}
}

func TestSendReturnsTheArrivalPerSession(t *testing.T) {
	srv, config := inboxServer(t)
	// the sessions file is keyed by pid, so the two sessions take two pids of their own
	sessions := []struct {
		id, status string
		pid        int
	}{
		{"aaaaaaaa-0000-4000-8000-000000000005", "busy", os.Getpid()},
		{"aaaaaaaa-0000-4000-8000-000000000006", "idle", startClaudeLike(t)},
	}
	for _, one := range sessions {
		sock, ln := inboxListener(t)
		inboxLines(t, ln)
		reportInboxSession(t, srv, one.id, sock, one.pid)
		writeInboxSessions(t, config, one.pid,
			fmt.Sprintf(`{"sessionId":%q,"pid":%d,"messagingSocketPath":%q,"status":%q}`, one.id, one.pid, sock, one.status))
	}

	body := fmt.Sprintf(`{"sessions":[%q,%q],"text":"run the release"}`, sessions[0].id, sessions[1].id)
	code, out := srv.post(t, "/api/send", body, nil)
	if code != 200 {
		t.Fatalf("send = %d %s", code, out)
	}
	sent, _ := bodyMap(t, out)["sent"].([]any)
	if len(sent) != 2 {
		t.Fatalf("sent = %v, want both sessions", sent)
	}
	for i, want := range []string{"between_calls", "started_turn"} {
		one, _ := sent[i].(map[string]any)
		if one["session"] != sessions[i].id || one["route"] != "inbox" || one["arrival"] != want {
			t.Errorf("sent[%d] = %v, want session %s on route inbox arriving %s", i, one, sessions[i].id, want)
		}
	}
}

func TestNonInboxRoutesCarryNoArrival(t *testing.T) {
	srv, _ := inboxServer(t)
	const id = "aaaaaaaa-0000-4000-8000-000000000007"
	reportInboxSession(t, srv, id, filepath.Join(t.TempDir(), "gone.sock"), os.Getpid())
	code, out := srv.post(t, "/api/sessions/"+id+"/answer", `{"text":"anyone?"}`, nil)
	if code != 200 {
		t.Fatalf("answer = %d %s", code, out)
	}
	resp := bodyMap(t, out)
	arrival, present := resp["arrival"]
	if resp["route"] != "next_prompt" || !present || arrival != "" {
		t.Fatalf("the answer = %v, want route next_prompt and an empty arrival", resp)
	}
}

func TestAHeldInboxMessageSaysApproveInTheTerminal(t *testing.T) {
	srv, config := inboxServer(t)
	sock, ln := inboxListener(t)
	lines := inboxLines(t, ln)

	const id = "11111111-2222-3333-4444-555555555555"
	pid := os.Getpid()
	reportInboxSession(t, srv, id, sock, pid)
	// the session's claude holds the turn for the owner's approval in its terminal
	writeInboxSessions(t, config, pid,
		fmt.Sprintf(`{"sessionId":%q,"pid":%d,"messagingSocketPath":%q,"status":"waiting"}`, id, pid, sock))
	keys := home.KeyFiles{Dir: filepath.Join(srv.home, "keys")}
	if err := keys.Set(context.Background(), "cc-messaging:"+id, "tok-4c08"); err != nil {
		t.Fatalf("set the token: %v", err)
	}

	code, out := srv.post(t, "/api/sessions/"+id+"/answer", `{"text":"one more thing"}`, nil)
	if code != 200 {
		t.Fatalf("answer = %d %s", code, out)
	}
	resp := bodyMap(t, out)
	if resp["route"] != "held" {
		t.Fatalf("the answer's route = %v (%s), want held", resp["route"], out)
	}
	if resp["arrival"] != "held" {
		t.Errorf("the answer's arrival = %v (%s), want held", resp["arrival"], out)
	}
	if data := <-lines; data == nil || !strings.Contains(string(data), `"type":"auth"`) {
		t.Errorf("the wire = %q, want the post that waits on the approval", data)
	}

	rows := messageRows(t, srv)
	if len(rows) != 1 || rows[0]["state"] != "held" || rows[0]["route"] != "inbox" {
		t.Fatalf("the message row = %v, want held on the inbox route", rows)
	}
	// a held message is out of the listen hook's hands: the claude shows it already
	code, out = srv.post(t, "/api/sessions/"+id+"/messages", "", nil)
	if code != 200 {
		t.Fatalf("messages = %d %s", code, out)
	}
	taken := bodyMap(t, out)
	if list, _ := taken["messages"].([]any); len(list) != 0 {
		t.Errorf("the listen hook took %v, want nothing: the claude holds the message", list)
	}
}

func TestTheHoldWindowEndsUnderAFixedClock(t *testing.T) {
	registry, _ := openRegistry(t)
	sock, ln := inboxListener(t)
	lines := inboxLines(t, ln)

	const id = "44444444-5555-6666-7777-888888888888"
	pid := os.Getpid()
	if err := registry.Report(bg, hub.Report{
		SessionID: id, Pid: pid, InboxSocket: sock,
		LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T08:59:58.000Z",
	}); err != nil {
		t.Fatalf("report: %v", err)
	}
	config := t.TempDir()
	s := &server{
		registry: registry,
		now:      fixedClock(),
		o: Options{
			Home:    t.TempDir(),
			Environ: []string{"CLAUDE_CONFIG_DIR=" + config},
			Secrets: func(context.Context, home.Home) home.SecretStore {
				return home.KeyFiles{Dir: filepath.Join(t.TempDir(), "keys")}
			},
		},
	}
	// no sessions file under config: nothing ever says the claude holds the turn, so the read
	// only ends when the window itself ends
	in := hub.MessageIn{SessionID: id, FromKind: hub.FromOwner, Text: "still with me?"}
	msgID, err := registry.QueueMessage(bg, in, "2026-10-16T09:00:00.000Z")
	if err != nil {
		t.Fatalf("queue the message: %v", err)
	}

	route := make(chan string, 1)
	go func() { route <- s.postInbox(bg, msgID, in).Route }()
	select {
	case got := <-route:
		if got != hub.RouteInbox {
			t.Fatalf("postInbox route = %q, want inbox", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("postInbox never came back: the hold window runs on the server's fixed clock")
	}
	if data := <-lines; data == nil {
		t.Fatal("nothing reached the inbox socket")
	}
}

func TestADeadInboxFallsBackToTheNextPrompt(t *testing.T) {
	srv, _ := inboxServer(t)

	const id = "22222222-3333-4444-5555-666666666666"
	dead := filepath.Join(t.TempDir(), "gone.sock")
	reportInboxSession(t, srv, id, dead, os.Getpid())

	code, out := srv.post(t, "/api/sessions/"+id+"/answer", `{"text":"still there?"}`, nil)
	if code != 200 {
		t.Fatalf("answer = %d %s", code, out)
	}
	resp := bodyMap(t, out)
	if resp["route"] != "next_prompt" {
		t.Fatalf("the answer's route = %v (%s), want next_prompt", resp["route"], out)
	}

	rows := messageRows(t, srv)
	if len(rows) != 1 || rows[0]["state"] != "pending" {
		t.Fatalf("the message row = %v, want it still waiting for the listen hook", rows)
	}
	code, out = srv.post(t, "/api/sessions/"+id+"/messages", "", nil)
	if code != 200 {
		t.Fatalf("messages = %d %s", code, out)
	}
	taken := bodyMap(t, out)
	list, _ := taken["messages"].([]any)
	if len(list) != 1 || list[0] != "From the owner via huddle: still there?" {
		t.Errorf("the listen hook took %v, want the one owner message", list)
	}
}
