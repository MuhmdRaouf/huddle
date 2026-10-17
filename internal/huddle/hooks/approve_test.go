package hooks

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// bashInput is one PreToolUse hook input for a Bash call.
func bashInput(command, session string) string {
	return fmt.Sprintf(`{"session_id":%q,"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":%q}}`, session, command)
}

// answeringHuddle is a fake Huddle that answers every approval request with one fixed body.
func answeringHuddle(t *testing.T, body string) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	t.Cleanup(s.Close)
	return s.URL
}

// TestTheApprovalHookAsksUnderASecond: a joined session and a rule that is on make Claude Code
// show its own permission prompt — the ask, with a short reason — and nothing else.
func TestTheApprovalHookAsksUnderASecond(t *testing.T) {
	s := startHuddle(t)
	for _, name := range []string{"api", "web"} {
		if code, body := op(t, s, "shop", "join", name, fmt.Sprintf(`{"role":%q,"claude_session":"claude-%s"}`, name, name)); code != 200 {
			t.Fatalf("join %s: %d %s", name, code, body)
		}
	}
	stdout, elapsed, _ := runHook(t, "approve", bashInput("git push --force origin main", "claude-api"),
		map[string]string{"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "shop", "HUDDLE_AS": "api"})
	if elapsed > time.Second {
		t.Errorf("approve took %s, want it under a second", elapsed)
	}
	var j approveOutput
	var extra map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &j); err != nil {
		t.Fatalf("approve output %q: %v", stdout, err)
	}
	if err := json.Unmarshal([]byte(stdout), &extra); err != nil {
		t.Fatalf("approve output %q: %v", stdout, err)
	}
	if len(extra) != 1 { // never a block, never a deny, nothing but the ask
		t.Errorf("approve output %q carries more than hookSpecificOutput", stdout)
	}
	if j.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Errorf("hookEventName = %q", j.HookSpecificOutput.HookEventName)
	}
	if j.HookSpecificOutput.PermissionDecision != "ask" {
		t.Errorf("permissionDecision = %q, want ask", j.HookSpecificOutput.PermissionDecision)
	}
	if !strings.Contains(j.HookSpecificOutput.PermissionDecisionReason, "force push") {
		t.Errorf("permissionDecisionReason = %q, want the rule in it", j.HookSpecificOutput.PermissionDecisionReason)
	}
}

