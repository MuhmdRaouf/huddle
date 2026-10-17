package server

// The permission flow's routes (SPEC 6.2, 6.6): the hook's item on the shared needs-you list,
// its long poll, and the owner's decision. The refusals each carry their own status: 409 for a
// cut input or a lost race, 403 for a session that is not the owner or an allow the machine
// will not make, 401 for a step-up that is due, 404 for an item that no longer waits.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
)

// permitPost posts one permission item as the hook does and answers its id.
func permitPost(t *testing.T, srv *childServer, session, tool, input string) int64 {
	t.Helper()
	body := fmt.Sprintf(`{"session_id":%q,"tool_name":%q,"tool_input":%s}`, session, tool, input)
	code, out := srv.post(t, "/api/needs", body, nil)
	if code != 200 {
		t.Fatalf("post the item: %d %s", code, out)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.ID == 0 {
		t.Fatalf("the created item's answer = %s (%v)", out, err)
	}
	return created.ID
}

// permitRow reads one item's row through the wait route, decided items answering at once.
func permitRow(t *testing.T, srv *childServer, id int64) map[string]any {
	t.Helper()
	code, _, out := srv.get(t, fmt.Sprintf("/api/needs/%d/wait?timeout=1", id), nil)
	if code != 200 {
		t.Fatalf("the item's wait = %d %s", code, out)
	}
	return bodyMap(t, out)
}

// permitConfig writes the home's [remote] switches the way an owner would.
func permitConfig(t *testing.T, srv *childServer, body string) {
	t.Helper()
	if err := os.MkdirAll(srv.home, 0o700); err != nil {
		t.Fatalf("the home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srv.home, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write the config: %v", err)
	}
}

// permitMember joins one member over HTTP and answers its credential, so a case can act as a
// session that is not the owner.
func permitMember(t *testing.T, srv *childServer) string {
	t.Helper()
	code, out := srv.post(t, "/api/tokens", `{}`, nil)
	if code != 200 {
		t.Fatalf("make an invite: %d %s", code, out)
	}
	var invite struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(out), &invite); err != nil || invite.Token == "" {
		t.Fatalf("the invite = %s (%v)", out, err)
	}
	code, out = srv.post(t, "/api/join", `{"token":"`+invite.Token+`","name":"worker"}`,
		map[string]string{"x-huddle-token": ""})
	if code != 200 {
		t.Fatalf("join: %d %s", code, out)
	}
	var joined struct {
		Credential string `json:"credential"`
	}
	if err := json.Unmarshal([]byte(out), &joined); err != nil || joined.Credential == "" {
		t.Fatalf("the join's answer = %s (%v)", out, err)
	}
	return joined.Credential
}

func TestThePermitRoutesCreateAndListItem(t *testing.T) {
	srv := startServer(t, nil)
	id := permitPost(t, srv, "s-1", "Bash", `{"command":"npm test"}`)
	items := needsItems(t, srv)
	row, ok := items[fmt.Sprintf("%d", id)]
	if !ok {
		t.Fatalf("the shared list = %v, want the posted item", items)
	}
	if row[0] != "permission" || row[1] != "open" {
		t.Errorf("the item's kind and state = %v, want permission open", row)
	}
	// the row carries the tool call the owner decides on
	detail := permitRow(t, srv, id)
	if detail["tool"] != "Bash" || detail["input"] != `{"command":"npm test"}` {
		t.Errorf("the row's tool call = %v %v", detail["tool"], detail["input"])
	}
	if detail["truncated"] == true {
		t.Error("an input under the limit is marked truncated")
	}
	if summary, _ := detail["summary"].(string); summary != "Bash: npm test" {
		t.Errorf("the summary = %q, want the cleaned one-line form", summary)
	}
	// the asking session reads needs-you in the registry
	reportSession(t, srv, "s-1", "", "", "UserPromptSubmit")
	code, _, out := srv.get(t, "/api/registry/sessions/s-1", nil)
	if code != 200 {
		t.Fatalf("the session = %d %s", code, out)
	}
	if state := bodyMap(t, out)["state"]; state != "needs-you" {
		t.Errorf("the asking session reads %v, want needs-you", state)
	}
	// a torn post is refused: nothing reaches the list
	if code, _ := srv.post(t, "/api/needs", `{"tool_name":"Bash"}`, nil); code != 400 {
		t.Errorf("a post without a session = %d, want 400", code)
	}
	if code, _ := srv.post(t, "/api/needs", `{"session_id":"s-2"}`, nil); code != 400 {
		t.Errorf("a post without a tool = %d, want 400", code)
	}
}

