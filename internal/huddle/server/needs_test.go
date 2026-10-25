package server

// The answer, send and needs-you routes (SPEC 6.1, 6.3, 6.4): items grow from reports, the owner
// answers and sends through them, a message reaches its session by channel push while the
// session's bridge acks one (route now) and by the next prompt otherwise, and pause, resume,
// snooze and dismiss reach the channels' own controls.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
)

// reportSession lands one report for a session in a channel, as the hooks would.
func reportSession(t *testing.T, srv *childServer, id, channel, member, event string) {
	t.Helper()
	body := fmt.Sprintf(`{"session_id":%q,"channel":%q,"member":%q,"last_event":%q,"last_event_at":%q}`,
		id, channel, member, event, isoAgo(2*time.Second))
	if event == "Notification" {
		body = fmt.Sprintf(`{"session_id":%q,"channel":%q,"member":%q,"last_event":"Notification","last_event_at":%q,"notification":{"type":"elicitation_dialog","message":"Pick a database"}}`,
			id, channel, member, isoAgo(2*time.Second))
	}
	if code, out := srv.post(t, "/api/registry/report", body, nil); code != 200 {
		t.Fatalf("report %s %s: %d %s", id, event, code, out)
	}
}

// needsItems reads the needs list as id → (kind, state).
func needsItems(t *testing.T, srv *childServer) map[string][2]string {
	t.Helper()
	code, _, out := srv.get(t, "/api/needs", nil)
	if code != 200 {
		t.Fatalf("needs = %d %s", code, out)
	}
	parsed := bodyMap(t, out)
	rows, _ := parsed["items"].([]any)
	items := map[string][2]string{}
	for _, row := range rows {
		if m, ok := row.(map[string]any); ok {
			id := fmt.Sprintf("%v", m["id"])
			kind, _ := m["kind"].(string)
			state, _ := m["state"].(string)
			items[id] = [2]string{kind, state}
		}
	}
	return items
}

// theQuestionID answers the one question item's id.
func theQuestionID(t *testing.T, srv *childServer) string {
	t.Helper()
	code, _, out := srv.get(t, "/api/needs", nil)
	if code != 200 {
		t.Fatalf("needs = %d %s", code, out)
	}
	parsed := bodyMap(t, out)
	rows, _ := parsed["items"].([]any)
	if len(rows) != 1 {
		t.Fatalf("items = %v, want the one question", rows)
	}
	item, _ := rows[0].(map[string]any)
	return fmt.Sprintf("%v", item["id"])
}

// messageRows reads the messages table straight from the home's hub.db.
func messageRows(t *testing.T, srv *childServer) []map[string]any {
	t.Helper()
	db, err := store.SQLite(context.Background(), srv.home+"/hub.db")
	if err != nil {
		t.Fatalf("open the hub db: %v", err)
	}
	defer db.Close()
	rows, err := db.All(context.Background(),
		"SELECT session_id, from_kind, text, state, route FROM messages ORDER BY id")
	if err != nil {
		t.Fatalf("read messages: %v", err)
	}
	out := []map[string]any{}
	for _, row := range rows {
		out = append(out, map[string]any{
			"session_id": row["session_id"], "from_kind": row["from_kind"],
			"text": row["text"], "state": row["state"], "route": row["route"],
		})
	}
	return out
}

