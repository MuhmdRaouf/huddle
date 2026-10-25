package hooks

// The report hook: one POST per Claude Code event to the local huddle's registry, with an
// outbox for the reports a dead server cannot take. The cases run the hook itself against a
// recording fake, a dead port and a mute server, in throwaway homes.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// reportEvents is the nine events hooks.json registers the report hook for.
var reportEvents = []string{
	"session-start", "user-prompt-submit", "pre-tool-use", "post-tool-use", "notification",
	"permission-request", "stop", "subagent-stop", "session-end",
}

// eventName maps a fixture's file name to the hook event name Claude Code sends.
var eventName = map[string]string{
	"session-start":      "SessionStart",
	"user-prompt-submit": "UserPromptSubmit",
	"pre-tool-use":       "PreToolUse",
	"post-tool-use":      "PostToolUse",
	"notification":       "Notification",
	"permission-request": "PermissionRequest",
	"stop":               "Stop",
	"subagent-stop":      "SubagentStop",
	"session-end":        "SessionEnd",
}

// reportFixture reads one event's stdin fixture with the test's transcript and cwd in place of
// the placeholders.
func reportFixture(t *testing.T, event, transcript, cwd string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "report", event+".json"))
	if err != nil {
		t.Fatalf("read the %s fixture: %v", event, err)
	}
	input := string(raw)
	input = strings.ReplaceAll(input, "__TRANSCRIPT__", transcript)
	input = strings.ReplaceAll(input, "__CWD__", cwd)
	return input
}

// reportHome answers a throwaway Huddle home whose config names this machine testbox.
func reportHome(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte("machine = \"testbox\"\n"), 0o600); err != nil {
		t.Fatalf("write the config: %v", err)
	}
	return root
}

// writeTranscript answers a transcript whose last assistant message ran glm-4.7 with its
// context 80 % full (120k input + 30k cache read + 10k cache write of a 200k window).
func writeTranscript(t *testing.T) string {
	t.Helper()
	body := `{"type":"user","message":{"role":"user","content":"hello"}}
{"type":"assistant","message":{"model":"claude-sonnet-4","usage":{"input_tokens":1000,"cache_read_input_tokens":2000,"cache_creation_input_tokens":0}}}
{"type":"assistant","message":{"model":"glm-4.7","usage":{"input_tokens":120000,"cache_read_input_tokens":30000,"cache_creation_input_tokens":10000}}}
{"type":"user","message":{"role":"user","content":"thanks"}}
`
	path := filepath.Join(t.TempDir(), "s-rep.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write the transcript: %v", err)
	}
	return path
}

// runReportHook runs the report hook in a throwaway world around one prepared home, so the
// cases can seed config.toml and outbox.jsonl first. Every variable a child process reads is
// pinned to a temp dir.
func runReportHook(t *testing.T, input, home string, env map[string]string) (string, time.Duration) {
	t.Helper()
	return runReportHookIn(context.Background(), t, input, home, env)
}

// runReportHookIn runs the report hook under ctx, the way the merged event hook runs it under
// the part's own deadline.
func runReportHookIn(ctx context.Context, t *testing.T, input, home string, env map[string]string) (string, time.Duration) {
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
	t0 := time.Now()
	Report(ctx, w, strings.NewReader(input), &out)
	return out.String(), time.Since(t0)
}

// registryPost is one POST the fake registry took.
type registryPost struct {
	Path  string
	Token string
	Body  map[string]any
	Raw   string
}

// fakeRegistry answers 200 to every POST and records what arrived.
type fakeRegistry struct {
	mu    sync.Mutex
	posts []registryPost
}

func (f *fakeRegistry) took() []registryPost {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]registryPost{}, f.posts...)
}

// startRegistry runs the recording fake until the test ends; it answers its URL.
func startRegistry(t *testing.T) (*fakeRegistry, string) {
	t.Helper()
	reg := &fakeRegistry{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := map[string]any{}
		_ = json.Unmarshal(raw, &body)
		reg.mu.Lock()
		reg.posts = append(reg.posts, registryPost{
			Path: r.URL.Path, Token: r.Header.Get("x-huddle-token"), Body: body, Raw: string(raw),
		})
		reg.mu.Unlock()
		w.WriteHeader(200)
	}))
	t.Cleanup(s.Close)
	return reg, s.URL
}

// field answers one body field as a string.
func field(body map[string]any, key string) string {
	value, _ := body[key].(string)
	return value
}

