package hooks

// The PermissionRequest hook: one tool call becomes a needs-you item the owner's app may
// allow or deny while the hook holds a long poll. The cases run the hook against the real
// server (in process, and once as the child process Claude Code runs), against dead, mute and
// garbage Huddles, and past the hook's own budget — silent whenever nothing arrives, so the
// terminal's own prompt is the fallback.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
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