func TestQuestionAndTurnItemsGrowFromReports(t *testing.T) {
	srv := startServer(t, nil)
	reportSession(t, srv, "s-q", "", "", "Notification")
	reportSession(t, srv, "s-t", "", "", "UserPromptSubmit")
	reportSession(t, srv, "s-t", "", "", "Stop")
	items := needsItems(t, srv)
	if len(items) != 2 {
		t.Fatalf("items = %v, want a question and a turn", items)
	}
	kinds := map[string]bool{}
	for _, pair := range items {
		kinds[pair[0]] = true
	}
	if !kinds["question"] || !kinds["turn"] {
		t.Fatalf("items = %v, want a question and a turn", items)
	}
	// the session with the open question reads needs-you; the finished turn's does not
	code, _, out := srv.get(t, "/api/registry/sessions/s-q", nil)
	if code != 200 || bodyMap(t, out)["state"] != "needs-you" {
		t.Errorf("s-q = %d %s, want needs-you", code, out)
	}
	code, _, out = srv.get(t, "/api/registry/sessions/s-t", nil)
	if code != 200 || bodyMap(t, out)["state"] != "idle" {
		t.Errorf("s-t = %d %s, want idle (a turn item alone is not needs-you)", code, out)
	}
	// opening the detail answers the turn item
	_ = needsItems(t, srv) // stable ids first
	code, _, _ = srv.get(t, "/api/registry/sessions/s-t", nil)
	if code != 200 {
		t.Fatalf("the detail = %d", code)
	}
	for id, pair := range needsItems(t, srv) {
		if pair[0] == "turn" {
			t.Errorf("the turn item %s stayed open after the owner opened the session", id)
		}
	}
}

func TestOpeningASessionStreamsItsAnsweredTurnItems(t *testing.T) {
	srv := startServer(t, nil)
	reportSession(t, srv, "s-t", "", "", "UserPromptSubmit")
	reportSession(t, srv, "s-t", "", "", "Stop")
	reportSession(t, srv, "s-q", "", "", "Notification")
	turns := map[string]bool{}
	for id, pair := range needsItems(t, srv) {
		if pair[0] == "turn" {
			turns[id] = true
		}
	}
	if len(turns) != 1 {
		t.Fatalf("the open turn items = %v, want one", turns)
	}
	frames := openRegistryLive(t, srv)
	nextRegistryEvent(t, frames, "hello")
	// the owner opens the session twice: the first read answers the turn item, the second has
	// nothing left to answer; a report of another session after both marks the stream's end
	for range 2 {
		if code, _, out := srv.get(t, "/api/registry/sessions/s-t", nil); code != 200 {
			t.Fatalf("the detail = %d %s", code, out)
		}
	}
	reportSession(t, srv, "s-mark", "", "", "UserPromptSubmit")
	answered := map[string]int{}
	var restated []string
	for {
		var m struct {
			Type string `json:"type"`
			Data struct {
				ID        json.Number `json:"id"`
				SessionID string      `json:"session"`
				Kind      string      `json:"kind"`
				State     string      `json:"state"`
				Mark      string      `json:"session_id"`
			} `json:"data"`
		}
		var row struct {
			Data struct {
				SessionID string `json:"session_id"`
				State     string `json:"state"`
			} `json:"data"`
		}
		frame := waitForAny(t, frames)
		if err := json.Unmarshal([]byte(frame), &m); err != nil {
			t.Fatalf("a frame = %s: %v", frame, err)
		}
		if m.Type == "registry.session" && m.Data.Mark == "s-mark" {
			break
		}
		if m.Type == "registry.session" && m.Data.Mark == "s-t" {
			if err := json.Unmarshal([]byte(frame), &row); err != nil {
				t.Fatalf("a row frame = %s: %v", frame, err)
			}
			restated = append(restated, row.Data.State)
		}
		if m.Type != "needs.item" {
			continue
		}
		if m.Data.SessionID != "s-t" || m.Data.Kind != "turn" || m.Data.State != "answered" {
			t.Fatalf("the needs event = %s, want s-t's turn item answered", frame)
		}
		answered[m.Data.ID.String()]++
	}
	for id := range turns {
		if answered[id] != 1 {
			t.Errorf("the turn item %s streamed %d times, want once", id, answered[id])
		}
	}
	if len(answered) != len(turns) {
		t.Errorf("the answered events = %v, want the turn items %v alone", answered, turns)
	}
	// the session's row follows once, out of needs-you: its turn no longer waits
	if len(restated) != 1 || restated[0] == "needs-you" {
		t.Errorf("s-t's restated rows = %v, want one that no longer reads needs-you", restated)
	}
}