func TestTheReportHookPostsOneReportPerEventWithTheRegistryFields(t *testing.T) {
	for _, event := range reportEvents {
		t.Run(event, func(t *testing.T) {
			reg, url := startRegistry(t)
			home := reportHome(t)
			transcript := writeTranscript(t)
			cwd := t.TempDir() // no git here: the project falls back to the cwd, the branch to none
			stdout, elapsed := runReportHook(t, reportFixture(t, event, transcript, cwd), home,
				map[string]string{"HUDDLE_URL": url, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "me"})
			if stdout != "" {
				t.Errorf("the hook printed %q, want silence", stdout)
			}
			if elapsed > 2*time.Second {
				t.Errorf("the hook took %s, want it inside its budget", elapsed)
			}
			took := reg.took()
			if len(took) != 1 {
				t.Fatalf("the registry took %d POSTs, want 1", len(took))
			}
			post := took[0]
			if post.Path != "/api/registry/report" {
				t.Errorf("path = %q", post.Path)
			}
			if post.Token != suiteToken {
				t.Errorf("credential = %q, want the local one", post.Token)
			}
			body := post.Body
			if field(body, "session_id") != "s-rep" {
				t.Errorf("session_id = %q", field(body, "session_id"))
			}
			if field(body, "machine") != "testbox" {
				t.Errorf("machine = %q, want the config's name", field(body, "machine"))
			}
			if field(body, "project") != cwd {
				t.Errorf("project = %q, want the cwd when git says nothing", field(body, "project"))
			}
			if field(body, "branch") != "" {
				t.Errorf("branch = %q, want none outside a repo", field(body, "branch"))
			}
			if field(body, "model") != "glm-4.7" {
				t.Errorf("model = %q, want the last assistant message's", field(body, "model"))
			}
			if body["context_percent"] != float64(80) {
				t.Errorf("context_percent = %v, want 80", body["context_percent"])
			}
			if field(body, "cwd") != cwd {
				t.Errorf("cwd = %q", field(body, "cwd"))
			}
			if pid, _ := body["pid"].(float64); pid <= 0 {
				t.Errorf("pid = %v, want the session's process", body["pid"])
			}
			if field(body, "channel") != "hooks" {
				t.Errorf("channel = %q", field(body, "channel"))
			}
			if field(body, "last_event") != eventName[event] {
				t.Errorf("last_event = %q, want %s", field(body, "last_event"), eventName[event])
			}
			if at := field(body, "last_event_at"); !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`).MatchString(at) {
				t.Errorf("last_event_at = %q, want RFC 3339 UTC", at)
			}
			// title and task come from the prompt, so only the prompt's event carries them
			if event == "user-prompt-submit" {
				if field(body, "title") != "Fix the checkout flow before Friday's release" {
					t.Errorf("title = %q", field(body, "title"))
				}
				if field(body, "task") != "Fix the checkout flow before Friday's release" {
					t.Errorf("task = %q, want the prompt's first line", field(body, "task"))
				}
			} else {
				if _, has := body["title"]; has {
					t.Errorf("title = %q, want it absent without a prompt", field(body, "title"))
				}
				if _, has := body["task"]; has {
					t.Errorf("task = %q, want it absent without a prompt", field(body, "task"))
				}
			}
		})
	}
}

func TestTheReportHookTakesItsProjectAndBranchFromGit(t *testing.T) {
	reg, url := startRegistry(t)
	home := reportHome(t)
	transcript := writeTranscript(t)
	repo := t.TempDir()
	gitHome := t.TempDir()
	init := exec.Command("git", "init", "-q", "--initial-branch=report-suite", repo)
	init.Env = []string{"HOME=" + gitHome, "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1"}
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, out)
	}
	// one commit, so HEAD names a branch the way a session's repo does
	commit := exec.Command("git", "-C", repo, "-c", "user.email=t@example.com", "-c", "user.name=t",
		"commit", "--allow-empty", "-q", "-m", "one")
	commit.Env = init.Env
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v (%s)", err, out)
	}
	runReportHook(t, reportFixture(t, "post-tool-use", transcript, repo), home,
		map[string]string{"HUDDLE_URL": url})
	took := reg.took()
	if len(took) != 1 {
		t.Fatalf("the registry took %d POSTs, want 1", len(took))
	}
	body := took[0].Body
	if field(body, "project") != repo {
		t.Errorf("project = %q, want the checkout %q", field(body, "project"), repo)
	}
	if field(body, "branch") != "report-suite" {
		t.Errorf("branch = %q, want report-suite", field(body, "branch"))
	}
}

func TestTheReportHookCutsTheTitleAndTaskToEightyRunes(t *testing.T) {
	reg, url := startRegistry(t)
	home := reportHome(t)
	long := strings.Repeat("é", 120) + "\nand a second line"
	input := fmt.Sprintf(`{"session_id":"s-long","hook_event_name":"UserPromptSubmit","prompt":%q,"cwd":%q}`,
		long, t.TempDir())
	runReportHook(t, input, home, map[string]string{"HUDDLE_URL": url})
	took := reg.took()
	if len(took) != 1 {
		t.Fatalf("the registry took %d POSTs, want 1", len(took))
	}
	body := took[0].Body
	if title := field(body, "title"); jsfmt.UTF16Len(title) != 80 {
		t.Errorf("title is %d UTF-16 units (%q), want 80", jsfmt.UTF16Len(title), title)
	}
	if task := field(body, "task"); strings.ContainsRune(task, '\n') || jsfmt.UTF16Len(task) != 80 {
		t.Errorf("task = %q, want the prompt's first line cut to 80 units", task)
	}
}

// TestTheReportHookLetsNoInjectedPromptNameTheSession pins the wrapper rule: a prompt Claude
// Code itself submits — a background task's notification, a system reminder, `!` shell input —
// names no title and no task, and a prompt the person typed still names both.
func TestTheReportHookLetsNoInjectedPromptNameTheSession(t *testing.T) {
	cwd := t.TempDir()
	for _, prompt := range []string{
		"<task-notification>\n<task-id>x</task-id>\n<task-type>background</task-type>\n<status>completed</status>",
		"  <system-reminder>hi</system-reminder>",
		"<bash-input>pwd</bash-input>\n<bash-stdout>/repo</bash-stdout>",
	} {
		reg, url := startRegistry(t)
		input := fmt.Sprintf(`{"session_id":"s-wrap","hook_event_name":"UserPromptSubmit","prompt":%q,"cwd":%q}`,
			prompt, cwd)
		runReportHook(t, input, reportHome(t), map[string]string{"HUDDLE_URL": url})
		took := reg.took()
		if len(took) != 1 {
			t.Fatalf("the registry took %d POSTs, want 1", len(took))
		}
		if _, has := took[0].Body["title"]; has {
			t.Errorf("title = %q, want it absent for an injected prompt", took[0].Body["title"])
		}
		if _, has := took[0].Body["task"]; has {
			t.Errorf("task = %q, want it absent for an injected prompt", took[0].Body["task"])
		}
	}
	// the person's own words still name the session
	reg, url := startRegistry(t)
	input := fmt.Sprintf(`{"session_id":"s-typed","hook_event_name":"UserPromptSubmit","prompt":"fix the login bug\nmore","cwd":%q}`, cwd)
	runReportHook(t, input, reportHome(t), map[string]string{"HUDDLE_URL": url})
	took := reg.took()
	if len(took) != 1 {
		t.Fatalf("the registry took %d POSTs, want 1", len(took))
	}
	if got := field(took[0].Body, "title"); got != "fix the login bug\nmore" {
		t.Errorf("title = %q, want the prompt whole", got)
	}
	if got := field(took[0].Body, "task"); got != "fix the login bug" {
		t.Errorf("task = %q, want the prompt's first line", got)
	}
}

// TestTheReportCarriesTheToolName pins the tool half of a PreToolUse or PostToolUse report: the
// tool's name and its short target (the Bash command, the edited file, the MCP tool), cut to
// 120 runes, and nothing else of the tool's input.
func TestTheReportCarriesTheToolName(t *testing.T) {
	cwd := t.TempDir()
	long := strings.Repeat("ü", 150)
	cases := []struct {
		name, event, toolName, toolInput, wantTarget string
	}{
		{"bash", "PostToolUse", "Bash", `{"command":"git status","description":"show the tree"}`, "git status"},
		{"bash first line", "PreToolUse", "Bash", `{"command":"cat <<EOF > notes\nAPI_KEY=abc\nEOF"}`, "cat <<EOF > notes"},
		{"long command", "PreToolUse", "Bash", fmt.Sprintf(`{"command":%q}`, "echo "+long), "echo " + long[:len("ü")*115]},
		{"edit", "PostToolUse", "Edit", `{"file_path":"/repo/src/cart.ts","old_string":"a","new_string":"SECRET"}`, "/repo/src/cart.ts"},
		{"write", "PreToolUse", "Write", `{"file_path":"/repo/.env","content":"TOKEN=hunter2"}`, "/repo/.env"},
		{"notebook", "PostToolUse", "NotebookEdit", `{"notebook_path":"/repo/a.ipynb","new_source":"x"}`, "/repo/a.ipynb"},
		{"mcp", "PreToolUse", "mcp__huddle__publish", `{"channel":"c","text":"private words"}`, "publish"},
		{"other", "PostToolUse", "Glob", `{"pattern":"**/*.go"}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reg, url := startRegistry(t)
			input := fmt.Sprintf(`{"session_id":"s-tool","hook_event_name":%q,"tool_name":%q,"tool_input":%s,"cwd":%q}`,
				c.event, c.toolName, c.toolInput, cwd)
			runReportHook(t, input, reportHome(t), map[string]string{"HUDDLE_URL": url})
			took := reg.took()
			if len(took) != 1 {
				t.Fatalf("the registry took %d POSTs, want 1", len(took))
			}
			body := took[0].Body
			if got := field(body, "tool"); got != c.toolName {
				t.Errorf("tool = %q, want %q", got, c.toolName)
			}
			got, has := body["tool_target"]
			if c.wantTarget == "" && has {
				t.Errorf("tool_target = %q, want it absent", got)
			}
			if c.wantTarget != "" && got != c.wantTarget {
				t.Errorf("tool_target = %q, want %q", got, c.wantTarget)
			}
			for _, secret := range []string{"API_KEY", "SECRET", "hunter2", "private words", "show the tree"} {
				if strings.Contains(took[0].Raw, secret) {
					t.Errorf("the report carries %q from the tool's input: %s", secret, took[0].Raw)
				}
			}
		})
	}
	// an event that runs no tool names none
	reg, url := startRegistry(t)
	input := fmt.Sprintf(`{"session_id":"s-tool","hook_event_name":"Stop","tool_name":"Bash","tool_input":{"command":"ls"},"cwd":%q}`, cwd)
	runReportHook(t, input, reportHome(t), map[string]string{"HUDDLE_URL": url})
	for _, key := range []string{"tool", "tool_target"} {
		if got, has := reg.took()[0].Body[key]; has {
			t.Errorf("%s = %q on a Stop, want it absent", key, got)
		}
	}
}