// TestTheApprovalHookIsSilent: rule off, no rule matches, not in a huddle, not joined, Huddle
// down or mute, garbage on stdin — all under a second, all silent.
func TestTheApprovalHookIsSilent(t *testing.T) {
	s := startHuddle(t)
	for _, name := range []string{"api", "web"} {
		if code, body := op(t, s, "shop", "join", name, "{}"); code != 200 {
			t.Fatalf("join %s: %d %s", name, code, body)
		}
	}
	cases := []struct {
		what  string
		input string
		env   map[string]string
	}{
		{"rule off", bashInput("git push origin main", "claude-api"), map[string]string{"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "shop", "HUDDLE_AS": "api"}},
		{"no rule matches", bashInput("git status", "claude-api"), map[string]string{"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "shop", "HUDDLE_AS": "api"}},
		{"another tool", `{"tool_name":"Read","tool_input":{"file_path":"/x"}}`, map[string]string{"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "shop", "HUDDLE_AS": "api"}},
		{"not in a huddle", bashInput("rm -rf build", "claude-api"), nil},
		{"not joined (no credential)", bashInput("rm -rf build", "claude-api"), map[string]string{"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "shop", "HUDDLE_AS": "api", "HUDDLE_TOKEN": "", "XDG_STATE_HOME": t.TempDir()}},
		{"a session that never joined", bashInput("rm -rf build", "claude-api"), map[string]string{"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "shop", "HUDDLE_AS": "stranger"}},
		{"Huddle is down", bashInput("rm -rf build", "claude-api"), map[string]string{"HUDDLE_URL": "http://127.0.0.1:1", "HUDDLE_CHANNEL": "shop", "HUDDLE_AS": "api"}},
		{"Huddle is mute", bashInput("rm -rf build", "claude-api"), map[string]string{"HUDDLE_URL": muteURL(t), "HUDDLE_CHANNEL": "shop", "HUDDLE_AS": "api"}},
		{"garbage on stdin", "not json", map[string]string{"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "shop", "HUDDLE_AS": "api"}},
	}
	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			stdout, elapsed, _ := runHook(t, "approve", c.input, c.env)
			if strings.TrimSpace(stdout) != "" {
				t.Errorf("approve output %q, want silence", stdout)
			}
			if elapsed > time.Second {
				t.Errorf("approve took %s, want it under a second", elapsed)
			}
		})
	}
}

// TestTheApprovalHookNeverDecidesOnItsOwn: the permission decision is the server's, never the
// hook's — a server that says no, answers garbage or too much, or is never asked leaves the
// normal permission flow untouched.
func TestTheApprovalHookNeverDecidesOnItsOwn(t *testing.T) {
	t.Run("a server that says no", func(t *testing.T) {
		url := answeringHuddle(t, `{"ask":false}`)
		stdout, _, _ := runHook(t, "approve", bashInput("rm -rf build", "claude-api"),
			map[string]string{"HUDDLE_URL": url, "HUDDLE_CHANNEL": "shop", "HUDDLE_AS": "api"})
		if stdout != "" {
			t.Errorf("approve output %q, want silence", stdout)
		}
	})
	t.Run("a server that answers garbage", func(t *testing.T) {
		stdout, _, _ := runHook(t, "approve", bashInput("rm -rf build", "claude-api"),
			map[string]string{"HUDDLE_URL": garbageURL(t), "HUDDLE_CHANNEL": "shop", "HUDDLE_AS": "api"})
		if stdout != "" {
			t.Errorf("approve output %q, want silence", stdout)
		}
	})
	t.Run("a reason the server left out", func(t *testing.T) {
		url := answeringHuddle(t, `{"ask":true}`)
		stdout, _, _ := runHook(t, "approve", bashInput("rm -rf build", "claude-api"),
			map[string]string{"HUDDLE_URL": url, "HUDDLE_CHANNEL": "shop", "HUDDLE_AS": "api"})
		var j approveOutput
		if err := json.Unmarshal([]byte(stdout), &j); err != nil {
			t.Fatalf("approve output %q: %v", stdout, err)
		}
		if want := "Huddle: the owner asked to approve this command"; j.HookSpecificOutput.PermissionDecisionReason != want {
			t.Errorf("permissionDecisionReason = %q, want %q", j.HookSpecificOutput.PermissionDecisionReason, want)
		}
	})
	t.Run("a reason too long for the prompt", func(t *testing.T) {
		url := answeringHuddle(t, fmt.Sprintf(`{"ask":true,"reason":%q}`, strings.Repeat("x", 400)))
		stdout, _, _ := runHook(t, "approve", bashInput("rm -rf build", "claude-api"),
			map[string]string{"HUDDLE_URL": url, "HUDDLE_CHANNEL": "shop", "HUDDLE_AS": "api"})
		var j approveOutput
		if err := json.Unmarshal([]byte(stdout), &j); err != nil {
			t.Fatalf("approve output %q: %v", stdout, err)
		}
		if got := len(j.HookSpecificOutput.PermissionDecisionReason); got != 300 {
			t.Errorf("permissionDecisionReason is %d characters, want it sliced to 300", got)
		}
	})
	t.Run("a command that is not a string", func(t *testing.T) {
		url := answeringHuddle(t, `{"ask":true}`)
		stdout, _, _ := runHook(t, "approve",
			`{"session_id":"s","tool_name":"Bash","tool_input":{"command":{"rm":["-rf","build"]}}}`,
			map[string]string{"HUDDLE_URL": url, "HUDDLE_CHANNEL": "shop", "HUDDLE_AS": "api"})
		if stdout != "" {
			t.Errorf("approve output %q, want silence", stdout)
		}
	})
	t.Run("a command no rule could match is never sent anywhere", func(t *testing.T) {
		// a mute server: had the hook asked it, it would still be waiting
		stdout, elapsed, _ := runHook(t, "approve", bashInput("git status", "claude-api"),
			map[string]string{"HUDDLE_URL": muteURL(t), "HUDDLE_CHANNEL": "shop", "HUDDLE_AS": "api"})
		if stdout != "" {
			t.Errorf("approve output %q, want silence", stdout)
		}
		if elapsed > time.Duration(approveStdinMS)*time.Millisecond {
			t.Errorf("approve took %s, want it only as long as its stdin wait", elapsed)
		}
	})
}