// waitForAny answers the stream's next frame, failing the test when none arrives within 5 s.
func waitForAny(t *testing.T, frames <-chan string) string {
	t.Helper()
	select {
	case line, open := <-frames:
		if !open {
			t.Fatal("the stream closed early")
		}
		return line
	case <-time.After(5 * time.Second):
		t.Fatal("no frame within 5 s")
	}
	return ""
}

func TestTheAnswerRouteQueuesAClosesTheQuestionAndNamesItsRoute(t *testing.T) {
	srv := startServer(t, nil)
	reportSession(t, srv, "s-1", "", "", "Notification")
	code, out := srv.post(t, "/api/sessions/s-1/answer", `{"text":"ship it"}`, nil)
	if code != 200 {
		t.Fatalf("answer = %d %s", code, out)
	}
	answer := bodyMap(t, out)
	if answer["route"] != "next_prompt" {
		t.Errorf("route = %v, want next_prompt with no bridge there", answer["route"])
	}
	if answer["ok"] != true {
		t.Errorf("answer = %v", answer)
	}
	// the question is answered, and the message waits with the owner's exact header
	if items := needsItems(t, srv); len(items) != 0 {
		t.Fatalf("the answered question stayed: %v", items)
	}
	rows := messageRows(t, srv)
	if len(rows) != 1 {
		t.Fatalf("messages = %v, want the one queued", rows)
	}
	if rows[0]["session_id"] != "s-1" || rows[0]["from_kind"] != "owner" || rows[0]["state"] != "pending" {
		t.Errorf("the queued row = %v", rows[0])
	}
	// the limits answer 400 with their reason
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"no text", `{"text":""}`, "1 to 4000"},
		{"over 4000", `{"text":"` + strings.Repeat("x", 4001) + `"}`, "4000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out := srv.post(t, "/api/sessions/s-1/answer", tc.body, nil)
			if code != 400 || !strings.Contains(out, tc.want) {
				t.Errorf("answer = %d %s, want 400 naming %q", code, out, tc.want)
			}
		})
	}
	if code, out := srv.post(t, "/api/sessions/no-such/answer", `{"text":"hi"}`, nil); code != 404 {
		t.Errorf("an answer to an unknown session = %d %s, want 404", code, out)
	}
}

func TestTheSendRouteReachesOneToTwentySessions(t *testing.T) {
	srv := startServer(t, nil)
	for _, id := range []string{"s-1", "s-2", "s-3"} {
		reportSession(t, srv, id, "", "", "UserPromptSubmit")
	}
	code, out := srv.post(t, "/api/send", `{"sessions":["s-1","s-3"],"text":"run the release"}`, nil)
	if code != 200 {
		t.Fatalf("send = %d %s", code, out)
	}
	parsed := bodyMap(t, out)
	sent, _ := parsed["sent"].([]any)
	if len(sent) != 2 {
		t.Fatalf("sent = %v, want both sessions", sent)
	}
	first, _ := sent[0].(map[string]any)
	if first["session"] != "s-1" || first["route"] != "next_prompt" {
		t.Errorf("sent[0] = %v", first)
	}
	rows := messageRows(t, srv)
	if len(rows) != 2 || rows[0]["session_id"] != "s-1" || rows[1]["session_id"] != "s-3" {
		t.Fatalf("messages = %v, want one row per session", rows)
	}
	for _, tc := range []struct {
		name string
		body string
		want string
		code int
	}{
		{"no sessions", `{"sessions":[],"text":"hi"}`, "1 to 20", 400},
		{"twenty-one", `{"sessions":["` + strings.Join(repeatIDs("s", 21), `","`) + `"],"text":"hi"}`, "1 to 20", 400},
		{"no text", `{"sessions":["s-1"],"text":""}`, "1 to 4000", 400},
		{"an unknown session", `{"sessions":["s-1","no-such"],"text":"hi"}`, "no session no-such", 404},
		{"not an array", `{"sessions":"s-1","text":"hi"}`, "array", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out := srv.post(t, "/api/send", tc.body, nil)
			if code != tc.code || !strings.Contains(out, tc.want) {
				t.Errorf("send = %d %s, want %d naming %q", code, out, tc.code, tc.want)
			}
		})
	}
}