// TestTheReportNamesClaudeCodeAsItsHarness pins the harness half: every report the Claude Code
// hook sends names its harness claude, so the Now list names it the way it names every other.
func TestTheReportNamesClaudeCodeAsItsHarness(t *testing.T) {
	cwd := t.TempDir()
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "Stop"} {
		t.Run(event, func(t *testing.T) {
			reg, url := startRegistry(t)
			input := fmt.Sprintf(`{"session_id":"s-cc","hook_event_name":%q,"cwd":%q}`, event, cwd)
			runReportHook(t, input, reportHome(t), map[string]string{"HUDDLE_URL": url})
			took := reg.took()
			if len(took) != 1 {
				t.Fatalf("the registry took %d POSTs, want 1", len(took))
			}
			if got := field(took[0].Body, "harness"); got != "claude" {
				t.Errorf("harness = %q, want %q", got, "claude")
			}
		})
	}
}

// TestTheReportCarriesThePromptLine pins the prompt half: a UserPromptSubmit report carries the
// typed prompt's first line, cut to 120 runes, and an injected prompt carries none.
func TestTheReportCarriesThePromptLine(t *testing.T) {
	cwd := t.TempDir()
	long := strings.Repeat("é", 150)
	cases := []struct {
		name, prompt, want string
	}{
		{"first line", "  fix the login bug  \nand then the tests", "fix the login bug"},
		{"long", long + "\nmore", long[:len("é")*120]},
		{"injected", "<system-reminder>hi</system-reminder>", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reg, url := startRegistry(t)
			input := fmt.Sprintf(`{"session_id":"s-line","hook_event_name":"UserPromptSubmit","prompt":%q,"cwd":%q}`,
				c.prompt, cwd)
			runReportHook(t, input, reportHome(t), map[string]string{"HUDDLE_URL": url})
			took := reg.took()
			if len(took) != 1 {
				t.Fatalf("the registry took %d POSTs, want 1", len(took))
			}
			got, has := took[0].Body["prompt_line"]
			if c.want == "" && has {
				t.Errorf("prompt_line = %q, want it absent", got)
			}
			if c.want != "" && got != c.want {
				t.Errorf("prompt_line = %q, want %q", got, c.want)
			}
		})
	}
}

