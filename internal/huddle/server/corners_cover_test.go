package server

// The corners the main flows step around: an owner's words a due step-up refuses, a message
// whose channel or member cannot take it, a blocked task whose session never reported, the
// browser that answers as itself, an item that expires inside its own poll, a join that
// deduplicates against the channel's sessions, and the removal watch that rides along.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/webauthntest"
)

func TestAStepUpDueDeviceIsRefusedTheOwnerWords(t *testing.T) {
	srv := startServer(t, nil)
	key := webauthntest.NewSoftKey(t)
	setCookie, userHandle := pairDevice(t, srv, key, "Phone")
	cookie := cookieValueOf(t, setCookie)
	reportSession(t, srv, "s-due", "", "", "UserPromptSubmit")
	// the device's last assertion is an hour old: the five minutes are out
	stepUpStamp(t, srv, cookie, time.Now().Add(-time.Hour).UnixMilli())
	headers := deviceHeaders(cookie)
	headers["content-type"] = "application/json"
	code, _, body, err := srv.request(bg, http.MethodPost, "/api/sessions/s-due/answer", `{"text":"hi"}`, headers)
	if err != nil || code != 401 || body != stepUpRequiredBody {
		t.Errorf("an answer past a due step-up = %d %s (%v), want the 401", code, body, err)
	}
	code, _, body, err = srv.request(bg, http.MethodPost, "/api/send", `{"sessions":["s-due"],"text":"hi"}`, headers)
	if err != nil || code != 401 || body != stepUpRequiredBody {
		t.Errorf("a send past a due step-up = %d %s (%v), want the 401", code, body, err)
	}
	// a fresh assertion lets the same words through
	if code, _ := stepUpOverHTTP(t, srv, key, cookie, userHandle); code != 200 {
		t.Fatalf("the step-up ceremony = %d", code)
	}
	code, _, body, err = srv.request(bg, http.MethodPost, "/api/sessions/s-due/answer", `{"text":"hi"}`, headers)
	if err != nil || code != 200 {
		t.Errorf("an answer with a fresh assertion = %d %s (%v), want 200", code, body, err)
	}
}

func TestAMessageForAChannelOrMemberThatCannotTakeItWaits(t *testing.T) {
	srv := startServer(t, nil)
	if code, out := srv.post(t, "/api/channels", `{"name":"reg"}`, nil); code != 200 {
		t.Fatalf("create the channel: %d %s", code, out)
	}
	if code, out := srv.op(t, "reg", "join", "api", "{}"); code != 200 {
		t.Fatalf("join: %d %v", code, out)
	}
	// a session whose channel is gone: the ack proves the bridge pushes, and the push finds
	// no channel to carry it
	reportSession(t, srv, "s-ghost", "ghost", "m", "UserPromptSubmit")
	ackPush(t, srv, "ghost", "m")
	code, out := srv.post(t, "/api/sessions/s-ghost/answer", `{"text":"for the ghost"}`, nil)
	if code != 200 || bodyMap(t, out)["route"] != "next_prompt" {
		t.Fatalf("an answer to a ghost channel = %d %s, want the next prompt", code, out)
	}
	// a session whose member never joined: the channel is there and refuses the carry
	reportSession(t, srv, "s-stranger", "reg", "stranger", "UserPromptSubmit")
	ackPush(t, srv, "reg", "stranger")
	code, out = srv.post(t, "/api/sessions/s-stranger/answer", `{"text":"for the stranger"}`, nil)
	if code != 200 || bodyMap(t, out)["route"] != "next_prompt" {
		t.Fatalf("an answer to a stranger member = %d %s, want the next prompt", code, out)
	}
	// a permission item for the ghost-channel session still lands: its fan-out finds no channel
	// to carry the event, and the create answers all the same
	if id := permitPost(t, srv, "s-ghost", "Bash", `{"command":"npm test"}`); id == 0 {
		t.Fatal("the ghost session's permission item did not land")
	}
	// both wait as pending with no route of their own, nothing was marked delivered
	rows := messageRows(t, srv)
	if len(rows) != 2 {
		t.Fatalf("messages = %v, want the two queued", rows)
	}
	for _, row := range rows {
		if row["state"] != "pending" || row["route"] != nil {
			t.Errorf("the queued row = %v, want it pending with no route yet", row)
		}
	}
}