// auditRows reads the audit log as row maps, newest first.
func auditRows(t *testing.T, srv *childServer) []map[string]any {
	t.Helper()
	code, _, out := srv.get(t, "/api/audit", nil)
	if code != 200 {
		t.Fatalf("audit = %d %s", code, out)
	}
	rows, _ := bodyMap(t, out)["entries"].([]any)
	parsed := []map[string]any{}
	for _, row := range rows {
		if m, ok := row.(map[string]any); ok {
			parsed = append(parsed, m)
		}
	}
	return parsed
}

// auditRowsOf answers the audit's rows of one action.
func auditRowsOf(t *testing.T, srv *childServer, action string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, row := range auditRows(t, srv) {
		if row["action"] == action {
			out = append(out, row)
		}
	}
	return out
}

func TestTheAnswerRouteWritesOneAuditRow(t *testing.T) {
	srv := startServer(t, nil)
	reportSession(t, srv, "s-1", "", "", "Notification")
	if code, out := srv.post(t, "/api/sessions/s-1/answer", `{"text":"ship it"}`, nil); code != 200 {
		t.Fatalf("answer = %d %s", code, out)
	}
	rows := auditRows(t, srv)
	if len(rows) != 1 {
		t.Fatalf("the audit = %d rows, want exactly the answer's one: %v", len(rows), rows)
	}
	if rows[0]["action"] != "answer" || rows[0]["actor"] != "owner" || rows[0]["target"] != "s-1" {
		t.Errorf("the row = %v, want the owner's answer on s-1", rows[0])
	}
}

func TestTheMessagesRouteWritesOneAuditRow(t *testing.T) {
	srv := startServer(t, nil)
	reportSession(t, srv, "s-1", "", "", "UserPromptSubmit")
	if code, out := srv.post(t, "/api/sessions/s-1/answer", `{"text":"ship it"}`, nil); code != 200 {
		t.Fatalf("answer = %d %s", code, out)
	}
	if code, out := srv.post(t, "/api/sessions/s-1/messages", `{}`, nil); code != 200 {
		t.Fatalf("messages = %d %s", code, out)
	}
	rows := auditRowsOf(t, srv, "messages")
	if len(rows) != 1 {
		t.Fatalf("the audit holds %d messages rows, want exactly one: %v", len(rows), rows)
	}
	if rows[0]["actor"] != "owner" || rows[0]["target"] != "s-1" {
		t.Errorf("the row = %v, want the owner's take on s-1", rows[0])
	}
}

func TestThePauseRouteWritesOneAuditRow(t *testing.T) {
	srv := startServer(t, nil)
	if code, out := srv.post(t, "/api/channels", `{"name":"reg"}`, nil); code != 200 {
		t.Fatalf("create the channel: %d %s", code, out)
	}
	if code, out := srv.op(t, "reg", "join", "api", "{}"); code != 200 {
		t.Fatalf("join: %d %v", code, out)
	}
	reportSession(t, srv, "s-p", "reg", "api", "UserPromptSubmit")
	if code, out := srv.post(t, "/api/sessions/s-p/pause", `{"why":"waiting on the owner"}`, nil); code != 200 {
		t.Fatalf("pause = %d %s", code, out)
	}
	rows := auditRows(t, srv)
	if len(rows) != 1 {
		t.Fatalf("the audit = %d rows, want exactly the pause's one: %v", len(rows), rows)
	}
	if rows[0]["action"] != "pause" || rows[0]["actor"] != "owner" || rows[0]["target"] != "s-p" {
		t.Errorf("the row = %v, want the owner's pause on s-p", rows[0])
	}
}