func TestTheReportHookAppendsToTheOutboxWhenTheServerIsDown(t *testing.T) {
	home := reportHome(t)
	stdout, elapsed := runReportHook(t,
		reportFixture(t, "user-prompt-submit", writeTranscript(t), t.TempDir()), home,
		map[string]string{"HUDDLE_URL": downURL(t), "HUDDLE_CHANNEL": "hooks"})
	if stdout != "" {
		t.Errorf("the hook printed %q, want silence", stdout)
	}
	if elapsed > 2*time.Second {
		t.Errorf("the hook took %s, want it inside 2 s", elapsed)
	}
	outbox := filepath.Join(home, "outbox.jsonl")
	info, err := os.Stat(outbox)
	if err != nil {
		t.Fatalf("the outbox: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("outbox mode = %v, want 0600", info.Mode().Perm())
	}
	raw, err := os.ReadFile(outbox)
	if err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("the outbox holds %d lines, want the one report", len(lines))
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &body); err != nil {
		t.Fatalf("the outbox line is not JSON: %s", lines[0])
	}
	if field(body, "last_event") != "UserPromptSubmit" || field(body, "session_id") != "s-rep" {
		t.Errorf("the outboxed report = %v", body)
	}
}

func TestTheReportHookOutboxesWhenNoServerIsKnownAtAll(t *testing.T) {
	home := reportHome(t)
	runReportHook(t, reportFixture(t, "stop", writeTranscript(t), t.TempDir()), home,
		map[string]string{"HUDDLE_URL": ""})
	raw, err := os.ReadFile(filepath.Join(home, "outbox.jsonl"))
	if err != nil || len(raw) == 0 {
		t.Fatalf("the outbox after a report with no server: %v (%d bytes)", err, len(raw))
	}
}