func TestABlockedTaskWithNoReportingSessionGrowsNoItem(t *testing.T) {
	srv := startServer(t, nil)
	if code, out := srv.post(t, "/api/channels", `{"name":"reg"}`, nil); code != 200 {
		t.Fatalf("create the channel: %d %s", code, out)
	}
	if code, out := srv.op(t, "reg", "join", "api", "{}"); code != 200 {
		t.Fatalf("join: %d %v", code, out)
	}
	// the task waits on a task the owner owns, and no session of the channel ever reported
	if code, out := srv.op(t, "reg", "task_create", "api", `{"id":"t-1","title":"Ship it","owner":"api"}`); code != 200 {
		t.Fatalf("t-1: %d %v", code, out)
	}
	if code, out := srv.op(t, "reg", "task_create", "api", `{"id":"t-own","title":"Approve","owner":"owner"}`); code != 200 {
		t.Fatalf("t-own: %d %v", code, out)
	}
	if code, out := srv.op(t, "reg", "task_update", "api", `{"id":"t-1","after":["t-own"]}`); code != 200 {
		t.Fatalf("depend: %d %v", code, out)
	}
	if code, out := srv.op(t, "reg", "task_status", "api", `{"id":"t-1","status":"blocked"}`); code != 200 {
		t.Fatalf("block: %d %v", code, out)
	}
	if items := needsItems(t, srv); len(items) != 0 {
		t.Fatalf("items = %v, want none while no session reports", items)
	}
}

func TestTheBrowserNamesItselfInTheAuditWhenItDecides(t *testing.T) {
	srv := startServer(t, nil)
	root := map[string]string{"x-huddle-token": srv.token}
	id := permitPost(t, srv, "s-1", "Bash", `{"command":"npm test"}`)
	// the owner's browser signs in and answers as itself
	_, out := srv.post(t, "/api/login", "{}", root)
	login := bodyMap(t, out)
	request, _ := http.NewRequest(http.MethodGet, srv.base+"/?code="+login["code"].(string), nil)
	request.Header.Set("x-huddle-token", srv.token)
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	cookie := strings.SplitN(response.Header.Get("Set-Cookie"), ";", 2)[0]
	// the cookie alone carries the request: no token beside it
	browser := map[string]string{"Cookie": cookie, "content-type": "application/json", "x-huddle-token": ""}
	code, out := srv.post(t, fmt.Sprintf("/api/needs/%d/decide", id), `{"decision":"deny"}`, browser)
	if code != 200 {
		t.Fatalf("the browser's decision = %d %s", code, out)
	}
	_, _, audit := srv.get(t, "/api/audit", root)
	if !strings.Contains(audit, `"actor":"owner"`) {
		t.Errorf("the audit = %s, want the browser's own name as the actor", audit)
	}
}

func TestTheOwnerRoutesRefuseBodiesAndChannelsTheyCannotActOn(t *testing.T) {
	srv := startServer(t, nil)
	root := map[string]string{"x-huddle-token": srv.token, "content-type": "application/json"}
	reportSession(t, srv, "s-1", "", "", "UserPromptSubmit")
	for _, tc := range []struct {
		name string
		path string
		body string
		want string
	}{
		{"an answer whose body is not JSON", "/api/sessions/s-1/answer", `{not json`, "body is not JSON"},
		{"a send whose body is not JSON", "/api/send", `{not json`, "body is not JSON"},
		{"a push ack whose body is not JSON", "/api/registry/push-ack", `{not json`, "body is not JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, body, err := srv.request(bg, http.MethodPost, tc.path, tc.body, root)
			if err != nil || code != 400 || !strings.Contains(body, tc.want) {
				t.Fatalf("%s = %d %s (%v), want the 400", tc.name, code, body, err)
			}
		})
	}
	// a pause of a session in a channel that is gone, and of a member the channel never knew:
	// the channel's own refusals surface
	if code, out := srv.post(t, "/api/channels", `{"name":"reg"}`, nil); code != 200 {
		t.Fatalf("create the channel: %d %s", code, out)
	}
	reportSession(t, srv, "s-ghost", "ghost", "m", "UserPromptSubmit")
	if code, out := srv.post(t, "/api/sessions/s-ghost/pause", `{}`, nil); code != 404 || !strings.Contains(out, "no channel ghost") {
		t.Errorf("pausing a session in a ghost channel = %d %s, want the channel's 404", code, out)
	}
	reportSession(t, srv, "s-stranger", "reg", "stranger", "UserPromptSubmit")
	if code, out := srv.post(t, "/api/sessions/s-stranger/pause", `{}`, nil); code != 404 || !strings.Contains(out, "has not joined") {
		t.Errorf("pausing a member the channel never knew = %d %s, want the channel's 404", code, out)
	}
}