func TestTheResumeRouteWritesOneAuditRow(t *testing.T) {
	srv := startServer(t, nil)
	if code, out := srv.post(t, "/api/channels", `{"name":"reg"}`, nil); code != 200 {
		t.Fatalf("create the channel: %d %s", code, out)
	}
	if code, out := srv.op(t, "reg", "join", "api", "{}"); code != 200 {
		t.Fatalf("join: %d %v", code, out)
	}
	reportSession(t, srv, "s-p", "reg", "api", "UserPromptSubmit")
	if code, out := srv.post(t, "/api/sessions/s-p/pause", `{}`, nil); code != 200 {
		t.Fatalf("pause = %d %s", code, out)
	}
	if code, out := srv.post(t, "/api/sessions/s-p/resume", `{}`, nil); code != 200 {
		t.Fatalf("resume = %d %s", code, out)
	}
	rows := auditRowsOf(t, srv, "resume")
	if len(rows) != 1 {
		t.Fatalf("the audit holds %d resume rows, want exactly one: %v", len(rows), rows)
	}
	if rows[0]["actor"] != "owner" || rows[0]["target"] != "s-p" {
		t.Errorf("the row = %v, want the owner's resume on s-p", rows[0])
	}
}

func TestTheSendRouteWritesOneRowPerSession(t *testing.T) {
	srv := startServer(t, nil)
	for _, id := range []string{"s-1", "s-2", "s-3"} {
		reportSession(t, srv, id, "", "", "UserPromptSubmit")
	}
	if code, out := srv.post(t, "/api/send", `{"sessions":["s-1","s-3"],"text":"run the release"}`, nil); code != 200 {
		t.Fatalf("send = %d %s", code, out)
	}
	rows := auditRowsOf(t, srv, "send")
	if len(rows) != 2 {
		t.Fatalf("the audit holds %d send rows, want exactly one per session: %v", len(rows), rows)
	}
	if rows[0]["actor"] != "owner" || rows[0]["target"] != "s-3" || rows[1]["target"] != "s-1" {
		t.Errorf("the rows = %v, want one row per session, newest first", rows)
	}
}

// repeatIDs answers n copies of prefix plus its number.
func repeatIDs(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s-%d", prefix, i)
	}
	return out
}

// openLive opens one channel live stream as a member, the way the MCP bridge does, and answers
// its frames on a channel.
func openLive(t *testing.T, srv *childServer, channel, as string) <-chan string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		srv.base+"/api/c/"+channel+"/live?as="+as, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("x-huddle-token", srv.token)
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	frames := make(chan string, 64)
	go func() {
		defer close(frames)
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			if line := scanner.Text(); strings.HasPrefix(line, "data: ") {
				frames <- strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	waitFor(t, frames, "hello")
	return frames
}

// waitFor reads frames until one parses as the wanted type.
func waitFor(t *testing.T, frames <-chan string, want string) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		var frame string
		select {
		case line, open := <-frames:
			if !open {
				t.Fatal("the stream closed early")
			}
			frame = line
		case <-deadline:
			t.Fatalf("no %s frame within 5 s", want)
		}
		var m struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(frame), &m); err != nil || m.Type != want {
			continue
		}
		return frame
	}
}

// ackPush posts the bridge's push ack, as the bridge does once its stream is live.
func ackPush(t *testing.T, srv *childServer, channel, as string) {
	t.Helper()
	body := fmt.Sprintf(`{"channel":%q,"as":%q}`, channel, as)
	if code, out := srv.post(t, "/api/registry/push-ack", body, nil); code != 200 {
		t.Fatalf("push-ack = %d %s", code, out)
	}
}

