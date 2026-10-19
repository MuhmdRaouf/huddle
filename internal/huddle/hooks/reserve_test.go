package hooks

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The PreToolUse reserve part: a session about to edit a path another live session in its
// channel holds hears one line of context naming the holder — and nothing else ever happens:
// its own reservations, a path nobody holds, a foreign tool, a missing huddle are all silence,
// and the exit code stays 0 whatever happens.

// editInput is one PreToolUse hook input for an editing tool.
func editInput(tool, path, session, cwd string) string {
	return fmt.Sprintf(`{"session_id":"s-%s","hook_event_name":"PreToolUse","tool_name":%q,"tool_input":{"file_path":%q},"cwd":%q}`,
		session, tool, path, cwd)
}

// joinedAB are two sessions of the hooks channel, a and b.
func joinedAB(t *testing.T, s *huddleServer) {
	t.Helper()
	for _, name := range []string{"a", "b"} {
		if code, body := op(t, s, "hooks", "join", name, "{}"); code != 200 {
			t.Fatalf("join %s: %d %s", name, code, body)
		}
	}
}

// reserveAs reserves paths as a session of the hooks channel, through the op the MCP tool runs.
func reserveAs(t *testing.T, s *huddleServer, as, body string) {
	t.Helper()
	if code, raw := op(t, s, "hooks", "reserve", as, body); code != 200 {
		t.Fatalf("reserve as %s: %d %s", as, code, raw)
	}
}