func TestTheReportHookReplaysTheOutboxInOrderThenTruncatesIt(t *testing.T) {
	home := reportHome(t)
	transcript := writeTranscript(t)
	cwd := t.TempDir()
	down := map[string]string{"HUDDLE_URL": downURL(t), "HUDDLE_CHANNEL": "hooks"}
	for _, event := range []string{"session-start", "user-prompt-submit", "post-tool-use"} {
		runReportHook(t, reportFixture(t, event, transcript, cwd), home, down)
	}
	raw, _ := os.ReadFile(filepath.Join(home, "outbox.jsonl"))
	before := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(before) != 3 {
		t.Fatalf("the outbox holds %d lines after the outage, want 3", len(before))
	}
	// the server is back: the next report first replays those three, in order, then itself
	reg, url := startRegistry(t)
	runReportHook(t, reportFixture(t, "stop", transcript, cwd), home,
		map[string]string{"HUDDLE_URL": url, "HUDDLE_CHANNEL": "hooks"})
	took := reg.took()
	if len(took) != 4 {
		t.Fatalf("the registry took %d POSTs, want 3 replayed and 1 new", len(took))
	}
	want := []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "Stop"}
	for i, event := range want {
		if field(took[i].Body, "last_event") != event {
			t.Errorf("POST %d = %s, want %s", i, field(took[i].Body, "last_event"), event)
		}
	}
	// the replayed lines are the outbox's own bytes, unchanged
	for i, line := range before {
		if took[i].Raw != line {
			t.Errorf("replayed POST %d differs from the outbox line:\n%s\n%s", i, took[i].Raw, line)
		}
	}
	if left, err := os.ReadFile(filepath.Join(home, "outbox.jsonl")); err != nil || len(left) != 0 {
		t.Errorf("the outbox after the replay: %v (%d bytes), want it truncated", err, len(left))
	}
}

func TestAReplayThatFailsKeepsTheRestAndAddsTheNewReport(t *testing.T) {
	home := reportHome(t)
	transcript := writeTranscript(t)
	cwd := t.TempDir()
	for _, event := range []string{"session-start", "user-prompt-submit"} {
		runReportHook(t, reportFixture(t, event, transcript, cwd), home,
			map[string]string{"HUDDLE_URL": downURL(t), "HUDDLE_CHANNEL": "hooks"})
	}
	// still down: the third report replays nothing, keeps the two, adds itself
	runReportHook(t, reportFixture(t, "stop", transcript, cwd), home,
		map[string]string{"HUDDLE_URL": downURL(t), "HUDDLE_CHANNEL": "hooks"})
	raw, _ := os.ReadFile(filepath.Join(home, "outbox.jsonl"))
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("the outbox holds %d lines, want the two plus the new one", len(lines))
	}
	var last map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("the last outbox line: %v", err)
	}
	if field(last, "last_event") != "Stop" {
		t.Errorf("the last outbox line = %v, want the new report", last)
	}
}