func TestAMessageIsPushedToTheBridgeThatAcked(t *testing.T) {
	srv := startServer(t, nil)
	if code, out := srv.post(t, "/api/channels", `{"name":"reg"}`, nil); code != 200 {
		t.Fatalf("create the channel: %d %s", code, out)
	}
	if code, out := srv.op(t, "reg", "join", "api", "{}"); code != 200 {
		t.Fatalf("join: %d %v", code, out)
	}
	reportSession(t, srv, "s-live", "reg", "api", "UserPromptSubmit")
	// the bridge: one live stream as the member, and its ack
	frames := openLive(t, srv, "reg", "api")
	ackPush(t, srv, "reg", "api")
	code, _, out := srv.get(t, "/api/registry/sessions/s-live", nil)
	if code != 200 || bodyMap(t, out)["delivery_route"] != "now" {
		t.Fatalf("the row = %d %s, want delivery_route now", code, out)
	}
	code, out = srv.post(t, "/api/sessions/s-live/answer", `{"text":"ship it"}`, nil)
	if code != 200 {
		t.Fatalf("answer = %d %s", code, out)
	}
	if route := bodyMap(t, out)["route"]; route != "now" {
		t.Fatalf("route = %v, want now", route)
	}
	// the pushed event reaches the bridge's stream, headed for the session
	frame := waitFor(t, frames, "event")
	var got struct {
		Data struct {
			To    *string `json:"to"`
			From  string  `json:"from"`
			Topic string  `json:"topic"`
			Msg   *string `json:"msg"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(frame), &got); err != nil {
		t.Fatalf("the event = %s: %v", frame, err)
	}
	if got.Data.From != "owner" || got.Data.To == nil || *got.Data.To != "api" || got.Data.Topic != "msg" ||
		got.Data.Msg == nil || *got.Data.Msg != "From the owner via huddle: ship it" {
		t.Errorf("the pushed event = %s", frame)
	}
	rows := messageRows(t, srv)
	if len(rows) != 1 || rows[0]["state"] != "delivered" || rows[0]["route"] != "now" {
		t.Errorf("the pushed row = %v", rows)
	}
}

func TestAPendingMessageIsPushedWhenTheBridgeAcks(t *testing.T) {
	srv := startServer(t, nil)
	if code, out := srv.post(t, "/api/channels", `{"name":"reg"}`, nil); code != 200 {
		t.Fatalf("create the channel: %d %s", code, out)
	}
	if code, out := srv.op(t, "reg", "join", "api", "{}"); code != 200 {
		t.Fatalf("join: %d %v", code, out)
	}
	reportSession(t, srv, "s-later", "reg", "api", "Stop")
	// answered while no bridge is there: the message waits for the next prompt
	if code, out := srv.post(t, "/api/sessions/s-later/answer", `{"text":"whenever"}`, nil); code != 200 ||
		bodyMap(t, out)["route"] != "next_prompt" {
		t.Fatalf("answer = %d %s, want next_prompt", code, out)
	}
	// the bridge comes: its ack promotes the waiting message to the now route
	frames := openLive(t, srv, "reg", "api")
	ackPush(t, srv, "reg", "api")
	frame := waitFor(t, frames, "event")
	if !strings.Contains(frame, "From the owner via huddle: whenever") {
		t.Errorf("the promoted event = %s", frame)
	}
	rows := messageRows(t, srv)
	if len(rows) != 1 || rows[0]["state"] != "delivered" || rows[0]["route"] != "now" {
		t.Errorf("the promoted row = %v", rows)
	}
}

func TestTheListenHookTakesTheSessionMessagesOnItsNextPrompt(t *testing.T) {
	srv := startServer(t, nil)
	reportSession(t, srv, "s-1", "", "", "UserPromptSubmit")
	if code, out := srv.post(t, "/api/sessions/s-1/answer", `{"text":"ship it"}`, nil); code != 200 {
		t.Fatalf("answer = %d %s", code, out)
	}
	code, out := srv.post(t, "/api/sessions/s-1/messages", `{}`, nil)
	if code != 200 {
		t.Fatalf("take = %d %s", code, out)
	}
	parsed := bodyMap(t, out)
	messages, _ := parsed["messages"].([]any)
	if len(messages) != 1 || messages[0] != "From the owner via huddle: ship it" {
		t.Fatalf("taken = %v, want the owner's header", messages)
	}
	// the take is once, and the row says which route delivered it
	if code, out = srv.post(t, "/api/sessions/s-1/messages", `{}`, nil); code != 200 {
		t.Fatalf("second take = %d %s", code, out)
	}
	if again, _ := bodyMap(t, out)["messages"].([]any); len(again) != 0 {
		t.Errorf("a second take answered %v", again)
	}
	rows := messageRows(t, srv)
	if len(rows) != 1 || rows[0]["state"] != "delivered" || rows[0]["route"] != "next_prompt" {
		t.Errorf("the taken row = %v", rows)
	}
}

func TestSnoozeAndDismiss(t *testing.T) {
	srv := startServer(t, nil)
	reportSession(t, srv, "s-1", "", "", "Notification")
	id := theQuestionID(t, srv)
	for _, tc := range []struct {
		name string
		body string
		code int
	}{
		{"fifteen minutes", `{"until":"15m"}`, 200},
		{"an hour", `{"until":"1h"}`, 200},
		{"tomorrow nine", `{"until":"tomorrow"}`, 200},
		{"an unknown span", `{"until":"2h"}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out := srv.post(t, "/api/needs/"+id+"/snooze", tc.body, nil)
			if code != tc.code {
				t.Fatalf("snooze = %d %s, want %d", code, out, tc.code)
			}
		})
	}
	// the snoozed item reads snoozed with its wake time
	code, _, out := srv.get(t, "/api/needs", nil)
	if code != 200 {
		t.Fatalf("needs = %d %s", code, out)
	}
	rows, _ := bodyMap(t, out)["items"].([]any)
	if len(rows) != 1 {
		t.Fatalf("items = %v, want the snoozed one still listed", rows)
	}
	item, _ := rows[0].(map[string]any)
	if item["state"] != "snoozed" || item["snoozed_until"] == "" {
		t.Errorf("the snoozed item = %v", item)
	}
	if code, out := srv.post(t, "/api/needs/"+id+"/dismiss", `{}`, nil); code != 200 {
		t.Fatalf("dismiss = %d %s", code, out)
	}
	if items := needsItems(t, srv); len(items) != 0 {
		t.Fatalf("the dismissed item stayed: %v", items)
	}
	if code, out := srv.post(t, "/api/needs/"+id+"/dismiss", `{}`, nil); code != 404 {
		t.Errorf("a second dismiss = %d %s, want 404", code, out)
	}
	if code, out := srv.post(t, "/api/needs/999999/snooze", `{"until":"15m"}`, nil); code != 404 {
		t.Errorf("an unknown item = %d %s, want 404", code, out)
	}
}