// TestTheReserveHookWarnsWhenAnotherLiveSessionHoldsThePath: the warning names the holder, the
// moment it lapses and the way to coordinate; it is context, never a decision.
func TestTheReserveHookWarnsWhenAnotherLiveSessionHoldsThePath(t *testing.T) {
	s := startHuddle(t)
	joinedAB(t, s)
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(repo, "src", "cart.ts")
	reserveAs(t, s, "a", fmt.Sprintf(`{"paths": [%q], "note": "rewriting the cart"}`, file))
	stdout, elapsed, _ := runHook(t, "reserve", editInput("Edit", file, "b", repo),
		map[string]string{"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "b"})
	if elapsed > time.Second {
		t.Errorf("pre-tool-use took %s, want it inside the deadline", elapsed)
	}
	neverBlocks(t, stdout)
	ctx := hookContext(t, stdout)
	if !strings.HasPrefix(ctx, file+" is reserved by a until ") {
		t.Errorf("context = %q, want the holder line", ctx)
	}
	if !strings.Contains(ctx, "(rewriting the cart)") ||
		!strings.Contains(ctx, "Coordinate with them (huddle send a …) before changing it.") {
		t.Errorf("context = %q, want the note and the way to coordinate", ctx)
	}
	// a subagent of the holder is the holder: its parent's reservation draws no warning
	sub, _, _ := runHook(t, "reserve", editInput("Edit", file, "sub", repo),
		map[string]string{"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "a.helper"})
	if sub != "" {
		t.Errorf("the holder's subagent printed %q, want silence", sub)
	}
}

// TestTheReserveHookIsSilent: the holder itself, a path nobody holds, a non-editing tool, a
// tool input without a file, garbage on stdin, not in a huddle, a session that never joined, a
// Huddle that is down or never answers, and an answer of not held (what an expired reservation
// becomes) — all silent, all fast.
func TestTheReserveHookIsSilent(t *testing.T) {
	s := startHuddle(t)
	joinedAB(t, s)
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	held := filepath.Join(repo, "src", "held.ts")
	mine := filepath.Join(repo, "src", "mine.ts")
	reserveAs(t, s, "a", fmt.Sprintf(`{"paths": [%q]}`, held))
	reserveAs(t, s, "b", fmt.Sprintf(`{"paths": [%q]}`, mine))
	env := map[string]string{"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "hooks"}
	cases := []struct {
		what  string
		input string
		env   map[string]string
	}{
		{"held by self", editInput("Edit", mine, "b", repo), env},
		{"not held", editInput("Edit", filepath.Join(repo, "src", "free.ts"), "b", repo), env},
		{"another tool", fmt.Sprintf(`{"session_id":"s-b","hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":%q},"cwd":%q}`, held, repo), env},
		{"no file named", fmt.Sprintf(`{"session_id":"s-b","hook_event_name":"PreToolUse","tool_name":"Edit","tool_input":{"command":"ls"},"cwd":%q}`, repo), env},
		{"garbage on stdin", "not json", env},
		{"not in a huddle", editInput("Edit", held, "b", repo), nil},
		{"a session that never joined", editInput("Edit", held, "b", repo), map[string]string{"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "stranger"}},
		{"Huddle is down", editInput("Edit", held, "b", repo), map[string]string{"HUDDLE_URL": downURL(t), "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "b"}},
		{"Huddle never answers", editInput("Edit", held, "b", repo), map[string]string{"HUDDLE_URL": muteURL(t), "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "b"}},
	}
	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			stdout, elapsed, _ := runHook(t, "reserve", c.input, c.env)
			if strings.TrimSpace(stdout) != "" {
				t.Errorf("reserve hook printed %q, want silence", stdout)
			}
			if elapsed > time.Second {
				t.Errorf("took %s, want it inside the deadline", elapsed)
			}
		})
	}
	t.Run("an expired reservation", func(t *testing.T) {
		// what the server answers once a reservation lapsed: held=false — silence
		fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"result":{"held":false,"path":"/src/cart.ts"},"text":""}`)
		}))
		t.Cleanup(fake.Close)
		stdout, _, _ := runHook(t, "reserve", editInput("Edit", held, "b", repo),
			map[string]string{"HUDDLE_URL": fake.URL, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "b"})
		if stdout != "" {
			t.Errorf("an expired reservation printed %q, want silence", stdout)
		}
	})
}

// TestTheReserveHookDirectoryReservation: a reservation on a directory warns for a file under
// it, never for one beside it.
func TestTheReserveHookDirectoryReservation(t *testing.T) {
	s := startHuddle(t)
	joinedAB(t, s)
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	reserveAs(t, s, "a", fmt.Sprintf(`{"paths": [%q]}`, filepath.Join(repo, "src")))
	under, _, _ := runHook(t, "reserve", editInput("Write", filepath.Join(repo, "src", "deep", "new.ts"), "b", repo),
		map[string]string{"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "b"})
	if ctx := hookContext(t, under); !strings.Contains(ctx, "is reserved by a until") {
		t.Errorf("context = %q, want the directory's holder for the file under it", ctx)
	}
	beside, _, _ := runHook(t, "reserve", editInput("Edit", filepath.Join(repo, "web", "app.ts"), "b", repo),
		map[string]string{"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "b"})
	if beside != "" {
		t.Errorf("a file beside the reserved directory printed %q, want silence", beside)
	}
}

// TestTheReserveHookNamesTheEventItServesWhenTheInputDidNot: a hook input without
// hook_event_name still answers under PreToolUse.
func TestTheReserveHookNamesTheEventItServesWhenTheInputDidNot(t *testing.T) {
	s := startHuddle(t)
	joinedAB(t, s)
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(repo, "src", "cart.ts")
	reserveAs(t, s, "a", fmt.Sprintf(`{"paths": [%q]}`, file))
	input := fmt.Sprintf(`{"session_id":"s-b","tool_name":"Edit","tool_input":{"file_path":%q},"cwd":%q}`, file, repo)
	stdout, _, _ := runHook(t, "reserve", input,
		map[string]string{"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "b"})
	var j struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(stdout), &j); err != nil {
		t.Fatalf("output %q: %v", stdout, err)
	}
	if j.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Errorf("hookEventName = %q, want PreToolUse", j.HookSpecificOutput.HookEventName)
	}
	if !regexp.MustCompile(`is reserved by a until \d{4}-`).MatchString(j.HookSpecificOutput.AdditionalContext) {
		t.Errorf("context = %q", j.HookSpecificOutput.AdditionalContext)
	}
}

// TestTheReservePartStaysInsideItsBudget: the plan grants the reserve part 300 ms, the part's
// own deadlines sit inside that, and the merged pre-tool-use process still answers inside
// hooks.json's 5 s when the server never answers.
func TestTheReservePartStaysInsideItsBudget(t *testing.T) {
	budget, ok := largestBudget("reserve")
	if !ok || budget != 300*time.Millisecond {
		t.Fatalf("the plan grants reserve %s, want 300ms", budget)
	}
	worst := (reserveStdinMS + reserveOriginMS + reserveFetchMS) * time.Millisecond
	if worst > budget {
		t.Errorf("the reserve part can take %s inside itself, its budget is %s", worst, budget)
	}
	world := t.TempDir()
	cmd := exec.Command(huddleBin, "hook", "pre-tool-use")
	cmd.Dir = world
	cmd.Env = []string{
		"HOME=" + t.TempDir(), "HUDDLE_HOME=" + world, "CLAUDE_PROJECT_DIR=" + world,
		"HUDDLE_NO_PROJECT_CRED=1", "HUDDLE_TOKEN=" + suiteToken, "HUDDLE_AUTOSTART=0",
		"XDG_STATE_HOME=" + t.TempDir(), "RADAR_HOME=" + t.TempDir(),
		"HUDDLE_URL=" + muteURL(t), "HUDDLE_CHANNEL=hooks", "HUDDLE_AS=b",
		"PATH=" + os.Getenv("PATH"),
	}
	cmd.Stdin = strings.NewReader(`{"session_id":"s-b","tool_name":"Edit","tool_input":{"file_path":"/x.ts"}}`)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	started := time.Now()
	if err := cmd.Run(); err != nil {
		t.Fatalf("huddle hook pre-tool-use: %v", err)
	}
	if got := time.Since(started); got > 5500*time.Millisecond {
		t.Errorf("pre-tool-use took %s, want it inside hooks.json's 5 s", got)
	}
	if stderr.String() != "" {
		t.Errorf("the hook wrote to stderr: %q", stderr.String())
	}
	neverBlocks(t, stdout.String())
}

// TestTheReservationsListAnswersTheDashboard: the channel route the Overview reads.
func TestTheReservationsListAnswersTheDashboard(t *testing.T) {
	s := startHuddle(t)
	joinedAB(t, s)
	reserveAs(t, s, "a", `{"paths": ["/r/dash.ts"], "note": "the schema"}`)
	req, err := http.NewRequest(http.MethodGet, s.base+"/api/c/hooks/reservations", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-huddle-token", s.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var view struct {
		Reservations []struct {
			Path    string  `json:"path"`
			Holder  string  `json:"holder"`
			Note    *string `json:"note"`
			Created string  `json:"created"`
			Expires string  `json:"expires"`
		} `json:"reservations"`
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("reservations %s: %v", raw, err)
	}
	if len(view.Reservations) != 1 || view.Reservations[0].Path != "/r/dash.ts" ||
		view.Reservations[0].Holder != "a" || view.Reservations[0].Note == nil {
		t.Errorf("reservations = %s", raw)
	}
}