// queueTogether queues the report s-00, then n more at the same moment, the way parallel tool
// calls fire a hook each, in one throwaway home whose server is down: each of the n replays
// s-00, fails and rewrites the outbox. It answers the outbox's lines. deliverReport is the one
// function the report hook and the external word land a report with.
func queueTogether(t *testing.T, n int) []string {
	t.Helper()
	root := reportHome(t)
	w := coverWorld(map[string]string{"HUDDLE_HOME": root, "HUDDLE_TOKEN": suiteToken})
	id := cli.Identity{URL: downURL(t)}
	land := func(i int) {
		rep := hub.Report{SessionID: fmt.Sprintf("s-%02d", i), LastEvent: "PostToolUse"}
		if err := deliverReport(context.Background(), w, id, rep, func() int { return reportBudgetMS }); err != nil {
			t.Errorf("report s-%02d: %v", i, err)
		}
	}
	land(0)
	start := make(chan struct{})
	var landing sync.WaitGroup
	for i := 1; i <= n; i++ {
		landing.Add(1)
		go func() {
			defer landing.Done()
			<-start
			land(i)
		}()
	}
	close(start)
	landing.Wait()
	raw, err := os.ReadFile(filepath.Join(root, "outbox.jsonl"))
	if err != nil {
		t.Fatalf("the outbox: %v", err)
	}
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
}

// wantEachQueuedOnce checks that lines are the reports s-00 to s-<n>, each a whole JSON line and
// each once.
func wantEachQueuedOnce(t *testing.T, lines []string, n int) {
	t.Helper()
	queued := map[string]int{}
	for _, line := range lines {
		var rep hub.Report
		if err := json.Unmarshal([]byte(line), &rep); err != nil {
			t.Errorf("the outbox holds a line that is not a report: %.80q", line)
			continue
		}
		queued[rep.SessionID]++
	}
	var notOnce []string
	for i := 0; i <= n; i++ {
		if id := fmt.Sprintf("s-%02d", i); queued[id] != 1 {
			notOnce = append(notOnce, fmt.Sprintf("%s x%d", id, queued[id]))
		}
	}
	if len(notOnce) > 0 {
		t.Errorf("%d of %d reports are not in the outbox exactly once: %v", len(notOnce), n+1, notOnce)
	}
	if len(lines) != n+1 {
		t.Errorf("the outbox holds %d lines, want %d", len(lines), n+1)
	}
}

func TestTwoReportsWhileTheHubIsDownBothQueue(t *testing.T) {
	wantEachQueuedOnce(t, queueTogether(t, 2), 2)
}

// Eight hooks fire at once; every one takes its turn on the lock inside outboxLockWait.
func TestManyReportsWhileTheHubIsDownAllQueue(t *testing.T) {
	const many = 8
	wantEachQueuedOnce(t, queueTogether(t, many), many)
}

// Every change to the outbox waits for its lock: while another hook holds it the outbox stays
// as it was, and the change lands once the lock is let go.
func TestEveryChangeToTheOutboxWaitsForItsLock(t *testing.T) {
	ctx := context.Background()
	changes := []struct {
		name string
		do   func(outbox string)
		want []string
	}{
		{"an append", func(outbox string) { appendOutbox(ctx, nil, outbox, "new") }, []string{"old", "new"}},
		{"a requeue", func(outbox string) { requeueOutbox(ctx, nil, outbox, []string{"old"}, "new") }, []string{"new"}},
		{"a drop of what was replayed", func(outbox string) { dropReplayed(outbox, []string{"old"}) }, nil},
	}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			outbox := filepath.Join(t.TempDir(), "outbox.jsonl")
			writeOutbox(outbox, []string{"old"})
			release := lockOutbox(outbox)
			done := make(chan struct{})
			go func() {
				defer close(done)
				change.do(outbox)
			}()
			select {
			case <-done:
				t.Error("the change finished while another hook held the lock")
			case <-time.After(20 * time.Millisecond):
			}
			if held := readOutbox(outbox); !slices.Equal(held, []string{"old"}) {
				t.Errorf("the outbox changed to %q while another hook held the lock", held)
			}
			release()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("the change did not land once the lock was let go")
			}
			if got := readOutbox(outbox); !slices.Equal(got, change.want) {
				t.Errorf("the outbox = %q, want %q", got, change.want)
			}
		})
	}
}

// A lock that cannot be taken costs no report: the line is queued without it.
func TestAReportQueuesWhenItsLockCannotBeTaken(t *testing.T) {
	t.Run("another hook holds it past the wait", func(t *testing.T) {
		outbox := filepath.Join(t.TempDir(), "outbox.jsonl")
		defer lockOutbox(outbox)()
		t0 := time.Now()
		appendOutbox(context.Background(), nil, outbox, "queued")
		if waited := time.Since(t0); waited < outboxLockWait {
			t.Errorf("the report waited %s for the lock, want all of %s", waited, outboxLockWait)
		}
		if got := readOutbox(outbox); !slices.Equal(got, []string{"queued"}) {
			t.Errorf("the outbox = %q, want the one report", got)
		}
	})
	t.Run("its file cannot be opened", func(t *testing.T) {
		outbox := filepath.Join(t.TempDir(), "outbox.jsonl")
		if err := os.Mkdir(outbox+".lock", 0o700); err != nil {
			t.Fatalf("take the lock file's name: %v", err)
		}
		appendOutbox(context.Background(), nil, outbox, "queued")
		if got := readOutbox(outbox); !slices.Equal(got, []string{"queued"}) {
			t.Errorf("the outbox = %q, want the one report", got)
		}
	})
}