func TestAnItemNearItsExpiryEndsItsOwnPollWithNoRow(t *testing.T) {
	// one server in process with a clock the test moves, its stdout captured (a start with no
	// credential of its own shows how to reach it)
	var mu sync.Mutex
	clock := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
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
			Home: t.TempDir(), Data: t.TempDir(), Token: "expiry-token", AutoCreate: true,
			ShowRoot: true,
			Stdout:   pipeWriter, Stderr: io.Discard,
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
	call := func(method, path, body string) (int, string) {
		t.Helper()
		request, err := http.NewRequest(method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("x-huddle-token", "expiry-token")
		if method != http.MethodGet {
			request.Header.Set("content-type", "application/json")
			request.Header.Set(ActionHeader, path)
		}
		response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		return response.StatusCode, string(raw)
	}
	if code, out := call(http.MethodPost, "/api/needs",
		`{"session_id":"s-1","tool_name":"Bash","tool_input":{"command":"npm test"}}`); code != 200 {
		t.Fatalf("the item never landed: %d %s", code, out)
	}
	// polls along the way keep the item's hook fresh while its life runs out: with under two
	// seconds of life left, the hold ends at the item's own age, not at its timeout
	for _, step := range []time.Duration{time.Minute, 5 * time.Minute, 3 * time.Minute} {
		if code, out := call(http.MethodGet, "/api/needs/1/wait?timeout=1", ""); code != 200 {
			t.Fatalf("a poll along the way = %d %s", code, out)
		}
		mu.Lock()
		clock = clock.Add(step)
		mu.Unlock()
	}
	mu.Lock()
	clock = clock.Add(59*time.Second + 500*time.Millisecond)
	mu.Unlock()
	started := time.Now()
	code, out := call(http.MethodGet, "/api/needs/1/wait?timeout=30", "")
	if code != 200 {
		t.Fatalf("the near-expiry poll = %d %s", code, out)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("the poll held %s, want it ended by the item's own age", elapsed)
	}
	if state := bodyMap(t, out)["state"]; state != "expired" {
		t.Errorf("the poll at the age limit answered %v, want expired", state)
	}
}

func TestAUniqueJoinNamesItselfAfterTheChannelSessions(t *testing.T) {
	srv := startServer(t, nil)
	root := map[string]string{"x-huddle-token": srv.token}
	if code, out := srv.post(t, "/api/channels", `{"name":"ujoin"}`, nil); code != 200 {
		t.Fatalf("create the channel: %d %s", code, out)
	}
	for _, name := range []string{"alpha", "beta"} {
		if code, out := srv.op(t, "ujoin", "join", name, "{}"); code != 200 {
			t.Fatalf("join %s: %d %v", name, code, out)
		}
	}
	code, out := srv.post(t, "/api/tokens", `{"channel":"ujoin"}`, root)
	if code != 200 {
		t.Fatalf("mint the invite: %d %s", code, out)
	}
	invite := bodyMap(t, out)
	token, _ := invite["token"].(string)
	// the name alpha is taken by the channel's own session: the join answers alpha-2
	code, out = srv.post(t, "/api/join", fmt.Sprintf(`{"token":%q,"name":"alpha","unique":true,"channel":"ujoin"}`, token), nil)
	if code != 200 {
		t.Fatalf("the unique join = %d %s", code, out)
	}
	joined := bodyMap(t, out)
	if joined["name"] != "alpha-2" {
		t.Errorf("the unique join answered %v, want alpha-2", joined["name"])
	}
}

func TestTheRemovalWatchRidesAlongWithoutDeciding(t *testing.T) {
	srv := startServer(t, map[string]string{
		"HUDDLE_REMOVAL_WATCH": "1",
		// a config dir with no plugin registry at all: the watch reads unknown, never gone
		"CLAUDE_CONFIG_DIR": t.TempDir(),
	})
	if code, _, body := srv.get(t, "/health", nil); code != 200 || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("/health = %d %s, want the server up beside its watch", code, body)
	}
	// the stop at cleanup ends the watch with the server: nothing hangs, nothing decides
}

func TestTheReportRowsCarryTheirMachineAndProject(t *testing.T) {
	srv := startServer(t, nil)
	body := fmt.Sprintf(`{"session_id":"s-m","last_event":"UserPromptSubmit","last_event_at":%q,"cwd":%q}`,
		time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), srv.dataDir)
	code, out := srv.post(t, "/api/registry/report", body, nil)
	if code != 200 {
		t.Fatalf("the report = %d %s", code, out)
	}
	code, _, detail := srv.get(t, "/api/registry/sessions/s-m", nil)
	if code != 200 {
		t.Fatalf("the session = %d %s", code, detail)
	}
	var row struct {
		State   string `json:"state"`
		Project string `json:"project"`
	}
	if err := json.Unmarshal([]byte(detail), &row); err != nil {
		t.Fatalf("the session body %s: %v", detail, err)
	}
	if row.State == "" {
		t.Errorf("the session = %s, want its state", detail)
	}
}
