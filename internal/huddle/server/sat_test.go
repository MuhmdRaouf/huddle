package server

// Two huddles in one test process — the hub and one satellite — joined the way the words join
// them: a one-use invite from `huddle pair --satellite` spent at /api/sat/join, the credential
// the satellite keeps, the role it restarts into. The satellite's link then carries a session's
// reports and a permission item to the hub, the owner's allow travels back and lands on the
// machine's own item, a message follows the same road, and an allow outside the machine's own
// allow list is refused with its reason — the machine's switches rule there (SPEC 6.6).

import (
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

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
)

// runLog is the log one server run writes; joined answers it whole.
type runLog struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *runLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *runLog) joined() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startHuddleRun starts one server in process over its own home and answers its base url.
func startHuddleRun(t *testing.T, homeDir, token string, log *runLog) (base string, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- Serve(ctx, Options{
			Version: "test", Host: "127.0.0.1", Port: 0,
			Home: homeDir, Data: homeDir, Token: token, AutoCreate: true,
			PermitSweepMS: 200,
			Secrets:       func(_ context.Context, h home.Home) home.SecretStore { return home.KeyFiles{Dir: h.Keys()} },
			Stdout:        log, Stderr: io.Discard,
		})
	}()
	deadline := time.Now().Add(10 * time.Second)
	for base == "" {
		if m := regexp.MustCompile(`on http://127\.0\.0\.1:(\d+)`).FindStringSubmatch(log.joined()); m != nil {
			base = "http://127.0.0.1:" + m[1]
		}
		if time.Now().After(deadline) {
			stop()
			t.Fatalf("the server never started: %s", log.joined())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if healthy := until(5*time.Second, func() bool {
		response, err := http.Get(base + "/health")
		if err != nil {
			return false
		}
		defer response.Body.Close()
		return response.StatusCode == 200
	}); !healthy {
		stop()
		t.Fatalf("the server at %s never answered /health: %s", base, log.joined())
	}
	return base, stop, doneCh
}

// call does one guarded request and answers the status and the body. token is a session's
// x-huddle-token; bearer is a satellite credential's Authorization.
func call(t *testing.T, base, method, path, token, bearer, body string) (int, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, base+path, reader)
	if err != nil {
		t.Fatalf("request %s: %v", path, err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(ActionHeader, path)
	}
	if token != "" {
		request.Header.Set("x-huddle-token", token)
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := (&http.Client{Timeout: 45 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("call %s: %v", path, err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(raw)
}

// TestHubAndSatellitePassAPermitAndAMessage is the card's own end to end: pair, join, forward,
// decide, refuse, send.
func TestHubAndSatellitePassAPermitAndAMessage(t *testing.T) {
	hubHome, satHome := t.TempDir(), t.TempDir()
	hubLog, satLog := &runLog{}, &runLog{}
	hubBase, stopHub, hubDone := startHuddleRun(t, hubHome, "hub-token", hubLog)
	t.Cleanup(func() {
		stopHub()
		select {
		case <-hubDone:
		case <-time.After(10 * time.Second):
		}
	})

	// the satellite joins: a one-use invite minted the way `huddle pair --satellite` mints it,
	// spent at the join, and the credential it gets back is what the link carries
	minter, err := hub.Open(context.Background(), home.Home{Root: hubHome}.HubDB())
	if err != nil {
		t.Fatalf("open the hub's database: %v", err)
	}
	invite, err := minter.MintSatelliteInvite(context.Background(), time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	status, body := call(t, hubBase, http.MethodPost, "/api/sat/join", "", "",
		`{"invite":"`+invite+`","name":"mbp"}`)
	if status != 200 {
		t.Fatalf("the join answered %d: %s", status, body)
	}
	var joined struct {
		Credential string `json:"credential"`
		Name       string `json:"name"`
	}
	if err := json.Unmarshal([]byte(body), &joined); err != nil || joined.Credential == "" || joined.Name != "mbp" {
		t.Fatalf("the join = %s: %v", body, err)
	}
	// one use: a second join with the same invite buys nothing
	if status, _ = call(t, hubBase, http.MethodPost, "/api/sat/join", "", "",
		`{"invite":"`+invite+`","name":"mbp"}`); status != 401 {
		t.Fatalf("a spent invite answered %d, want 401", status)
	}
	// no credential: the satellite routes answer nothing
	if status, _ = call(t, hubBase, http.MethodGet, "/api/sat/next", "", "", ""); status != 401 {
		t.Fatalf("a credentialless poll answered %d, want 401", status)
	}
	if err := minter.Close(); err != nil {
		t.Fatalf("close the hub's database: %v", err)
	}

	// the machine keeps its credential and starts into its role: the link comes up with its
	// server, which is how a real machine links
	h := home.Home{Root: satHome}
	if err := home.SaveConfig(h, home.Config{
		Role: "satellite", Machine: "mbp", Hub: home.HubConfig{URL: hubBase},
		Remote: home.RemoteConfig{Permissions: true, Messages: true, Allow: []string{"*"}},
	}); err != nil {
		t.Fatalf("the satellite's config: %v", err)
	}
	if err := home.SaveSatelliteSecret(context.Background(), home.KeyFiles{Dir: h.Keys()}, hubBase, joined.Credential); err != nil {
		t.Fatalf("the satellite's credential: %v", err)
	}
	satBase, stopSat, satDone := startHuddleRun(t, satHome, "sat-token", satLog)
	t.Cleanup(func() {
		stopSat()
		select {
		case <-satDone:
		case <-time.After(10 * time.Second):
		}
	})
	if !until(5*time.Second, func() bool {
		return strings.Contains(satLog.joined(), "satellite link to "+hubBase)
	}) {
		t.Fatalf("the link never came up: %s / %s", satLog.joined(), hubLog.joined())
	}

	// the machine's session reports and asks, the way its hooks do
	_, body = call(t, satBase, http.MethodPost, "/api/registry/report", "sat-token", "",
		`{"session_id":"s-1","machine":"mbp","project":"/repo","last_event":"SessionStart","last_event_at":"2026-10-16T12:00:00.000Z"}`)
	if !strings.Contains(body, `"ok":true`) {
		t.Fatalf("the satellite's report = %s", body)
	}
	status, body = call(t, satBase, http.MethodPost, "/api/needs", "sat-token", "",
		`{"session_id":"s-1","tool_name":"Bash","tool_input":{"command":"npm test"}}`)
	if status != 200 {
		t.Fatalf("the satellite's item answered %d: %s", status, body)
	}
	var asked struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal([]byte(body), &asked)

	// the link carries item and report across, under the machine the credential names
	hubItem := int64(0)
	if !until(8*time.Second, func() bool {
		_, body := call(t, hubBase, http.MethodGet, "/api/needs", "hub-token", "", "")
		var list struct {
			Items []struct {
				ID      int64  `json:"id"`
				Kind    string `json:"kind"`
				State   string `json:"state"`
				Summary string `json:"summary"`
			} `json:"items"`
		}
		if json.Unmarshal([]byte(body), &list) != nil {
			return false
		}
		for _, item := range list.Items {
			if item.Kind == "permission" && strings.Contains(item.Summary, "npm test") && item.State == "open" {
				hubItem = item.ID
				return true
			}
		}
		return false
	}) {
		t.Fatalf("the forwarded item never appeared on the hub: %s / %s", hubLog.joined(), satLog.joined())
	}
	// the hub's registry carries the machine's session
	if _, body = call(t, hubBase, http.MethodGet, "/api/registry/sessions?machine=mbp", "hub-token", "", ""); !strings.Contains(body, "s-1") {
		t.Fatalf("the hub's registry = %s, want the satellite's session", body)
	}

	// the owner allows on the hub; the machine's own hook hears the allow through its own poll
	type waitAnswer struct {
		State    string `json:"state"`
		Decision string `json:"decision"`
	}
	decided := make(chan waitAnswer, 1)
	go func() {
		_, body := call(t, satBase, http.MethodGet,
			fmt.Sprintf("/api/needs/%d/wait", asked.ID), "sat-token", "", "")
		var item waitAnswer
		_ = json.Unmarshal([]byte(body), &item)
		decided <- item
	}()
	status, body = call(t, hubBase, http.MethodPost,
		fmt.Sprintf("/api/needs/%d/decide", hubItem), "hub-token", "", `{"decision":"allow"}`)
	if status != 200 {
		t.Fatalf("the hub's allow answered %d: %s", status, body)
	}
	select {
	case got := <-decided:
		if got.State != "answered" || got.Decision != "allow" {
			t.Fatalf("the machine's poll = %+v, want answered allow", got)
		}
	case <-time.After(35 * time.Second):
		t.Fatalf("the machine's poll never heard the allow: %s / %s", hubLog.joined(), satLog.joined())
	}

	// the owner's words ride the same road: a send to the satellite's session is queued there
	status, body = call(t, hubBase, http.MethodPost, "/api/send", "hub-token", "",
		`{"sessions":["s-1"],"text":"ship it today"}`)
	if status != 200 || !strings.Contains(body, `"route":"satellite"`) {
		t.Fatalf("the send answered %d: %s", status, body)
	}
	messageBody := ""
	if !until(8*time.Second, func() bool {
		_, messageBody = call(t, satBase, http.MethodPost, "/api/sessions/s-1/messages", "sat-token", "", "{}")
		return strings.Contains(messageBody, "ship it today")
	}) {
		t.Fatalf("the message never reached the machine's session: %q / %s / %s", messageBody, hubLog.joined(), satLog.joined())
	}

	// an allow outside the machine's own list is refused there: the terminal reads the machine's
	// deny, and the hub's row reads refused with the machine's reason
	cfg, err := home.LoadConfig(h)
	if err != nil {
		t.Fatalf("load the satellite's config: %v", err)
	}
	cfg.Remote.Allow = []string{"Read(*)"}
	if err := home.SaveConfig(h, cfg); err != nil {
		t.Fatalf("tighten the satellite's list: %v", err)
	}
	_, body = call(t, satBase, http.MethodPost, "/api/needs", "sat-token", "",
		`{"session_id":"s-1","tool_name":"Bash","tool_input":{"command":"curl example.com"}}`)
	var asked2 struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal([]byte(body), &asked2)
	hubItem2 := int64(0)
	if !until(8*time.Second, func() bool {
		_, body := call(t, hubBase, http.MethodGet, "/api/needs", "hub-token", "", "")
		var list struct {
			Items []struct {
				ID      int64  `json:"id"`
				Kind    string `json:"kind"`
				State   string `json:"state"`
				Summary string `json:"summary"`
			} `json:"items"`
		}
		if json.Unmarshal([]byte(body), &list) != nil {
			return false
		}
		for _, item := range list.Items {
			if item.Kind == "permission" && strings.Contains(item.Summary, "curl example.com") && item.State == "open" {
				hubItem2 = item.ID
				return true
			}
		}
		return false
	}) {
		t.Fatalf("the second item never appeared on the hub: %s / %s", hubLog.joined(), satLog.joined())
	}
	decided2 := make(chan waitAnswer, 1)
	go func() {
		_, body := call(t, satBase, http.MethodGet,
			fmt.Sprintf("/api/needs/%d/wait", asked2.ID), "sat-token", "", "")
		var item waitAnswer
		_ = json.Unmarshal([]byte(body), &item)
		decided2 <- item
	}()
	if status, body = call(t, hubBase, http.MethodPost,
		fmt.Sprintf("/api/needs/%d/decide", hubItem2), "hub-token", "", `{"decision":"allow"}`); status != 200 {
		t.Fatalf("the second allow answered %d: %s", status, body)
	}
	select {
	case got := <-decided2:
		if got.State != "answered" || got.Decision != "deny" {
			t.Fatalf("the machine's poll = %+v, want the machine's deny", got)
		}
	case <-time.After(35 * time.Second):
		t.Fatalf("the machine's poll never heard the refusal: %s / %s", hubLog.joined(), satLog.joined())
	}
	// the hub's own row for the item reads the machine's refusal, whatever the owner allowed
	reader, err := hub.Open(context.Background(), home.Home{Root: hubHome}.HubDB())
	if err != nil {
		t.Fatalf("open the hub's database for the read-back: %v", err)
	}
	if !until(8*time.Second, func() bool {
		row, err := reader.PermissionItemByID(context.Background(), hubItem2, time.Now())
		return err == nil && row != nil && row.State == "answered" &&
			row.Decision == "refused" && strings.Contains(row.Message, "allow list")
	}) {
		row, _ := reader.PermissionItemByID(context.Background(), hubItem2, time.Now())
		t.Fatalf("the hub's row never read the machine's refusal: %+v / %s / %s", row, hubLog.joined(), satLog.joined())
	}
	// and the item left the needs-you list with it
	_, body = call(t, hubBase, http.MethodGet, "/api/needs", "hub-token", "", "")
	if strings.Contains(body, "curl example.com") {
		t.Fatalf("the refused item still waits on the hub: %s", body)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close the read-back: %v", err)
	}
	// and the refusal is the machine's own audit row (SPEC 7.7)
	if !until(8*time.Second, func() bool {
		_, body := call(t, hubBase, http.MethodGet, "/api/audit", "hub-token", "", "")
		return strings.Contains(body, `"action":"refused"`) && strings.Contains(body, "mbp")
	}) {
		t.Fatalf("the audit never heard the refusal: %s", hubLog.joined())
	}
}

// satelliteInboxRig is a hub paired with a satellite whose session listens on an inbox socket.
type satelliteInboxRig struct {
	hubBase string
	sat     *childServer
	lines   <-chan []byte
	hubLog  *runLog
}

// startSatelliteInboxRig pairs a hub with a satellite the way the machines pair: a one-use
// invite minted and spent at the join, the credential the machine keeps, its own server starting
// into the satellite role with its link up. The satellite runs as a child process of its own
// because the server under test is that process: its inbox report names a socket, and the
// socket the owner's words must reach is the one its own server posts through. The session it
// reports lives on an inbox socket with no pid, so a post takes the socket alone.
func startSatelliteInboxRig(t *testing.T) *satelliteInboxRig {
	t.Helper()
	hubHome, satHome := t.TempDir(), t.TempDir()
	hubLog := &runLog{}
	hubBase, stopHub, hubDone := startHuddleRun(t, hubHome, "hub-token", hubLog)
	t.Cleanup(func() {
		stopHub()
		select {
		case <-hubDone:
		case <-time.After(10 * time.Second):
		}
	})

	minter, err := hub.Open(context.Background(), home.Home{Root: hubHome}.HubDB())
	if err != nil {
		t.Fatalf("open the hub's database: %v", err)
	}
	invite, err := minter.MintSatelliteInvite(context.Background(), time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	status, body := call(t, hubBase, http.MethodPost, "/api/sat/join", "", "",
		`{"invite":"`+invite+`","name":"mbp"}`)
	if status != 200 {
		t.Fatalf("the join answered %d: %s", status, body)
	}
	var joined struct {
		Credential string `json:"credential"`
		Name       string `json:"name"`
	}
	if err := json.Unmarshal([]byte(body), &joined); err != nil || joined.Credential == "" || joined.Name != "mbp" {
		t.Fatalf("the join = %s: %v", body, err)
	}
	if err := minter.Close(); err != nil {
		t.Fatalf("close the hub's database: %v", err)
	}

	h := home.Home{Root: satHome}
	if err := home.SaveConfig(h, home.Config{
		Role: "satellite", Machine: "mbp", Hub: home.HubConfig{URL: hubBase},
		Remote: home.RemoteConfig{Permissions: true, Messages: true, Allow: []string{"*"}},
	}); err != nil {
		t.Fatalf("the satellite's config: %v", err)
	}
	if err := home.SaveSatelliteSecret(context.Background(), home.KeyFiles{Dir: h.Keys()}, hubBase, joined.Credential); err != nil {
		t.Fatalf("the satellite's credential: %v", err)
	}
	sat := startServer(t, map[string]string{"HUDDLE_HOME": satHome})
	if !until(10*time.Second, func() bool {
		return strings.Contains(sat.log.joined(), "satellite link to "+hubBase)
	}) {
		t.Fatalf("the link never came up: %s", sat.log.joined())
	}

	sock, ln := inboxListener(t)
	lines := inboxLines(t, ln)
	reportInboxSession(t, sat, "s-1", sock, 0)
	if !until(10*time.Second, func() bool {
		_, body := call(t, hubBase, http.MethodGet, "/api/registry/sessions?machine=mbp", "hub-token", "", "")
		return strings.Contains(body, "s-1")
	}) {
		t.Fatalf("the hub's registry never carried the machine's session: %s", hubLog.joined())
	}
	return &satelliteInboxRig{hubBase: hubBase, sat: sat, lines: lines, hubLog: hubLog}
}

// inboxLine is one line a server posts to a session's inbox socket.
type inboxLine struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Message   struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
}

// inboxOwnerLine decodes the one line the owner's words post to the socket; a second line — an
// auth line — fails the decode, so the answer is one line or none.
func inboxOwnerLine(t *testing.T, raw []byte) inboxLine {
	t.Helper()
	var line inboxLine
	if err := json.Unmarshal(raw, &line); err != nil {
		t.Fatalf("the socket carried %q: %v", raw, err)
	}
	return line
}

// TestASatelliteServerDeliversAnOwnersMessageAtOnce proves the at-once half of the satellite's
// message road: the owner's send rides the link to the machine, and the machine's own server
// posts it through the session's inbox socket with no prompt needed.
func TestASatelliteServerDeliversAnOwnersMessageAtOnce(t *testing.T) {
	rig := startSatelliteInboxRig(t)
	status, body := call(t, rig.hubBase, http.MethodPost, "/api/send", "hub-token", "",
		`{"sessions":["s-1"],"text":"ship it today"}`)
	if status != 200 || !strings.Contains(body, `"route":"satellite"`) {
		t.Fatalf("the send answered %d: %s", status, body)
	}
	var raw []byte
	select {
	case raw = <-rig.lines:
	case <-time.After(10 * time.Second):
		t.Fatalf("the session's socket never heard the owner: %s / %s",
			rig.hubLog.joined(), rig.sat.log.joined())
	}
	line := inboxOwnerLine(t, raw)
	if line.Type != "user" || line.SessionID != "s-1" ||
		line.Message.Role != "user" || !strings.Contains(line.Message.Content, "ship it today") {
		t.Fatalf("the socket carried %+v, want the owner's words for s-1", line)
	}
}