func TestTheOutboxDropsItsOldestLinesAtFiveMiBAndLogsOneLine(t *testing.T) {
	home := reportHome(t)
	outbox := filepath.Join(home, "outbox.jsonl")
	var flood strings.Builder
	for i := 0; i < 10; i++ {
		flood.WriteString(fmt.Sprintf(`{"marker":"m%02d","pad":%q}`+"\n", i, strings.Repeat("x", 600*1024)))
	}
	if err := os.WriteFile(outbox, []byte(flood.String()), 0o600); err != nil {
		t.Fatalf("flood the outbox: %v", err)
	}
	runReportHook(t, reportFixture(t, "stop", writeTranscript(t), t.TempDir()), home,
		map[string]string{"HUDDLE_URL": downURL(t), "HUDDLE_CHANNEL": "hooks"})
	info, err := os.Stat(outbox)
	if err != nil {
		t.Fatalf("the outbox: %v", err)
	}
	if info.Size() > 5*1024*1024 {
		t.Errorf("the outbox is %d bytes, want it capped at 5 MiB", info.Size())
	}
	raw, _ := os.ReadFile(outbox)
	text := string(raw)
	if strings.Contains(text, `"m00"`) {
		t.Error("the oldest line is still in the outbox, want the oldest dropped")
	}
	if !strings.Contains(text, `"m09"`) {
		t.Error("the newest flood line is gone, want the newest kept")
	}
	if !strings.Contains(text, `"last_event":"Stop"`) {
		t.Error("the new report is not in the outbox")
	}
	log, err := os.ReadFile(filepath.Join(home, "hooks.log"))
	if err != nil || !strings.Contains(string(log), "report: outbox over its 5 MiB cap: dropped") {
		t.Errorf("hooks.log = %v %q, want one cap line", err, string(log))
	}
}

func TestTheReportHookNeverExceedsTwoSecondsAgainstAHungServer(t *testing.T) {
	home := reportHome(t)
	_, elapsed := runReportHook(t, reportFixture(t, "post-tool-use", writeTranscript(t), t.TempDir()),
		home, map[string]string{"HUDDLE_URL": muteURL(t), "HUDDLE_CHANNEL": "hooks"})
	if elapsed >= 2*time.Second {
		t.Errorf("the hook took %s against a hung server, want it under 2 s", elapsed)
	}
	raw, err := os.ReadFile(filepath.Join(home, "outbox.jsonl"))
	if err != nil || len(raw) == 0 {
		t.Errorf("the outbox after a hung server: %v (%d bytes), want the report in it", err, len(raw))
	}
}

// The merged event hook drops a part at its own deadline, which can come before the report's
// 1.9 s budget ends (UserPromptSubmit grants it 1.3 s): the report must give up on a hung server
// in time to queue itself, or it vanishes with the dropped part.
func TestTheReportHookQueuesBeforeItsCallersDeadline(t *testing.T) {
	home := reportHome(t)
	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	runReportHookIn(ctx, t, reportFixture(t, "user-prompt-submit", writeTranscript(t), t.TempDir()),
		home, map[string]string{"HUDDLE_URL": muteURL(t), "HUDDLE_CHANNEL": "hooks"})
	if late := time.Since(deadline); late > 0 {
		t.Errorf("the hook returned %s after its caller's deadline, want before it", late)
	}
	raw, err := os.ReadFile(filepath.Join(home, "outbox.jsonl"))
	if err != nil || len(raw) == 0 {
		t.Errorf("the outbox after a hung server: %v (%d bytes), want the report in it", err, len(raw))
	}
}