// KindPermissionOf names the permission kind without importing the hub package into every case.
func KindPermissionOf() string { return "permission" }

func TestThePermitRoutesNeedTheWriteGuards(t *testing.T) {
	srv := startServer(t, nil)
	request, err := http.NewRequest(http.MethodPost, srv.base+"/api/needs",
		strings.NewReader(`{"session_id":"s-1","tool_name":"Bash","tool_input":{"command":"npm test"}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("content-type", "application/json")
	request.Header.Set("x-huddle-token", srv.token)
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 403 {
		t.Errorf("a write naming no action = %d, want 403", response.StatusCode)
	}
}

func TestDecideAllowsAndDeniesOnce(t *testing.T) {
	srv := startServer(t, nil)
	id := permitPost(t, srv, "s-1", "Bash", `{"command":"npm test"}`)
	code, out := srv.post(t, fmt.Sprintf("/api/needs/%d/decide", id), `{"decision":"deny","message":"not tonight"}`, nil)
	if code != 200 {
		t.Fatalf("deny: %d %s", code, out)
	}
	row := bodyMap(t, out)
	if row["state"] != "answered" || row["decision"] != "deny" || row["message"] != "not tonight" {
		t.Errorf("the denied row = %v", row)
	}
	// the second answer — the app's or the terminal's — changes nothing
	if code, out = srv.post(t, fmt.Sprintf("/api/needs/%d/decide", id), `{"decision":"allow"}`, nil); code != 409 {
		t.Fatalf("a second decision = %d %s, want 409", code, out)
	}
	other := permitPost(t, srv, "s-2", "Bash", `{"command":"npm run build"}`)
	if code, out = srv.post(t, fmt.Sprintf("/api/needs/%d/decide", other), `{"decision":"allow"}`, nil); code != 200 {
		t.Fatalf("allow: %d %s", code, out)
	}
	if row = bodyMap(t, out); row["state"] != "answered" || row["decision"] != "allow" {
		t.Errorf("the allowed row = %v", row)
	}
	// a decision that is neither refuses, and an unknown item is a 404
	third := permitPost(t, srv, "s-3", "Bash", `{"command":"true"}`)
	if code, _ = srv.post(t, fmt.Sprintf("/api/needs/%d/decide", third), `{"decision":"maybe"}`, nil); code != 400 {
		t.Errorf("a decision that is neither allow nor deny = %d, want 400", code)
	}
	if code, _ = srv.post(t, "/api/needs/999999/decide", `{"decision":"allow"}`, nil); code != 404 {
		t.Errorf("an unknown item = %d, want 404", code)
	}
	// one audit row per decision (SPEC 7.7): the actor, the action, the item
	code, _, out = srv.get(t, "/api/audit", nil)
	if code != 200 {
		t.Fatalf("the audit log = %d %s", code, out)
	}
	rows, _ := bodyMap(t, out)["entries"].([]any)
	seen := map[string]string{}
	for _, row := range rows {
		if m, ok := row.(map[string]any); ok {
			seen[fmt.Sprintf("%v", m["action"])] = fmt.Sprintf("%v", m["target"])
		}
	}
	if seen["deny"] != fmt.Sprintf("%d", id) || seen["allow"] != fmt.Sprintf("%d", other) {
		t.Errorf("the audit rows = %v, want one deny on %d and one allow on %d", seen, id, other)
	}
	for _, row := range rows {
		if m, ok := row.(map[string]any); ok {
			if m["actor"] != "owner" {
				t.Errorf("the audit row's actor = %v, want the owner", m["actor"])
			}
		}
	}
	// the refused decision and the unknown item wrote nothing
	if len(rows) != 2 {
		t.Errorf("the audit log holds %d rows, want the two decisions alone", len(rows))
	}
	// the remote allow raised its alert: the session and the tool, never the command
	db, err := store.SQLite(context.Background(), filepath.Join(srv.home, "hub.db"))
	if err != nil {
		t.Fatalf("open the hub db: %v", err)
	}
	defer db.Close()
	alertRows, err := db.All(context.Background(), "SELECT kind, json FROM alerts ORDER BY id")
	if err != nil {
		t.Fatalf("read the alerts: %v", err)
	}
	if len(alertRows) != 1 || alertRows[0]["kind"] != "alert.remote_allow" {
		t.Fatalf("the alerts = %v, want the one remote allow", alertRows)
	}
	body, _ := alertRows[0]["json"].(string)
	if !strings.Contains(body, `"session":"s-2"`) || !strings.Contains(body, `"tool":"Bash"`) {
		t.Errorf("the alert = %s, want the session and the tool", body)
	}
	if strings.Contains(body, "npm run build") {
		t.Errorf("the alert carries the command: %s", body)
	}
}

func TestOnlyTheOwnerDecides(t *testing.T) {
	srv := startServer(t, nil)
	id := permitPost(t, srv, "s-1", "Bash", `{"command":"npm test"}`)
	member := permitMember(t, srv)
	headers := map[string]string{"x-huddle-token": member}
	if code, out := srv.post(t, fmt.Sprintf("/api/needs/%d/decide", id), `{"decision":"allow"}`, headers); code != 403 {
		t.Fatalf("a session's own decision = %d %s, want 403", code, out)
	}
	// the member may read the list and hold the hook's poll: only the answer is the owner's
	if code, _, _ := srv.get(t, "/api/needs", headers); code != 200 {
		t.Errorf("a member reads the needs = %d, want 200", code)
	}
	if code, _, _ := srv.get(t, fmt.Sprintf("/api/needs/%d/wait?timeout=1", id), headers); code != 200 {
		t.Errorf("a member holds the poll = %d, want 200", code)
	}
	if code, out := srv.post(t, fmt.Sprintf("/api/needs/%d/decide", id), `{"decision":"allow"}`, nil); code != 200 {
		t.Fatalf("the owner's decision = %d %s", code, out)
	}
}

func TestAllowOnACutInputIsRefused(t *testing.T) {
	srv := startServer(t, nil)
	big := `{"command":"echo ` + strings.Repeat("x", 9000) + `"}`
	id := permitPost(t, srv, "s-1", "Bash", big)
	if row := permitRow(t, srv, id); row["truncated"] != true {
		t.Fatalf("the item is not marked truncated: %v", row)
	}
	code, out := srv.post(t, fmt.Sprintf("/api/needs/%d/decide", id), `{"decision":"allow"}`, nil)
	if code != 409 {
		t.Fatalf("allow a cut input = %d %s, want 409", code, out)
	}
	if !strings.Contains(out, "the input was cut; allow it in the terminal") {
		t.Errorf("the refusal = %s, want it to name the terminal", out)
	}
	// a denial needs no such check: the input is already refused remotely
	if code, _ = srv.post(t, fmt.Sprintf("/api/needs/%d/decide", id), `{"decision":"deny"}`, nil); code != 200 {
		t.Errorf("deny a cut input = %d, want 200", code)
	}
}

func TestAllowPassesTheMachinesSwitches(t *testing.T) {
	cases := []struct {
		name    string
		config  string
		tool    string
		input   string
		want    int
		wantMsg string
	}{
		{
			"the default admits a plain command", "",
			"Bash", `{"command":"npm test"}`, 200, "",
		},
		{
			"a deleting command is refused", "",
			"Bash", `{"command":"rm -rf /tmp/x"}`, 403, "deleting command",
		},
		{
			"a forced push is refused", "",
			"Bash", `{"command":"git push --force"}`, 403, "forced push",
		},
		{
			"a secrets path is refused", "",
			"Read", `{"file_path":"secrets/token.txt"}`, 403, "secrets path",
		},
		{
			"a .ssh path is refused", "",
			"Read", `{"file_path":"~/.ssh/config"}`, 403, ".ssh",
		},
		{
			"a narrow list admits its own command", "[remote]\nallow = [\"Bash(npm test*)\"]\n",
			"Bash", `{"command":"npm test -- -f"}`, 200, "",
		},
		{
			"a narrow list refuses the rest", "[remote]\nallow = [\"Bash(npm test*)\"]\n",
			"Bash", `{"command":"cargo build"}`, 403, "not in this machine's remote allow list",
		},
		{
			"the floor beats the list", "[remote]\nallow = [\"*\"]\npermissions = true\n",
			"Bash", `{"command":"rm x"}`, 403, "deleting command",
		},
		{
			"remote permissions off refuse everything", "[remote]\npermissions = false\n",
			"Bash", `{"command":"npm test"}`, 403, "remote permissions are off",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := startServer(t, nil)
			if tc.config != "" {
				permitConfig(t, srv, tc.config)
			}
			id := permitPost(t, srv, "s-1", tc.tool, tc.input)
			code, out := srv.post(t, fmt.Sprintf("/api/needs/%d/decide", id), `{"decision":"allow"}`, nil)
			if code != tc.want {
				t.Fatalf("allow = %d %s, want %d", code, out, tc.want)
			}
			if tc.wantMsg != "" && !strings.Contains(out, tc.wantMsg) {
				t.Errorf("the refusal = %s, want it to name %q", out, tc.wantMsg)
			}
		})
	}
}

func TestTheSharedSnoozeAndDismissHoldAPermissionItem(t *testing.T) {
	srv := startServer(t, nil)
	id := permitPost(t, srv, "s-1", "Bash", `{"command":"npm test"}`)
	// the shared snooze hides it from the list
	code, out := srv.post(t, fmt.Sprintf("/api/needs/%d/snooze", id), `{"until":"15m"}`, nil)
	if code != 200 {
		t.Fatalf("snooze: %d %s", code, out)
	}
	if row := permitRow(t, srv, id); row["state"] != "snoozed" {
		t.Fatalf("the snoozed item reads %v, want snoozed", row["state"])
	}
	// the shared list keeps it, shown behind its snooze
	if row, _ := needsItems(t, srv)[fmt.Sprintf("%d", id)]; row[1] != "snoozed" {
		t.Errorf("the shared list shows %v, want the item behind its snooze", row)
	}
	// a snoozed item is not waiting: a decision refuses
	if code, _ = srv.post(t, fmt.Sprintf("/api/needs/%d/decide", id), `{"decision":"deny"}`, nil); code != 409 {
		t.Errorf("deciding a snoozed item = %d, want 409", code)
	}
	// a snooze span that is not one of the three refuses
	if code, _ = srv.post(t, fmt.Sprintf("/api/needs/%d/snooze", id), `{"until":"banana"}`, nil); code != 400 {
		t.Errorf("a garbage span = %d, want 400", code)
	}
	// the shared dismiss drops it, and a second dismiss finds nothing open
	if code, out = srv.post(t, fmt.Sprintf("/api/needs/%d/dismiss", id), `{}`, nil); code != 200 {
		t.Fatalf("dismiss: %d %s", code, out)
	}
	if row := permitRow(t, srv, id); row["state"] != "dismissed" {
		t.Fatalf("the dismissed item reads %v, want dismissed", row["state"])
	}
	if code, _ = srv.post(t, fmt.Sprintf("/api/needs/%d/dismiss", id), `{}`, nil); code != 404 {
		t.Errorf("a second dismiss = %d, want 404", code)
	}
}

func TestTheWaitRouteAnswersTheDecisionAndItsTimeout(t *testing.T) {
	srv := startServer(t, nil)
	id := permitPost(t, srv, "s-1", "Bash", `{"command":"npm test"}`)
	// one short poll times out with the item still open: the hook re-polls
	if row := permitRow(t, srv, id); row["state"] != "open" {
		t.Fatalf("a timed-out wait answers %v, want the item still open", row["state"])
	}
	// a second poll is answered by the decision that lands while it holds
	answered := make(chan string, 1)
	go func() {
		code, _, out := srv.get(t, fmt.Sprintf("/api/needs/%d/wait?timeout=30", id), nil)
		if code != 200 {
			answered <- fmt.Sprintf("code %d", code)
			return
		}
		answered <- out
	}()
	time.Sleep(200 * time.Millisecond)
	if code, out := srv.post(t, fmt.Sprintf("/api/needs/%d/decide", id), `{"decision":"allow"}`, nil); code != 200 {
		t.Fatalf("decide while a poll holds: %d %s", code, out)
	}
	select {
	case out := <-answered:
		row := bodyMap(t, out)
		if row["state"] != "answered" || row["decision"] != "allow" {
			t.Fatalf("the held poll answered %v, want the allowance", row)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the held poll never answered")
	}
	// an unknown item's poll is a 404, and a garbage timeout is refused
	if code, _, _ := srv.get(t, "/api/needs/999999/wait?timeout=1", nil); code != 404 {
		t.Errorf("an unknown item's wait = %d, want 404", code)
	}
	if code, _, _ := srv.get(t, fmt.Sprintf("/api/needs/%d/wait?timeout=banana", id), nil); code != 400 {
		t.Errorf("a garbage timeout = %d, want 400", code)
	}
	// a poll that names no timeout takes the thirty-second default; a decided item answers
	// at once, so the default costs nothing here
	code, _, out := srv.get(t, fmt.Sprintf("/api/needs/%d/wait", id), nil)
	if code != 200 {
		t.Errorf("a wait without a timeout = %d, want 200", code)
	}
	if row := bodyMap(t, out); row["state"] != "answered" {
		t.Errorf("the decided item's wait answered %v, want answered", row["state"])
	}
}

func TestATerminalAnswerWithdrawsTheItem(t *testing.T) {
	srv := startServer(t, nil)
	id := permitPost(t, srv, "s-1", "Bash", `{"command":"npm test"}`)
	// a working event of another kind withdraws nothing
	reportSession(t, srv, "s-1", "", "", "UserPromptSubmit")
	if _, waiting := needsItems(t, srv)[fmt.Sprintf("%d", id)]; !waiting {
		t.Fatal("a UserPromptSubmit withdrew the item")
	}
	// the session's next PostToolUse does: the terminal answered first
	reportSession(t, srv, "s-1", "", "", "PostToolUse")
	if _, waiting := needsItems(t, srv)[fmt.Sprintf("%d", id)]; waiting {
		t.Fatal("the item still needs someone")
	}
	if code, out := srv.post(t, fmt.Sprintf("/api/needs/%d/decide", id), `{"decision":"allow"}`, nil); code != 409 {
		t.Fatalf("deciding a withdrawn item = %d %s, want 409", code, out)
	}
	if row := permitRow(t, srv, id); row["state"] != "withdrawn" {
		t.Fatalf("the withdrawn item reads %v, want withdrawn", row["state"])
	}
	// a Stop withdraws too
	other := permitPost(t, srv, "s-2", "Bash", `{"command":"npm run build"}`)
	reportSession(t, srv, "s-2", "", "", "Stop")
	if row := permitRow(t, srv, other); row["state"] != "withdrawn" {
		t.Fatalf("the item after its session's stop reads %v, want withdrawn", row["state"])
	}
}

func TestAPermissionItemStreamsToTheChannelsLiveStream(t *testing.T) {
	srv := startServer(t, nil)
	if code, out := srv.post(t, "/api/channels", `{"name":"reg"}`, nil); code != 200 {
		t.Fatalf("create the channel: %d %s", code, out)
	}
	reportSession(t, srv, "s-live", "reg", "bridge", "UserPromptSubmit")
	frames := openLive(t, srv, "reg", "")
	id := permitPost(t, srv, "s-live", "Bash", `{"command":"npm test"}`)
	frame := waitFor(t, frames, "needs.item")
	var got struct {
		Data struct {
			ID    int64  `json:"id"`
			State string `json:"state"`
			Kind  string `json:"kind"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(frame), &got); err != nil {
		t.Fatalf("the needs event = %s: %v", frame, err)
	}
	if got.Data.ID != id || got.Data.State != "open" || got.Data.Kind != "permission" {
		t.Fatalf("the needs event = %s, want the open permission item %d", frame, id)
	}
	// the decision streams too
	srv.post(t, fmt.Sprintf("/api/needs/%d/decide", id), `{"decision":"deny","message":"no"}`, nil)
	frame = waitFor(t, frames, "needs.item")
	if err := json.Unmarshal([]byte(frame), &got); err != nil {
		t.Fatalf("the needs event = %s: %v", frame, err)
	}
	if got.Data.State != "answered" {
		t.Fatalf("the needs event after the decision = %s, want answered", frame)
	}
}

func TestThePermissionSweeperWithdrawsADeadPrompt(t *testing.T) {
	// one server in process with a clock the test moves: the sweeper's own ticker runs in
	// real time, the expiry it computes runs on the server's clock
	var mu sync.Mutex
	clock := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	advance := func(by time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		clock = clock.Add(by)
	}
	pipeReader, pipeWriter := io.Pipe()
	announced := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(pipeReader).ReadString('\n')
		announced <- line
		_, _ = io.Copy(io.Discard, pipeReader)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() {
		_ = Serve(ctx, Options{
			Version: "test", Host: "127.0.0.1", Port: 0,
			Home: t.TempDir(), Data: t.TempDir(), Token: "sweep-token", AutoCreate: true,
			PermitSweepMS: 50,
			Stdout:        pipeWriter, Stderr: io.Discard,
			Now: func() time.Time { mu.Lock(); defer mu.Unlock(); return clock },
		})
		pipeWriter.Close()
		close(served)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-served:
		case <-time.After(5 * time.Second):
		}
	})
	line := <-announced
	match := regexp.MustCompile(`on http://[\d.]+:(\d+)`).FindStringSubmatch(line)
	if match == nil {
		t.Fatalf("the server did not say its port: %q", line)
	}
	base := "http://127.0.0.1:" + match[1]
	healthy := until(5*time.Second, func() bool {
		resp, err := http.Get(base + "/health")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == 200
	})
	if !healthy {
		t.Fatal("the server never answered /health")
	}
	post := func(path, body string) bool {
		request, err := http.NewRequest(http.MethodPost, base+path, strings.NewReader(body))
		if err != nil {
			return false
		}
		request.Header.Set("content-type", "application/json")
		request.Header.Set(ActionHeader, path)
		request.Header.Set("x-huddle-token", "sweep-token")
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode == 200
	}
	if !post("/api/needs", `{"session_id":"s-1","tool_name":"Bash","tool_input":{"command":"npm test"}}`) {
		t.Fatal("the item never landed")
	}
	// two minutes pass with no poll: the hook is gone, and the sweeper withdraws its prompt
	advance(2 * time.Minute)
	listed := func() bool {
		request, _ := http.NewRequest(http.MethodGet, base+"/api/needs", nil)
		request.Header.Set("x-huddle-token", "sweep-token")
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
		if err != nil {
			return true
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return strings.Contains(string(raw), `"permission"`)
	}
	if gone := until(5*time.Second, func() bool { return !listed() }); !gone {
		t.Fatal("the dead prompt never left the list")
	}
}
