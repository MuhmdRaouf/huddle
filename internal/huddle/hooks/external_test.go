package hooks

// The external word: `huddle hook external --harness <name> --event <event>` maps another
// agent's hook payload onto the report the registry takes, tags the session with its harness,
// and answers in silence whatever happens. The payloads are the recorded shapes the agents'
// docs and sources document (the discovery file lists the citations); the fixtures are
// synthetic and carry no secret.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
)

// externalCase is one agent's payload and what the registry must read out of it.
type externalCase struct {
	name      string // the harness word's --harness
	event     string // the word's --event
	fixture   string // the payload file under testdata/external
	session   string
	lastEvent string // the report name the agent's event maps to
	harness   string
	title     string // "" when the payload carries no prompt
	task      string // "" when neither a prompt nor a tool name says one
}

// externalCases are the recorded payloads, one per agent and event kind. The files hold
// __CWD__ where the agent names its directory; each run fills in its own temp dir in memory.
func externalCases() []externalCase {
	return []externalCase{
		{
			name: "codex", event: "PreToolUse", session: "cx-1", lastEvent: "PreToolUse", harness: "codex",
			task:    "shell",
			fixture: "codex-pre-tool-use.json",
		},
		{
			name: "codex", event: "SessionEnd", session: "cx-1", lastEvent: "SessionEnd", harness: "codex",
			fixture: "codex-session-end.json",
		},
		{
			name: "gemini", event: "BeforeAgent", session: "gm-1", lastEvent: "UserPromptSubmit", harness: "gemini",
			title: "Write the release notes", task: "Write the release notes",
			fixture: "gemini-before-agent.json",
		},
		{
			name: "gemini", event: "AfterAgent", session: "gm-1", lastEvent: "Stop", harness: "gemini",
			title: "Write the release notes", task: "Write the release notes",
			fixture: "gemini-after-agent.json",
		},
		{
			name: "qwen", event: "UserPromptSubmit", session: "qw-1", lastEvent: "UserPromptSubmit", harness: "qwen",
			title: "Fix the failing test", task: "Fix the failing test",
			fixture: "qwen-user-prompt-submit.json",
		},
		{
			name: "crush", event: "PreToolUse", session: "cr-1", lastEvent: "PreToolUse", harness: "crush",
			task:    "bash",
			fixture: "crush-pre-tool-use.json",
		},
		{
			name: "opencode", event: "session.created", session: "oc-1", lastEvent: "SessionStart", harness: "opencode",
			fixture: "opencode-session-created.json",
		},
		{
			name: "opencode", event: "session.idle", session: "oc-1", lastEvent: "Stop", harness: "opencode",
			fixture: "opencode-session-idle.json",
		},
	}
}

func TestTheExternalWordReportsEachAgentUnderItsHarness(t *testing.T) {
	for _, tc := range externalCases() {
		t.Run(tc.name+" "+tc.event, func(t *testing.T) {
			reg, url := startRegistry(t)
			home := reportHome(t)
			raw, err := os.ReadFile(filepath.Join("testdata", "external", tc.fixture))
			if err != nil {
				t.Fatalf("read the fixture: %v", err)
			}
			payload := strings.ReplaceAll(string(raw), "__CWD__", t.TempDir())
			stdout := runExternalHook(t, payload, tc.name, tc.event, home,
				map[string]string{"HUDDLE_URL": url, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "me"})
			if stdout != "" {
				t.Errorf("the word printed %q, want silence", stdout)
			}
			took := reg.took()
			if len(took) != 1 {
				t.Fatalf("the registry took %d POSTs, want 1", len(took))
			}
			body := took[0].Body
			if field(body, "session_id") != tc.session {
				t.Errorf("session_id = %q, want %q", field(body, "session_id"), tc.session)
			}
			if field(body, "harness") != tc.harness {
				t.Errorf("harness = %q, want %q", field(body, "harness"), tc.harness)
			}
			if field(body, "last_event") != tc.lastEvent {
				t.Errorf("last_event = %q, want %q", field(body, "last_event"), tc.lastEvent)
			}
			if field(body, "machine") != "testbox" {
				t.Errorf("machine = %q, want the config's name", field(body, "machine"))
			}
			if field(body, "channel") != "hooks" {
				t.Errorf("channel = %q", field(body, "channel"))
			}
			if _, has := body["pid"]; has {
				t.Errorf("pid = %v, want it absent: no pid check for another agent's process", body["pid"])
			}
			if _, has := body["model"]; has {
				t.Errorf("model = %v, want it absent: another agent's transcript is not read", body["model"])
			}
			if got := field(body, "title"); got != tc.title {
				t.Errorf("title = %q, want %q", got, tc.title)
			}
			if got := field(body, "task"); got != tc.task {
				t.Errorf("task = %q, want %q", got, tc.task)
			}
		})
	}
}

func TestTheExternalWordSaysNothingWhenThereIsNothingToSay(t *testing.T) {
	cases := []struct {
		label   string
		name    string
		event   string
		payload string
	}{
		{"an unknown harness", "notaharness", "SessionStart", `{"session_id":"x-1"}`},
		{"an event the harness does not report", "crush", "SessionEnd", `{"session_id":"cr-2"}`},
		{"a payload without a session", "codex", "PreToolUse", `{"hook_event_name":"PreToolUse","cwd":"/tmp"}`},
		{"an empty event and payload", "codex", "", `{}`},
		{"input that is not JSON", "codex", "PreToolUse", "not json at all"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			reg, url := startRegistry(t)
			home := reportHome(t)
			stdout := runExternalHook(t, tc.payload, tc.name, tc.event, home,
				map[string]string{"HUDDLE_URL": url})
			if stdout != "" {
				t.Errorf("the word printed %q, want silence", stdout)
			}
			if took := reg.took(); len(took) != 0 {
				t.Errorf("the registry took %d POSTs, want none", len(took))
			}
		})
	}
}

// runExternalHook runs the external word in a throwaway world around one prepared home, the
// way the report hook's cases do.
func runExternalHook(t *testing.T, payload, name, event, home string, env map[string]string) string {
	t.Helper()
	vars := map[string]string{
		"HOME":                   t.TempDir(),
		"HUDDLE_HOME":            home,
		"HUDDLE_NO_PROJECT_CRED": "1",
		"HUDDLE_TOKEN":           suiteToken,
		"XDG_STATE_HOME":         t.TempDir(),
		"XDG_DATA_HOME":          t.TempDir(),
		"XDG_CONFIG_HOME":        t.TempDir(),
		"RADAR_HOME":             t.TempDir(),
		"CLAUDE_PROJECT_DIR":     home,
		"PATH":                   os.Getenv("PATH"),
	}
	for key, value := range env {
		if value == "" {
			delete(vars, key)
			continue
		}
		vars[key] = value
	}
	entries := make([]string, 0, len(vars))
	for key, value := range vars {
		entries = append(entries, key+"="+value)
	}
	w := &cli.World{
		Getenv:  func(key string) string { return vars[key] },
		Environ: func() []string { return append([]string{}, entries...) },
		Dir:     home,
		Now:     func() time.Time { return time.Now() },
		Bin:     huddleBin,
	}
	var out strings.Builder
	External(context.Background(), w, name, event, strings.NewReader(payload), &out)
	return out.String()
}