// TestTheStopReportCarriesTheDigest pins the one event that carries a digest (SPEC 8): the
// Stop hook builds it from the transcript it already reads, and no other event carries one.
func TestTheStopReportCarriesTheDigest(t *testing.T) {
	reg, url := startRegistry(t)
	home := reportHome(t)
	cwd := t.TempDir()
	path := filepath.Join(t.TempDir(), "s-dig.jsonl")
	body := `{"type":"user","message":{"role":"user","content":"Ship the billing fix"}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Decided: proration stays on the invoice."}]}}
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write the transcript: %v", err)
	}
	runReportHook(t, reportFixture(t, "stop", path, cwd), home, map[string]string{"HUDDLE_URL": url})
	took := reg.took()
	if len(took) != 1 {
		t.Fatalf("the registry took %d POSTs, want 1", len(took))
	}
	want := "Goal: Ship the billing fix\nDecisions:\n- Decided: proration stays on the invoice."
	if got := field(took[0].Body, "digest"); got != want {
		t.Errorf("the Stop report's digest = %q, want %q", got, want)
	}
	// the same transcript on a UserPromptSubmit carries no digest at all
	reg2, url2 := startRegistry(t)
	runReportHook(t, reportFixture(t, "user-prompt-submit", path, cwd), home,
		map[string]string{"HUDDLE_URL": url2})
	took = reg2.took()
	if len(took) != 1 {
		t.Fatalf("the registry took %d POSTs, want 1", len(took))
	}
	if _, has := took[0].Body["digest"]; has {
		t.Errorf("the UserPromptSubmit report carries a digest: %v", took[0].Body["digest"])
	}
}

// ── the pieces the report is made of ─────────────────────────────────────────

func TestSessionPidWalksPastShellsAndTheLauncher(t *testing.T) {
	commands := map[int]string{
		10: "sh -c sh /plugin/dist/run hook report",
		11: "sh /plugin/dist/run hook report",
		12: "/usr/local/bin/claude",
		20: "zsh -i",
		21: "/usr/bin/node /usr/local/bin/claude",
	}
	parents := map[int]int{10: 11, 11: 12, 20: 21}
	command := func(pid int) (string, bool) { line, ok := commands[pid]; return line, ok }
	parent := func(pid int) (int, bool) {
		if p, ok := parents[pid]; ok {
			return p, true
		}
		return 0, false
	}
	if got := sessionPid(10, parent, command); got != 12 {
		t.Errorf("sessionPid through the hook chain = %d, want 12", got)
	}
	if got := sessionPid(20, parent, command); got != 21 {
		t.Errorf("sessionPid through a login shell = %d, want 21", got)
	}
	// a chain that cannot be read, and one that never leaves its shells, answer no session
	if got := sessionPid(99, parent, command); got != 0 {
		t.Errorf("sessionPid of an unknown pid = %d, want 0", got)
	}
	loop := func(int) (int, bool) { return 30, true }
	if got := sessionPid(30, loop, command); got != 0 {
		t.Errorf("sessionPid of a shell loop = %d, want 0", got)
	}
}

func TestLastModelOfReadsOnlyTheTail(t *testing.T) {
	dir := t.TempDir()
	small := filepath.Join(dir, "small.jsonl")
	body := `{"type":"assistant","message":{"model":"claude-sonnet-4","usage":{"input_tokens":1000}}}
{"type":"user","message":{"content":"hi"}}
{"type":"assistant","message":{"model":"glm-4.7","usage":{"input_tokens":9000,"cache_read_input_tokens":1000}}}
`
	if err := os.WriteFile(small, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	model, used := lastModelOf(small)
	if model != "glm-4.7" || used != 10000 {
		t.Errorf("lastModelOf = (%q, %d), want the last assistant's glm-4.7 and 10000", model, used)
	}
	// the only assistant message sits before the 64 KiB tail: it is not read
	tailOnly := filepath.Join(dir, "tail.jsonl")
	var big strings.Builder
	big.WriteString(`{"type":"assistant","message":{"model":"kimi-k2","usage":{"input_tokens":5}}}` + "\n")
	big.WriteString(strings.Repeat(`{"type":"user","message":{"content":"`+strings.Repeat("y", 200)+`"}}`+"\n", 400))
	if err := os.WriteFile(tailOnly, []byte(big.String()), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if info, err := os.Stat(tailOnly); err != nil || info.Size() <= 64*1024 {
		t.Fatalf("the tail fixture is %v bytes, want it past 64 KiB", info)
	}
	if model, _ := lastModelOf(tailOnly); model != "" {
		t.Errorf("lastModelOf read past the tail: %q", model)
	}
	if model, _ := lastModelOf(filepath.Join(dir, "missing.jsonl")); model != "" {
		t.Errorf("lastModelOf of a missing file = %q, want nothing", model)
	}
}

func TestContextPercentMirrorsRadar(t *testing.T) {
	cases := []struct {
		model string
		used  int64
		want  float64
	}{
		{"glm-4.7", 100_000, 50},
		{"glm-4.7", 250_000, 25},             // over the small window: the call itself says 1M
		{"claude-sonnet-4[1m]", 500_000, 50}, // the model id says 1M
		{"glm-4.7", 0, 0},
	}
	for _, c := range cases {
		if got := contextPercentOf(c.model, c.used); got != c.want {
			t.Errorf("contextPercentOf(%q, %d) = %v, want %v", c.model, c.used, got, c.want)
		}
	}
}