func TestPauseAndResumeReachTheChannelsControl(t *testing.T) {
	srv := startServer(t, nil)
	if code, out := srv.post(t, "/api/channels", `{"name":"reg"}`, nil); code != 200 {
		t.Fatalf("create the channel: %d %s", code, out)
	}
	if code, out := srv.op(t, "reg", "join", "api", "{}"); code != 200 {
		t.Fatalf("join: %d %v", code, out)
	}
	reportSession(t, srv, "s-p", "reg", "api", "UserPromptSubmit")
	controlOf := func() string {
		t.Helper()
		code, out := srv.op(t, "reg", "sessions", "owner", `{}`)
		if code != 200 {
			t.Fatalf("sessions = %d %v", code, out)
		}
		return mustJSON(t, out)
	}
	if code, out := srv.post(t, "/api/sessions/s-p/pause", `{"why":"waiting on the owner"}`, nil); code != 200 {
		t.Fatalf("pause = %d %s", code, out)
	}
	if sessions := controlOf(); !strings.Contains(sessions, `"pause"`) {
		t.Errorf("the channel still runs the member: %s", sessions)
	}
	if code, out := srv.post(t, "/api/sessions/s-p/resume", `{}`, nil); code != 200 {
		t.Fatalf("resume = %d %s", code, out)
	}
	if sessions := controlOf(); strings.Contains(sessions, `"pause"`) {
		t.Errorf("the member stayed paused: %s", sessions)
	}
	// a session in no channel cannot be paused there, and an unknown one is refused
	reportSession(t, srv, "s-free", "", "", "UserPromptSubmit")
	if code, out := srv.post(t, "/api/sessions/s-free/pause", `{}`, nil); code != 409 {
		t.Errorf("pausing a channel-less session = %d %s, want 409", code, out)
	}
	if code, out := srv.post(t, "/api/sessions/no-such/pause", `{}`, nil); code != 404 {
		t.Errorf("pausing an unknown session = %d %s, want 404", code, out)
	}
}

