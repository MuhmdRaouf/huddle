package hooks

// The PermissionRequest hook: one tool call becomes a needs-you item the owner's app may
// allow or deny while the hook holds a long poll. The cases run the hook against the real
// server (in process, and once as the child process Claude Code runs), against dead, mute,
// garbage and restarting Huddles, and past the hook's own budget — silent whenever nothing arrives, so the
// terminal's own prompt is the fallback.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// permitInputJSON is the stdin Claude Code sends a PermissionRequest hook.
const permitInputJSON = `{"session_id":"s-rep","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"npm test"}}`

// needIDOf waits for the server to hold one open needs-you item and answers its id.
func needIDOf(t *testing.T, s *huddleServer) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		request, err := http.NewRequest(http.MethodGet, s.base+"/api/needs", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("x-huddle-token", s.token)
		response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
		if err == nil {
			raw, _ := io.ReadAll(response.Body)
			response.Body.Close()
			var list struct {
				Items []struct {
					ID int64 `json:"id"`
				} `json:"items"`
			}
			if json.Unmarshal(raw, &list) == nil && len(list.Items) > 0 {
				return fmt.Sprintf("%d", list.Items[0].ID)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no needs-you item arrived: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// runPermitHook runs the permit hook in the test's process against s and answers its stdout.
func runPermitHook(t *testing.T, s *huddleServer, input string, env map[string]string) string {
	t.Helper()
	out := make(chan string, 1)
	vars := map[string]string{"HUDDLE_URL": s.base}
	for key, value := range env {
		vars[key] = value
	}
	go func() {
		stdout, _, _ := runHook(t, "permit", input, vars)
		out <- stdout
	}()
	return <-out
}

func TestThePermitHookPrintsTheAppAllowance(t *testing.T) {
	s := startHuddle(t)
	out := make(chan string, 1)
	go func() {
		stdout, _, _ := runHook(t, "permit", permitInputJSON, map[string]string{"HUDDLE_URL": s.base})
		out <- stdout
	}()
	id := needIDOf(t, s)
	if code, body := post(t, s, "/api/needs/"+id+"/decide", s.token, `{"decision":"allow"}`); code != 200 {
		t.Fatalf("the app's allow = %d %s", code, body)
	}
	select {
	case stdout := <-out:
		want := `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}` + "\n"
		if stdout != want {
			t.Fatalf("the hook printed %q, want the documented allow decision", stdout)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the hook never answered its poll")
	}
}

func TestThePermitHookPrintsTheAppDenialWithItsMessage(t *testing.T) {
	s := startHuddle(t)
	// a deleting command may not be allowed remotely, but a denial is always safe to take
	input := `{"session_id":"s-rep","tool_name":"Bash","tool_input":{"command":"rm -rf /tmp/x"}}`
	out := make(chan string, 1)
	go func() {
		stdout, _, _ := runHook(t, "permit", input, map[string]string{"HUDDLE_URL": s.base})
		out <- stdout
	}()
	id := needIDOf(t, s)
	if code, body := post(t, s, "/api/needs/"+id+"/decide", s.token,
		`{"decision":"deny","message":"not tonight"}`); code != 200 {
		t.Fatalf("the app's deny = %d %s", code, body)
	}
	select {
	case stdout := <-out:
		want := `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"not tonight"}}}` + "\n"
		if stdout != want {
			t.Fatalf("the hook printed %q, want the documented deny decision with its message", stdout)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the hook never answered its poll")
	}
}

func TestThePermitHookPrintsNothingWhenTheTerminalAnswersFirst(t *testing.T) {
	s := startHuddle(t)
	out := make(chan string, 1)
	go func() {
		stdout, _, _ := runHook(t, "permit", permitInputJSON, map[string]string{"HUDDLE_URL": s.base})
		out <- stdout
	}()
	id := needIDOf(t, s)
	// the session's next PostToolUse is the terminal having answered its own prompt
	report := `{"session_id":"s-rep","last_event":"PostToolUse","last_event_at":"` +
		time.Now().UTC().Format("2006-01-02T15:04:05.000Z") + `"}`
	if code, body := post(t, s, "/api/registry/report", s.token, report); code != 200 {
		t.Fatalf("the terminal's report = %d %s", code, body)
	}
	select {
	case stdout := <-out:
		if stdout != "" {
			t.Fatalf("the hook printed %q, want nothing: the terminal's answer stands", stdout)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the hook never came back from its poll")
	}
	_ = id
}

func TestThePermitHookGivesUpAtItsOwnBudget(t *testing.T) {
	s := startHuddle(t)
	t0 := time.Now()
	stdout := runPermitHook(t, s, permitInputJSON, map[string]string{"HUDDLE_PERMIT_BUDGET_MS": "800"})
	if stdout != "" {
		t.Fatalf("the hook printed %q with no decision, want silence", stdout)
	}
	if elapsed := time.Since(t0); elapsed > 6*time.Second {
		t.Fatalf("the hook took %s, want it inside its budget", elapsed)
	}
}

func TestThePermitHookIsSilentOnEveryFailure(t *testing.T) {
	cases := []struct {
		name  string
		input string
		env   map[string]string
	}{
		{"no huddle", permitInputJSON, map[string]string{"HUDDLE_URL": ""}},
		{"no session", `{"tool_name":"Bash","tool_input":{"command":"npm test"}}`, nil},
		{"no tool", `{"session_id":"s-rep"}`, nil},
		{"no tool input", `{"session_id":"s-rep","tool_name":"Bash"}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, elapsed, _ := runHook(t, "permit", tc.input, tc.env)
			if stdout != "" {
				t.Errorf("the hook printed %q, want silence", stdout)
			}
			if elapsed > 5*time.Second {
				t.Errorf("the hook took %s, want it to fail fast", elapsed)
			}
		})
	}
	for name, url := range map[string]func(*testing.T) string{
		"down":        downURL,
		"mute":        muteURL,
		"garbage":     garbageURL,
		"wrong shape": wrongShapeURL,
		"starting":    startingURL,
	} {
		t.Run(name, func(t *testing.T) {
			url := url(t)
			stdout, elapsed, _ := runHook(t, "permit", permitInputJSON,
				map[string]string{"HUDDLE_URL": url, "HUDDLE_PERMIT_BUDGET_MS": "800"})
			if stdout != "" {
				t.Errorf("the hook printed %q, want silence", stdout)
			}
			// the POST alone bounds the mute case; the budget bounds the rest
			if elapsed > 10*time.Second {
				t.Errorf("the hook took %s, want it inside its deadlines", elapsed)
			}
		})
	}
}

func TestThePermitHookStaysSilentWhenTheAllowIsRefused(t *testing.T) {
	s := startHuddle(t)
	// a cut input is never allowed remotely: the app's allow is refused, and the hook has no
	// decision to print — the terminal is the only place left to answer
	big := `{"session_id":"s-rep","tool_name":"Bash","tool_input":{"command":"echo ` +
		strings.Repeat("x", 9000) + `"}}`
	out := make(chan string, 1)
	go func() {
		stdout, _, _ := runHook(t, "permit", big, map[string]string{
			"HUDDLE_URL": s.base, "HUDDLE_PERMIT_BUDGET_MS": "1200"})
		out <- stdout
	}()
	id := needIDOf(t, s)
	if code, body := post(t, s, "/api/needs/"+id+"/decide", s.token, `{"decision":"allow"}`); code != 409 {
		t.Fatalf("the app's allow of a cut input = %d %s, want 409", code, body)
	}
	select {
	case stdout := <-out:
		if stdout != "" {
			t.Fatalf("the hook printed %q, want silence: the refusal leaves the terminal", stdout)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the hook never came back from its budget")
	}
}

// TestThePermitHookRunsAsTheProcessClaudeCodeRuns is the full flow against a fake Claude Code:
// the real binary, spawn it as Claude Code spawns it (stdin the hook JSON, the world in env),
// let the app answer while it polls, and take the decision it prints.
func TestThePermitHookRunsAsTheProcessClaudeCodeRuns(t *testing.T) {
	s := startHuddle(t)
	home := t.TempDir()
	vars := []string{
		"HOME=" + t.TempDir(),
		"HUDDLE_HOME=" + home,
		"HUDDLE_URL=" + s.base,
		"HUDDLE_TOKEN=" + suiteToken,
		"HUDDLE_NO_PROJECT_CRED=1",
		"XDG_STATE_HOME=" + t.TempDir(),
		"XDG_DATA_HOME=" + t.TempDir(),
		"XDG_CONFIG_HOME=" + t.TempDir(),
		"RADAR_HOME=" + t.TempDir(),
		"CLAUDE_PROJECT_DIR=" + home,
		"PATH=" + os.Getenv("PATH"),
	}
	cmd := exec.Command(huddleBin, "hook", "permit")
	cmd.Env = vars
	cmd.Dir = home
	cmd.Stdin = strings.NewReader(permitInputJSON)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the hook process: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	id := needIDOf(t, s)
	if code, body := post(t, s, "/api/needs/"+id+"/decide", s.token, `{"decision":"deny","message":"not from here"}`); code != 200 {
		t.Fatalf("the app's deny = %d %s", code, body)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the hook process ended with %v", err)
		}
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the hook process never ended")
	}
	want := `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"not from here"}}}` + "\n"
	if stdout.String() != want {
		t.Fatalf("the hook process printed %q, want the documented deny decision", stdout.String())
	}
}

// startingURL takes the POST and answers every poll 503: a hub that never finishes starting,
// polled again and again until the budget runs out.
func startingURL(t *testing.T) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			fmt.Fprint(w, `{"id":7}`)
			return
		}
		http.Error(w, "starting", http.StatusServiceUnavailable)
	}))
	t.Cleanup(s.Close)
	return s.URL
}

// restartingHub is a fake Huddle that restarts while the permit hook polls: its first life takes
// the POST and cuts the first wait mid-poll, it stays down for a while (the polls in between are
// refused), and its second life answers the first wait 503 and the next one allow.
type restartingHub struct {
	addr  string
	mu    sync.Mutex
	life  int
	waits int
}

func startRestartingHub(t *testing.T) *restartingHub {
	t.Helper()
	h := &restartingHub{addr: fmt.Sprintf("127.0.0.1:%d", ports.Free(t)), life: 1}
	h.serve(t)
	return h
}

// serve runs one life of the hub on its fixed address.
func (h *restartingHub) serve(t *testing.T) {
	t.Helper()
	listener, err := net.Listen("tcp", h.addr)
	if err != nil {
		t.Errorf("listen on %s: %v", h.addr, err)
		return
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.answer(t, listener, w, r)
	})}
	t.Cleanup(func() { _ = srv.Close() })
	go func() { _ = srv.Serve(listener) }()
}

func (h *restartingHub) answer(t *testing.T, listener net.Listener, w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	life := h.life
	if strings.HasSuffix(r.URL.Path, "/wait") {
		h.waits++
	}
	waits := h.waits
	h.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/needs":
		fmt.Fprint(w, `{"id":7}`)
	case r.URL.Path != "/api/needs/7/wait":
		http.NotFound(w, r)
	case life == 1:
		// the restart: the open poll is cut and the port closes until the second life
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
		_ = listener.Close()
		h.mu.Lock()
		h.life = 2
		h.mu.Unlock()
		go func() {
			time.Sleep(1500 * time.Millisecond)
			h.serve(t)
		}()
	case waits == 2:
		http.Error(w, "starting", http.StatusServiceUnavailable)
	default:
		fmt.Fprint(w, `{"id":7,"kind":"permission","state":"answered","decision":"allow"}`)
	}
}

func TestThePermitHookKeepsPollingAcrossAHubRestart(t *testing.T) {
	h := startRestartingHub(t)
	out := make(chan string, 1)
	go func() {
		stdout, _, _ := runHook(t, "permit", permitInputJSON, map[string]string{
			"HUDDLE_URL": "http://" + h.addr, "HUDDLE_PERMIT_BUDGET_MS": "15000"})
		out <- stdout
	}()
	select {
	case stdout := <-out:
		want := `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}` + "\n"
		if stdout != want {
			t.Fatalf("the hook printed %q, want the allow the hub gave after its restart", stdout)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the hook never came back from its poll")
	}
}