func TestBlockedItemsFollowTheChannelsTasks(t *testing.T) {
	srv := startServer(t, nil)
	if code, out := srv.post(t, "/api/channels", `{"name":"reg"}`, nil); code != 200 {
		t.Fatalf("create the channel: %d %s", code, out)
	}
	if code, out := srv.op(t, "reg", "join", "api", "{}"); code != 200 {
		t.Fatalf("join: %d %v", code, out)
	}
	reportSession(t, srv, "s-b", "reg", "api", "UserPromptSubmit")
	// api's task waits on a task the owner owns: it is blocked on the owner
	if code, out := srv.op(t, "reg", "task_create", "api", `{"id":"t-1","title":"Ship the checkout","owner":"api"}`); code != 200 {
		t.Fatalf("t-1: %d %v", code, out)
	}
	if code, out := srv.op(t, "reg", "task_create", "api", `{"id":"t-own","title":"Approve the design","owner":"owner"}`); code != 200 {
		t.Fatalf("t-own: %d %v", code, out)
	}
	if code, out := srv.op(t, "reg", "task_update", "api", `{"id":"t-1","after":["t-own"]}`); code != 200 {
		t.Fatalf("depend: %d %v", code, out)
	}
	if code, out := srv.op(t, "reg", "task_status", "api", `{"id":"t-1","status":"blocked","note":"waits on the owner"}`); code != 200 {
		t.Fatalf("block: %d %v", code, out)
	}
	code, _, out := srv.get(t, "/api/needs", nil)
	if code != 200 {
		t.Fatalf("needs = %d %s", code, out)
	}
	rows, _ := bodyMap(t, out)["items"].([]any)
	if len(rows) != 1 {
		t.Fatalf("items = %v, want the blocked task", rows)
	}
	item, _ := rows[0].(map[string]any)
	if item["kind"] != "blocked" || item["session"] != "s-b" {
		t.Errorf("the blocked item = %v", item)
	}
	if summary, _ := item["summary"].(string); !strings.Contains(summary, "t-1") || !strings.Contains(summary, "waits on you") {
		t.Errorf("summary = %q", summary)
	}
	// the session reads needs-you while its task waits
	code, _, out = srv.get(t, "/api/registry/sessions/s-b", nil)
	if code != 200 || bodyMap(t, out)["state"] != "needs-you" {
		t.Errorf("s-b = %d %s, want needs-you", code, out)
	}
	// the owner does their part: the task no longer waits on them, and the item withdraws
	if code, out := srv.op(t, "reg", "task_status", "owner", `{"id":"t-own","status":"done","note":"approved"}`); code != 200 {
		t.Fatalf("finish t-own: %d %v", code, out)
	}
	if items := needsItems(t, srv); len(items) != 0 {
		t.Fatalf("the unblocked task's item stayed: %v", items)
	}
}
