package hooks

// The hooks run inside every session and must never block one: silent on any failure, back
// within the deadline the hook plan grants their part of the event, never a blocking decision —
// the Stop hook's deliberate block, only while asks await a reply, excepted. Each case runs the
// hook itself against a Huddle that is down, slow (accepts and never answers), answers garbage,
// or with odd env; the block cases run against the real server. The cases run the hook functions
// in process (their entry points, run as child processes by Claude Code, are covered by their own
// case below: exit 0, nothing on stderr).

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	mathrand "math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/hookplan"
	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
	"github.com/MuhmdRaouf/huddle/internal/huddle/server"
)

// huddleBin is the huddle binary TestMain builds once: the entry-point cases run it as Claude
// Code runs it, and the servers the session-start hook starts run it with the serve entry.
var huddleBin string

// suiteToken is the root credential this run's servers and hook runs share, the one the plugin's
// own suite hands every child it spawns: one credential per run, never a real one.
var suiteToken string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "huddle-hooks-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	huddleBin = filepath.Join(dir, "huddle")
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	suiteToken = "test-" + hex.EncodeToString(raw)
	build := exec.Command("go", "build", "-o", huddleBin, "github.com/MuhmdRaouf/huddle/cmd/huddle")
	build.Dir = "../../.." // the module root, from this package's directory
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building the huddle binary:", err)
		os.Exit(1)
	}
	code := m.Run()
	// os.Exit would skip a defer, so the build dir goes here: the suite must leave no temp behind.
	os.RemoveAll(dir)
	os.Exit(code)
}

// hookByName runs one hook by its entry-point name.
var hookByName = map[string]func(context.Context, *cli.World, io.Reader, io.Writer){
	"approve":       Approve,
	"listen":        Listen,
	"permit":        Permit,
	"session-start": SessionStart,
	"stop":          Stop,
}

// runHook runs one hook in a throwaway world: cwd and HUDDLE_HOME in a temp dir, so no real
// huddle.json or seen state is read or written. An env value of "" removes the variable. It
// answers what the hook printed, how long it took, and the world it ran in.
func runHook(t *testing.T, name string, input string, env map[string]string) (string, time.Duration, string) {
	t.Helper()
	hook, ok := hookByName[name]
	if !ok {
		t.Fatalf("no hook %s", name)
	}
	world := t.TempDir()
	vars := map[string]string{
		"HOME":                   t.TempDir(),
		"HUDDLE_HOME":            world,
		"HUDDLE_NO_PROJECT_CRED": "1",
		"HUDDLE_TOKEN":           suiteToken,
		// autostart off unless a case asks for it: these cases test hooks, not server starts
		"HUDDLE_AUTOSTART":   "0",
		"XDG_STATE_HOME":     t.TempDir(),
		"RADAR_HOME":         t.TempDir(),
		"HUDDLE_NOTIFY_LOG":  filepath.Join(t.TempDir(), "notify.log"),
		"CLAUDE_PROJECT_DIR": world,
		"PATH":               os.Getenv("PATH"),
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
		Dir:     world,
		Now:     func() time.Time { return time.Now() },
		Bin:     huddleBin,
	}
	var out strings.Builder
	t0 := time.Now()
	hook(context.Background(), w, strings.NewReader(input), &out)
	return out.String(), time.Since(t0), world
}

// hookContext reads one hook's stdout as a hookSpecificOutput additionalContext.
func hookContext(t *testing.T, stdout string) string {
	t.Helper()
	var j struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
		SystemMessage json.RawMessage `json:"systemMessage"`
	}
	if err := json.Unmarshal([]byte(stdout), &j); err != nil {
		t.Fatalf("hook output is not context JSON: %q", stdout)
	}
	if len(j.SystemMessage) > 0 {
		t.Errorf("hook output carries a systemMessage: %q", stdout)
	}
	return j.HookSpecificOutput.AdditionalContext
}

// neverBlocks holds: whatever a hook prints must be context, never a decision.
func neverBlocks(t *testing.T, stdout string) {
	t.Helper()
	if strings.TrimSpace(stdout) == "" {
		return
	}
	var j map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &j); err != nil {
		t.Fatalf("hook output is not one JSON object: %q", stdout)
	}
	if _, ok := j["decision"]; ok {
		t.Errorf("hook output carries a decision: %q", stdout)
	}
}

// ── fake Huddles: down, mute, garbage, wrong shape ───────────────────────────

// muteURL is a server that accepts connections and never answers: a Huddle that hangs.
func muteURL(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		listener.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range conns {
			conn.Close()
		}
	})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()
	return fmt.Sprintf("http://127.0.0.1:%d", listener.Addr().(*net.TCPAddr).Port)
}

// downURL is a port nothing listens on: Huddle is down.
func downURL(t *testing.T) string {
	t.Helper()
	for i := 0; i < 500; i++ {
		port := 20000 + mathrand.Intn(12000)
		if server.PortFree(port) {
			return fmt.Sprintf("http://127.0.0.1:%d", port)
		}
	}
	t.Fatal("no free port in 20000-31999")
	return ""
}

// garbageURL answers HTML where JSON is asked for.
func garbageURL(t *testing.T) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<h1>not json</h1>")
	}))
	t.Cleanup(s.Close)
	return s.URL
}

// wrongShapeURL answers JSON of the wrong shape.
func wrongShapeURL(t *testing.T) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"result":42,"text":"garbled"}`)
	}))
	t.Cleanup(s.Close)
	return s.URL
}

// freePort picks an unused port below every OS's ephemeral range, so nothing is handed it by a
// port-0 bind between the check and the use.
func freePort(t *testing.T) int {
	t.Helper()
	for i := 0; i < 500; i++ {
		port := 20000 + mathrand.Intn(12000)
		if server.PortFree(port) {
			return port
		}
	}
	t.Fatal("no free port in 20000-31999")
	return 0
}

// ── the real server, for the cases that need one ─────────────────────────────

// huddleServer is one real Huddle answering in process on a port of its own.
type huddleServer struct {
	base  string
	token string
}

// startHuddle runs the server until the test ends. The token is its root credential, the one
// every op of the test's own drives.
func startHuddle(t *testing.T) *huddleServer {
	t.Helper()
	home := t.TempDir()
	data := t.TempDir()
	pipeReader, pipeWriter := io.Pipe()
	announced := make(chan string, 1)
	go func() {
		reader := bufio.NewReader(pipeReader)
		line, _ := reader.ReadString('\n')
		announced <- line
		// keep draining, or the next line the server says would block it forever
		_, _ = io.Copy(io.Discard, reader)
	}()
	token := suiteToken
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() {
		_ = server.Serve(ctx, server.Options{
			Version: "test", Host: "127.0.0.1", Port: 0,
			Home: home, Data: data, Token: token, AutoCreate: true,
			Stdout: pipeWriter, Stderr: io.Discard,
		})
		pipeWriter.Close()
		close(served)
	}()
	// the channel databases must be closed before the temp homes are removed.
	t.Cleanup(func() {
		cancel()
		select {
		case <-served:
		case <-time.After(5 * time.Second):
		}
	})
	line := <-announced
	match := regexp.MustCompile(`on http://127\.0\.0\.1:(\d+)`).FindStringSubmatch(line)
	if match == nil {
		t.Fatalf("the server did not say its port: %q", line)
	}
	s := &huddleServer{base: "http://127.0.0.1:" + match[1], token: token}
	for i := 0; i < 200; i++ {
		resp, err := http.Get(s.base + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return s
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("the server did not answer /health")
	return nil
}

// post posts one JSON body with the given credential in the header, naming its action the way
// the hooks' own client does.
func post(t *testing.T, s *huddleServer, path, token, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, s.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set(server.ActionHeader, strings.SplitN(path, "?", 2)[0])
	if token != "" {
		req.Header.Set("x-huddle-token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// op drives one channel operation as the test's root credential.
func op(t *testing.T, s *huddleServer, channel, name, as string, body string) (int, []byte) {
	t.Helper()
	return post(t, s, fmt.Sprintf("/api/c/%s/op/%s?as=%s", channel, name, as), s.token, body)
}

// stopServerAt stops the server a hook started in one world, if it left one.
func stopServerAt(t *testing.T, world, url string) {
	t.Helper()
	if _, err := os.ReadFile(filepath.Join(world, "run", "huddle.pid")); err != nil {
		return // it started nothing
	}
	cmd := exec.Command(huddleBin, "down")
	cmd.Dir = world
	cmd.Env = []string{"HOME=" + t.TempDir(), "HUDDLE_HOME=" + world, "HUDDLE_URL=" + url, "PATH=" + os.Getenv("PATH")}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	_ = cmd.Run()
}

// ── the plan's deadlines ─────────────────────────────────────────────────────

// TestDeadlinesSitInsideThePlanBudgets holds each hook's own worst-case deadlines against the
// largest budget the hook plan grants it: the merged event process cuts a part at its budget, so
// a handler whose internals overshoot every slice it gets would answer nothing but cuts. The
// plan's own suite holds the sums against the timeout hooks.json grants each event. approve and
// permit keep their own lines, so theirs are held against the timeouts those lines carry.
func TestDeadlinesSitInsideThePlanBudgets(t *testing.T) {
	worst := map[string]time.Duration{
		"listen":        (listenStdinMS + listenTouchMS + listenFeedMS) * time.Millisecond,
		"session-start": startBudgetMS * time.Millisecond,
		"stop":          (stopStdinMS + stopInboxMS) * time.Millisecond,
		"report":        reportBudgetMS * time.Millisecond,
	}
	for hook, ms := range worst {
		largest, found := largestBudget(hook)
		if !found {
			t.Fatalf("the plan grants %s no budget", hook)
		}
		if ms > largest {
			t.Errorf("%s can take %s inside itself, the plan never grants it more than %s", hook, ms, largest)
		}
	}
	for hook, ms := range map[string]time.Duration{
		"approve": (approveStdinMS + approveFetchMS) * time.Millisecond,
		"permit":  (permitStdinMS + permitPostMS + permitBudgetMS + permitWaitMS + permitSlackMS) * time.Millisecond,
	} {
		seconds, ok := ownLineTimeout(t, hook)
		if !ok {
			t.Fatalf("hooks.json gives %s no line of its own", hook)
		}
		budget := time.Duration(seconds) * time.Second
		if ms > budget*7/10 {
			t.Errorf("%s can take %s; its own line grants %s (70%% is %s)", hook, ms, budget, budget*7/10)
		}
	}
}

// ownLineTimeout reads the timeout of the hook's own hooks.json line, in seconds: the one line
// whose command ends in `hook <name>`.
func ownLineTimeout(t *testing.T, hook string) (int, bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "plugin", "hooks", "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	var file struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse hooks.json: %v", err)
	}
	suffix := " hook " + hook
	for _, groups := range file.Hooks {
		for _, group := range groups {
			for _, entry := range group.Hooks {
				if strings.HasSuffix(entry.Command, suffix) {
					return entry.Timeout, true
				}
			}
		}
	}
	return 0, false
}

// largestBudget answers the largest deadline the hook plan grants one huddle hook, over every
// event whose parts hold it.
func largestBudget(hook string) (time.Duration, bool) {
	largest, found := time.Duration(0), false
	for _, parts := range hookplan.Events {
		for _, part := range parts {
			if part.Name == hook && part.Budget > largest {
				largest, found = part.Budget, true
			}
		}
	}
	return largest, found
}

// ── listen ───────────────────────────────────────────────────────────────────

func TestTheListenHookDownSlowGarbageOrWrongShapeHuddle(t *testing.T) {
	for name, url := range map[string]func(*testing.T) string{
		"down":        downURL,
		"slow":        muteURL,
		"garbage":     garbageURL,
		"wrong shape": wrongShapeURL,
	} {
		t.Run(name, func(t *testing.T) {
			url := url(t)
			stdout, elapsed, _ := runHook(t, "listen",
				`{"session_id":"s-1","hook_event_name":"PostToolUse"}`,
				map[string]string{"HUDDLE_URL": url, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "me"})
			if elapsed > 3500*time.Millisecond {
				t.Errorf("listen took %s, want it inside the deadline", elapsed)
			}
			neverBlocks(t, stdout)
		})
	}
}

func TestTheListenHookAnswersUserPromptSubmitTheSameWay(t *testing.T) {
	url := muteURL(t)
	stdout, elapsed, _ := runHook(t, "listen",
		`{"session_id":"s-1","hook_event_name":"UserPromptSubmit"}`,
		map[string]string{"HUDDLE_URL": url, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "me"})
	if elapsed > 3500*time.Millisecond {
		t.Errorf("listen took %s, want it inside the deadline", elapsed)
	}
	neverBlocks(t, stdout)
}

func TestTheListenHookSurvivesOddEnv(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.WriteFile(home, []byte("a file, not a dir"), 0o600); err != nil {
		t.Fatalf("write the broken home: %v", err)
	}
	garbage := garbageURL(t)
	cases := map[string]map[string]string{
		"not a URL":            {"HUDDLE_URL": "::::", "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "me"},
		"no host":              {"HUDDLE_URL": "http://", "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "me"},
		"odd channel and wait": {"HUDDLE_URL": garbage, "HUDDLE_CHANNEL": "unclosed[", "HUDDLE_AS": "me", "HUDDLE_WAIT": "banana", "HUDDLE_LISTEN": " , x ,"},
		"home is a file":       {"HUDDLE_URL": garbage, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "me", "HUDDLE_HOME": home},
		"no channel":           {"HUDDLE_URL": garbage, "HUDDLE_CHANNEL": "", "HUDDLE_AS": "me"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			stdout, elapsed, _ := runHook(t, "listen",
				`{"session_id":"s-1","hook_event_name":"PostToolUse"}`, env)
			if elapsed > 3500*time.Millisecond {
				t.Errorf("listen took %s, want it inside the deadline", elapsed)
			}
			neverBlocks(t, stdout)
		})
	}
}

// ── session-start ────────────────────────────────────────────────────────────

func TestTheSessionStartHookNoChannelDownGarbage(t *testing.T) {
	stdout, _, _ := runHook(t, "session-start", `{"source":"startup"}`,
		map[string]string{"HUDDLE_URL": downURL(t), "HUDDLE_CHANNEL": "", "HUDDLE_AS": ""})
	if stdout != "" {
		t.Errorf("no channel configured, hook printed %q", stdout)
	}
	for name, url := range map[string]func(*testing.T) string{
		"down":        downURL,
		"garbage":     garbageURL,
		"wrong shape": wrongShapeURL,
		"not a URL":   func(*testing.T) string { return "::::" },
	} {
		t.Run(name, func(t *testing.T) {
			url := url(t)
			stdout, elapsed, _ := runHook(t, "session-start", `{"source":"startup"}`,
				map[string]string{"HUDDLE_URL": url, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "me"})
			if elapsed > 7000*time.Millisecond {
				t.Errorf("session-start took %s, want it inside the deadline", elapsed)
			}
			neverBlocks(t, stdout)
		})
	}
}

func TestTheSessionStartHookGivesUpOnAHuddleThatNeverAnswers(t *testing.T) {
	stdout, elapsed, _ := runHook(t, "session-start", `{"source":"startup"}`,
		map[string]string{"HUDDLE_URL": muteURL(t), "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "me"})
	if elapsed > 5*time.Second { // the 3 s budget, far inside the timeout
		t.Errorf("session-start took %s, want it inside its 3 s budget", elapsed)
	}
	neverBlocks(t, stdout) // it says the service does not answer — context, not a decision
}

func TestTheSessionStartHookWithAutostartKeepsToItsBudgetWhenTheServerItStartsCannotAnswer(t *testing.T) {
	url := muteURL(t) // the port is taken: up refuses to start on it
	stdout, elapsed, world := runHook(t, "session-start", `{"source":"startup"}`,
		map[string]string{"HUDDLE_URL": url, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "me", "HUDDLE_AUTOSTART": "1"})
	defer stopServerAt(t, world, url)
	if elapsed > 5500*time.Millisecond {
		t.Errorf("session-start took %s, want it inside its 3 s budget", elapsed)
	}
	neverBlocks(t, stdout)
}

// TestTheFirstSessionStartsHuddleWithNoChannelAndNoSetup: a fresh install's first session — no
// channel anywhere, no setup, no token — leaves a server answering /health, and says nothing
// (no channel: nothing to join, nothing to report).
func TestTheFirstSessionStartsHuddleWithNoChannelAndNoSetup(t *testing.T) {
	port := freePort(t)
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	stdout, _, world := runHook(t, "session-start", `{"source":"startup","session_id":"s-first"}`,
		map[string]string{"HUDDLE_URL": url, "HUDDLE_AUTOSTART": "", "HUDDLE_TOKEN": ""})
	defer stopServerAt(t, world, url)
	if stdout != "" {
		t.Errorf("session-start printed %q with no channel, want silence", stdout)
	}
	answers := func() bool {
		resp, err := http.Get(url + "/health")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}
	deadline := time.Now().Add(10 * time.Second)
	for !answers() {
		if time.Now().After(deadline) {
			t.Fatalf("nothing answered %s/health within 10 s of the first session", url)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestAutostartOffLeavesHuddleDownWithNoChannel: HUDDLE_AUTOSTART=0 keeps the first session
// from starting anything, and the hook stays silent.
func TestAutostartOffLeavesHuddleDownWithNoChannel(t *testing.T) {
	port := freePort(t)
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	stdout, _, world := runHook(t, "session-start", `{"source":"startup","session_id":"s-first"}`,
		map[string]string{"HUDDLE_URL": url, "HUDDLE_AUTOSTART": "0", "HUDDLE_TOKEN": ""})
	defer stopServerAt(t, world, url)
	if stdout != "" {
		t.Errorf("session-start printed %q with no channel, want silence", stdout)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url + "/health")
		if err == nil {
			resp.Body.Close()
			t.Fatalf("a server answered %s/health with autostart off", url)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// ── the entry points as Claude Code runs them ────────────────────────────────

// TestTheHookEntriesExitZeroInSilence runs the binary the way hooks.json runs each hook: with no
// channel configured every event entry (and the two decision entries) answers 0 and prints
// nothing anywhere.
func TestTheHookEntriesExitZeroInSilence(t *testing.T) {
	for _, name := range []string{
		"session-start", "approve", "user-prompt-submit", "pre-tool-use", "post-tool-use",
		"notification", "permission-request", "stop", "subagent-start", "subagent-stop",
		"session-end", "pre-compact",
	} {
		t.Run(name, func(t *testing.T) {
			world := t.TempDir()
			cmd := exec.Command(huddleBin, "hook", name)
			cmd.Dir = world
			cmd.Env = []string{
				"HOME=" + t.TempDir(), "RADAR_HOME=" + t.TempDir(), "RADAR_AUTOSTART=0",
				"HUDDLE_HOME=" + world, "CLAUDE_PROJECT_DIR=" + world,
				"HUDDLE_NO_PROJECT_CRED=1", "HUDDLE_TOKEN=test-root", "HUDDLE_AUTOSTART=0",
				"XDG_STATE_HOME=" + t.TempDir(),
				"PATH=" + os.Getenv("PATH"),
			}
			cmd.Stdin = strings.NewReader("{}")
			var stdout, stderr strings.Builder
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("huddle hook %s: %v", name, err)
			}
			if stdout.String() != "" || stderr.String() != "" {
				t.Errorf("huddle hook %s printed (%q, %q), want silence", name, stdout.String(), stderr.String())
			}
		})
	}
}

// TestTheHookEntryPrintsTheContextClaudeCodeExpects proves the wiring end to end: the entry
// point's stdout is the hook's one JSON line.
func TestTheHookEntryPrintsTheContextClaudeCodeExpects(t *testing.T) {
	s := startHuddle(t)
	world := t.TempDir()
	cmd := exec.Command(huddleBin, "hook", "session-start")
	cmd.Dir = world
	cmd.Env = []string{
		"HOME=" + t.TempDir(), "HUDDLE_HOME=" + world, "CLAUDE_PROJECT_DIR=" + world,
		"HUDDLE_NO_PROJECT_CRED=1", "HUDDLE_TOKEN=" + s.token, "XDG_STATE_HOME=" + t.TempDir(),
		"HUDDLE_URL=" + s.base, "HUDDLE_CHANNEL=hooks", "HUDDLE_AS=me",
		"PATH=" + os.Getenv("PATH"),
	}
	cmd.Stdin = strings.NewReader(`{"source":"startup","session_id":"s-e2e"}`)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("huddle hook session-start: %v", err)
	}
	if stderr.String() != "" {
		t.Errorf("the hook wrote to stderr: %q", stderr.String())
	}
	ctx := hookContext(t, stdout.String())
	if !strings.Contains(ctx, `You are in Huddle channel "hooks" as "me"`) {
		t.Errorf("context = %q", ctx)
	}
}

// ── quiet ────────────────────────────────────────────────────────────────────

func TestAHookThatFailsLogsTheErrorToHooksLogAndStaysSilent(t *testing.T) {
	world := t.TempDir()
	w := &cli.World{Getenv: func(key string) string {
		if key == "HUDDLE_HOME" {
			return world
		}
		return ""
	}}
	var out strings.Builder
	Run(context.Background(), w, "probe", func() error { return fmt.Errorf("kaboom") })
	if out.String() != "" {
		t.Errorf("Run printed %q, want silence", out.String())
	}
	raw, err := os.ReadFile(filepath.Join(world, "hooks.log"))
	if err != nil {
		t.Fatalf("read hooks.log: %v", err)
	}
	if !strings.Contains(string(raw), "probe: kaboom\n") {
		t.Errorf("hooks.log = %q, want a probe: kaboom line", string(raw))
	}
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z probe: kaboom\n$`).Match(raw) {
		t.Errorf("hooks.log = %q, want the ISO time and the message on one line", string(raw))
	}
}

// TestLogHookErrorFallsBackToTheTempDir holds the courtesy log's other home: a project whose
// Huddle home does not exist yet still gets its line, in the temp dir.
func TestLogHookErrorFallsBackToTheTempDir(t *testing.T) {
	dir := t.TempDir()
	for _, name := range tempEnvNames() {
		t.Setenv(name, dir)
	}
	w := &cli.World{Getenv: func(string) string { return "" }}
	LogHookError(context.Background(), w, "probe", fmt.Errorf("boom"))
	raw, err := os.ReadFile(filepath.Join(dir, "hooks.log"))
	if err != nil || !strings.Contains(string(raw), "probe: boom\n") {
		t.Errorf("temp hooks.log: %v %q", err, string(raw))
	}
}

// tempEnvNames answers the variable the temp dir is read from.
func tempEnvNames() []string {
	return []string{"TMPDIR"}
}

// ── stop ─────────────────────────────────────────────────────────────────────

func TestTheStopHookBlocksExactlyWhileAsksAwaitAReply(t *testing.T) {
	s := startHuddle(t)
	for _, name := range []string{"senior", "me", "quiet"} {
		if code, body := op(t, s, "hooks", "join", name, "{}"); code != 200 {
			t.Fatalf("join %s: %d %s", name, code, body)
		}
	}
	if code, body := op(t, s, "hooks", "send", "senior", `{"to":"me","msg":"did you finish the migration?","ask":true}`); code != 200 {
		t.Fatalf("send: %d %s", code, body)
	}
	stdout, elapsed, _ := runHook(t, "stop", "{}",
		map[string]string{"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "me"})
	if elapsed > 15*time.Second {
		t.Errorf("stop took %s, want it inside the deadline", elapsed)
	}
	var j struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(stdout), &j); err != nil {
		t.Fatalf("stop output %q is not a decision", stdout)
	}
	if j.Decision != "block" {
		t.Errorf("decision = %q, want block", j.Decision)
	}
	if !strings.Contains(j.Reason, "did you finish the migration?") {
		t.Errorf("reason = %q, want the ask in it", j.Reason)
	}
}

func TestTheStopHookBlocksOncePerAskNeverAgainAndNeverForAStaleOne(t *testing.T) {
	s := startHuddle(t)
	for _, name := range []string{"senior", "twice"} {
		if code, body := op(t, s, "hooks", "join", name, "{}"); code != 200 {
			t.Fatalf("join %s: %d %s", name, code, body)
		}
	}
	if code, body := op(t, s, "hooks", "send", "senior", `{"to":"twice","msg":"still there?","ask":true}`); code != 200 {
		t.Fatalf("send: %d %s", code, body)
	}
	home := t.TempDir() // one Huddle home across the runs, like one project
	env := map[string]string{"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "twice", "HUDDLE_HOME": home}
	first, _, _ := runHook(t, "stop", "{}", env)
	var j struct {
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal([]byte(first), &j); err != nil || j.Decision != "block" {
		t.Fatalf("first stop output %q, want a block", first)
	}
	again, _, _ := runHook(t, "stop", "{}", env)
	if again != "" { // the asker may be gone: one reminder is enough
		t.Errorf("second stop output %q, want silence", again)
	}
	if code, body := op(t, s, "hooks", "send", "senior", `{"to":"twice","msg":"and this one?","ask":true}`); code != 200 {
		t.Fatalf("send again: %d %s", code, body)
	}
	next, _, _ := runHook(t, "stop", "{}", env)
	if next == "" {
		t.Fatal("a new ask still counts, got silence")
	}
	var n struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(next), &n); err != nil {
		t.Fatalf("third stop output %q: %v", next, err)
	}
	if !strings.Contains(n.Reason, "and this one?") {
		t.Errorf("reason = %q, want the new ask in it", n.Reason)
	}
	if strings.Contains(n.Reason, "still there?") {
		t.Errorf("reason = %q, the raised ask must not come back", n.Reason)
	}
	if code, body := op(t, s, "hooks", "send", "senior", `{"to":"twice","msg":"an old one","ask":true}`); code != 200 {
		t.Fatalf("send the old one: %d %s", code, body)
	}
	time.Sleep(50 * time.Millisecond)
	stale, _, _ := runHook(t, "stop", "{}", map[string]string{
		"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "twice",
		"HUDDLE_HOME": t.TempDir(), "HUDDLE_STOP_MAX_AGE": "0",
	})
	if stale != "" { // older than the limit: never blocks
		t.Errorf("stale stop output %q, want silence", stale)
	}
}

func TestTheStopHookNeverBlocks(t *testing.T) {
	s := startHuddle(t)
	for _, name := range []string{"senior", "me", "quiet"} {
		if code, body := op(t, s, "hooks", "join", name, "{}"); code != 200 {
			t.Fatalf("join %s: %d %s", name, code, body)
		}
	}
	if code, body := op(t, s, "hooks", "send", "senior", `{"to":"me","msg":"still open?","ask":true}`); code != 200 {
		t.Fatalf("send: %d %s", code, body)
	}
	open := map[string]string{"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "me"}
	cases := []struct {
		what  string
		input string
		env   map[string]string
	}{
		{"stop_hook_active is set", `{"stop_hook_active":true}`, open},
		{"a subagent answers through its parent", `{"agent_id":"toolu_1"}`, open},
		{"the inbox is empty", "{}", map[string]string{"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "quiet"}},
		{"Huddle is down", "{}", map[string]string{"HUDDLE_URL": downURL(t), "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "me"}},
		{"Huddle never answers (deadline 5 s)", "{}", map[string]string{"HUDDLE_URL": muteURL(t), "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "me"}},
		{"Huddle answers garbage", "{}", map[string]string{"HUDDLE_URL": garbageURL(t), "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "me"}},
		{"Huddle answers the wrong shape", "{}", map[string]string{"HUDDLE_URL": wrongShapeURL(t), "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "me"}},
	}
	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			stdout, elapsed, _ := runHook(t, "stop", c.input, c.env)
			if elapsed > 10500*time.Millisecond {
				t.Errorf("stop took %s, want it inside the deadline", elapsed)
			}
			if stdout != "" { // any Stop-hook output is a decision; none of these may decide
				t.Errorf("stop output %q, want silence", stdout)
			}
		})
	}
}

// ── credentials ──────────────────────────────────────────────────────────────

func TestASessionWithoutACredentialStaysOutside(t *testing.T) {
	s := startHuddle(t)
	if code, body := op(t, s, "hooks", "join", "senior", "{}"); code != 200 {
		t.Fatalf("join senior: %d %s", code, body)
	}
	_, _ = op(t, s, "hooks", "send", "senior", `{"to":"stranger","msg":"psst","ask":true}`)
	// no credential anywhere: this session holds nothing for the server
	env := map[string]string{
		"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "stranger",
		"HUDDLE_TOKEN": "", "XDG_STATE_HOME": t.TempDir(),
	}
	stdout, _, _ := runHook(t, "session-start", `{"source":"startup","session_id":"s-out"}`, env)
	ctx := hookContext(t, stdout)
	if !strings.Contains(ctx, "runs at "+s.base+", but this session holds no credential") {
		t.Errorf("context = %q, want the no-credential line", ctx)
	}
	if !strings.Contains(ctx, "/huddle:invite") { // a way back in, not a dead end
		t.Errorf("context = %q, want the way back in", ctx)
	}
	for _, hook := range []struct {
		name  string
		input string
	}{
		{"listen", `{"session_id":"s-out","hook_event_name":"PostToolUse"}`},
		{"stop", `{"session_id":"s-out"}`},
	} {
		t.Run(hook.name, func(t *testing.T) {
			stdout, _, _ := runHook(t, hook.name, hook.input, env)
			if stdout != "" {
				t.Errorf("%s printed %q, want silence", hook.name, stdout)
			}
		})
	}
}

func TestAJoinedMembersSessionStartCarriesNoSystemMessage(t *testing.T) {
	s := startHuddle(t)
	code, body := post(t, s, "/api/tokens", s.token, `{"channel":"hooks"}`)
	if code != 200 {
		t.Fatalf("mint the invite: %d %s", code, body)
	}
	var invite struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &invite); err != nil {
		t.Fatalf("the invite's answer %s: %v", body, err)
	}
	code, body = post(t, s, "/api/join", "", fmt.Sprintf(`{"token":%q,"name":"member"}`, invite.Token))
	if code != 200 {
		t.Fatalf("join with it: %d %s", code, body)
	}
	var joined struct {
		Credential string  `json:"credential"`
		Name       string  `json:"name"`
		Channel    *string `json:"channel"`
	}
	if err := json.Unmarshal(body, &joined); err != nil {
		t.Fatalf("the join's answer %s: %v", body, err)
	}
	// one home for both hook calls, so the credential seeded before them is theirs to find
	home := t.TempDir()
	sessions := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatalf("make the sessions dir: %v", err)
	}
	cred := fmt.Sprintf(`{"url":%q,"channel":"hooks","as":"member","credential":%q}`, s.base, joined.Credential)
	if err := os.WriteFile(filepath.Join(sessions, "s-mem.json"), []byte(cred), 0o600); err != nil {
		t.Fatalf("keep the member's credential: %v", err)
	}
	env := map[string]string{
		"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "member",
		"HUDDLE_TOKEN": "", "XDG_STATE_HOME": t.TempDir(), "HUDDLE_HOME": home,
	}
	resumed, _, _ := runHook(t, "session-start", `{"source":"resume","session_id":"s-mem"}`, env)
	if ctx := hookContext(t, resumed); !strings.Contains(ctx, `as "member"`) {
		t.Errorf("resume context = %q", ctx)
	}
	started, _, _ := runHook(t, "session-start", `{"source":"startup","session_id":"s-mem"}`, env)
	ctx := hookContext(t, started)
	if !strings.Contains(ctx, `You are in Huddle channel "hooks" as "member"`) {
		t.Errorf("startup context = %q", ctx)
	}
	if strings.Contains(ctx, joined.Credential) {
		t.Errorf("the context carries the credential: %q", ctx)
	}
}

func TestTheSessionThatStartsHuddleHoldsItsRootCredential(t *testing.T) {
	port := freePort(t)
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	env := map[string]string{
		"HUDDLE_URL": url, "HUDDLE_CHANNEL": "made", "HUDDLE_AS": "boss",
		"HUDDLE_AUTOSTART": "1", "HUDDLE_TOKEN": "", "XDG_STATE_HOME": t.TempDir(),
	}
	stdout, _, world := runHook(t, "session-start", `{"source":"startup","session_id":"s-boss"}`, env)
	defer stopServerAt(t, world, url)
	var j struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
		SystemMessage json.RawMessage `json:"systemMessage"`
	}
	if err := json.Unmarshal([]byte(stdout), &j); err != nil {
		t.Fatalf("session-start output %q: %v", stdout, err)
	}
	ctx := j.HookSpecificOutput.AdditionalContext
	if !strings.Contains(ctx, `You are in Huddle channel "made" as "boss"`) {
		t.Errorf("context = %q", ctx)
	}
	if len(j.SystemMessage) > 0 { // no invite or sign-in link through the hook
		t.Errorf("output carries a systemMessage: %q", stdout)
	}
	if strings.Contains(stdout, "--token") || strings.Contains(stdout, "?code=") {
		t.Errorf("output carries a token or a sign-in code: %q", stdout)
	}
	// the session's credential is the root one, kept 0600 in a 0700 dir under the home
	dir := filepath.Join(world, "sessions")
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("sessions dir: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("sessions dir mode = %v, want 0700", info.Mode().Perm())
	}
	file := filepath.Join(dir, "s-boss.json")
	info, err = os.Stat(file)
	if err != nil {
		t.Fatalf("the session's credential: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("the session's credential mode = %v, want 0600", info.Mode().Perm())
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read the credential: %v", err)
	}
	var cred struct {
		URL        string `json:"url"`
		Channel    string `json:"channel"`
		As         string `json:"as"`
		Credential string `json:"credential"`
		Root       bool   `json:"root"`
	}
	if err := json.Unmarshal(raw, &cred); err != nil {
		t.Fatalf("the credential %s: %v", raw, err)
	}
	if cred.URL != url || cred.Channel != "made" || cred.As != "boss" || !cred.Root || cred.Credential == "" {
		t.Errorf("the credential = %+v", cred)
	}
	// the creator mints the invite itself (huddle setup prints the line; here, over the API)
	code, body := post(t, &huddleServer{base: url}, "/api/tokens", cred.Credential, `{"channel":"made"}`)
	if code != 200 {
		t.Fatalf("mint the invite: %d %s", code, body)
	}
	var invite struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &invite); err != nil {
		t.Fatalf("the invite's answer %s: %v", body, err)
	}
	code, body = post(t, &huddleServer{base: url}, "/api/join", "", fmt.Sprintf(`{"token":%q,"name":"dev"}`, invite.Token))
	if code != 200 {
		t.Fatalf("join with it: %d %s", code, body)
	}
	var joined struct {
		Name    string  `json:"name"`
		Channel *string `json:"channel"`
	}
	if err := json.Unmarshal(body, &joined); err != nil {
		t.Fatalf("the join's answer %s: %v", body, err)
	}
	if joined.Name != "dev" || joined.Channel == nil || *joined.Channel != "made" {
		t.Errorf("the join's answer = %+v", joined)
	}
	// a later start of the same session joins quietly, in the home its credential lives in
	env["HUDDLE_HOME"] = world
	again, _, _ := runHook(t, "session-start", `{"source":"resume","session_id":"s-boss"}`, env)
	if ctx := hookContext(t, again); strings.Contains(ctx, "/huddle:join") {
		t.Errorf("resume context = %q, no join line in it", ctx)
	}
}

// ── edits ────────────────────────────────────────────────────────────────────

func TestTheListenHookReportsAnEditAsARepoRelativePathAndTellsTheSecondSessionOnce(t *testing.T) {
	s := startHuddle(t)
	for _, name := range []string{"payments", "shop"} {
		if code, body := op(t, s, "hooks", "join", name, "{}"); code != 200 {
			t.Fatalf("join %s: %d %s", name, code, body)
		}
	}
	repo := t.TempDir()
	for _, dir := range []string{filepath.Join(repo, ".git"), filepath.Join(repo, "src")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("make %s: %v", dir, err)
		}
	}
	file := filepath.Join(repo, "src", "cart.ts")
	edit := func(session string) string {
		t.Helper()
		input := fmt.Sprintf(`{"session_id":"s-%s","hook_event_name":"PostToolUse","tool_name":"Write","tool_input":{"file_path":%q,"content":"TOP-SECRET-CONTENT"},"cwd":%q}`, session, file, repo)
		stdout, elapsed, _ := runHook(t, "listen", input,
			map[string]string{"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": session})
		if elapsed > 5*time.Second {
			t.Errorf("listen took %s, want it inside the deadline", elapsed)
		}
		return stdout
	}
	if first := edit("payments"); strings.Contains(first, "edited src/cart.ts") {
		t.Errorf("the first edit's output %q already names an editor", first)
	}
	second := edit("shop")
	ctx := hookContext(t, second)
	if !regexp.MustCompile(`payments edited src/cart\.ts .* coordinate with it`).MatchString(ctx) {
		t.Errorf("context = %q, want the other session's edit in it", ctx)
	}
	neverBlocks(t, second)
	if again := edit("shop"); strings.Contains(again, "edited src/cart.ts") { // once per file per window
		t.Errorf("the second warning %q, want none", again)
	}
	// a subagent's edit counts as its session's, and its warning goes to the subagent
	subInput := fmt.Sprintf(`{"session_id":"s-pay","agent_id":"ag-1","hook_event_name":"PostToolUse","tool_name":"Edit","tool_input":{"file_path":%q},"cwd":%q}`, file, repo)
	sub, _, _ := runHook(t, "listen", subInput,
		map[string]string{"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "payments"})
	if ctx := hookContext(t, sub); !regexp.MustCompile(`shop edited src/cart\.ts`).MatchString(ctx) {
		t.Errorf("subagent context = %q, want the other session's edit in it", ctx)
	}
	req, err := http.NewRequest(http.MethodGet, s.base+"/api/c/hooks/conflicts", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("x-huddle-token", s.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("conflicts: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var view struct {
		Conflicts []struct {
			Path     string `json:"path"`
			Sessions []struct {
				Name string `json:"name"`
			} `json:"sessions"`
		} `json:"conflicts"`
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("conflicts %s: %v", raw, err)
	}
	names := []string{}
	for _, conflict := range view.Conflicts {
		if conflict.Path == "src/cart.ts" {
			for _, session := range conflict.Sessions {
				names = append(names, session.Name)
			}
		}
	}
	sort.Strings(names)
	if len(names) != 2 || names[0] != "payments" || names[1] != "shop" {
		t.Errorf("src/cart.ts's sessions = %v, want [payments shop]", names)
	}
	if strings.Contains(string(raw), "TOP-SECRET") {
		t.Errorf("the conflicts view carries the file's contents: %s", raw)
	}
	// a Huddle that is down: silence, inside the deadline
	down := fmt.Sprintf(`{"session_id":"s-d","hook_event_name":"PostToolUse","tool_name":"Edit","tool_input":{"file_path":%q},"cwd":%q}`, file, repo)
	stdout, elapsed, _ := runHook(t, "listen", down,
		map[string]string{"HUDDLE_URL": downURL(t), "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "shop"})
	if stdout != "" || elapsed > 5*time.Second {
		t.Errorf("down: (%q, %s), want silence inside the deadline", stdout, elapsed)
	}
}

// ── the quiet plumbing ───────────────────────────────────────────────────────

func TestAHookThatPanicsLogsItTooAndStaysSilent(t *testing.T) {
	world := t.TempDir()
	w := &cli.World{Getenv: func(key string) string {
		if key == "HUDDLE_HOME" {
			return world
		}
		return ""
	}}
	Run(context.Background(), w, "probe", func() error { panic("bang") })
	raw, err := os.ReadFile(filepath.Join(world, "hooks.log"))
	if err != nil || !strings.Contains(string(raw), "probe: bang\n") {
		t.Errorf("hooks.log: %v %q", err, string(raw))
	}
}

func TestTheCourtesyLogCapsItself(t *testing.T) {
	world := t.TempDir()
	w := &cli.World{Getenv: func(key string) string {
		if key == "HUDDLE_HOME" {
			return world
		}
		return ""
	}}
	file := filepath.Join(world, "hooks.log")
	if err := os.WriteFile(file, make([]byte, 256*1024+1), 0o600); err != nil {
		t.Fatalf("flood hooks.log: %v", err)
	}
	LogHookError(context.Background(), w, "probe", fmt.Errorf("after the flood"))
	info, err := os.Stat(file)
	if err != nil {
		t.Fatalf("stat hooks.log: %v", err)
	}
	if info.Size() > 256*1024 {
		t.Errorf("hooks.log kept growing: %d bytes", info.Size())
	}
	raw, _ := os.ReadFile(file)
	if !strings.Contains(string(raw), "probe: after the flood\n") {
		t.Errorf("hooks.log = %q, want the new line in it", string(raw))
	}
}

func TestReadInputGivesUpOnASilentStdin(t *testing.T) {
	pipeReader, pipeWriter := io.Pipe() // stdin that never says anything
	defer pipeReader.Close()
	defer pipeWriter.Close()
	var input hookInput
	t0 := time.Now()
	readInput(pipeReader, 20, &input)
	if elapsed := time.Since(t0); elapsed > 2*time.Second {
		t.Errorf("readInput took %s, want it to give up at its deadline", elapsed)
	}
	if input != (hookInput{}) {
		t.Errorf("input = %+v, want it untouched", input)
	}
}

func TestTheWorldHelpersReadTheProcessWhenTheWorldDoesNot(t *testing.T) {
	t.Setenv("HOOKS_PROBE_VAR", "yes")
	if got := getenvOf(&cli.World{}, "HOOKS_PROBE_VAR"); got != "yes" {
		t.Errorf("getenvOf = %q, want the process's variable", got)
	}
	if got := getenvOf(&cli.World{Getenv: func(string) string { return "own" }}, "HOOKS_PROBE_VAR"); got != "own" {
		t.Errorf("getenvOf = %q, want the world's own", got)
	}
	if nowOf(&cli.World{}).IsZero() {
		t.Error("nowOf = the zero time, want a clock")
	}
}

// ── session-start's PATH line ────────────────────────────────────────────────

// TestSessionStartPutsThePluginOnTheSessionPath: every start appends the plugin's bin/ to the
// session's Bash PATH once, whatever else the hook does (here: no channel, so nothing else).
func TestSessionStartPutsThePluginOnTheSessionPath(t *testing.T) {
	world := t.TempDir()
	root := t.TempDir()
	envFile := filepath.Join(world, "claude-env")
	env := map[string]string{
		"HUDDLE_CHANNEL": "", "HUDDLE_AS": "",
		"CLAUDE_ENV_FILE": envFile, "CLAUDE_PLUGIN_ROOT": root,
	}
	for i := 0; i < 2; i++ {
		if stdout, _, _ := runHook(t, "session-start", `{"source":"startup"}`, env); stdout != "" {
			t.Errorf("session-start printed %q, want silence", stdout)
		}
	}
	raw, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read the env file: %v", err)
	}
	line := `export PATH="` + filepath.Join(root, "bin") + `:$PATH"`
	if strings.Count(string(raw), line) != 1 { // once per session, not once per hook run
		t.Errorf("env file = %q, want the PATH line exactly once", string(raw))
	}
}

// ── the stop hook's own odd states ───────────────────────────────────────────

// TestTheStopHookSurvivesItsOwnOddStates: an age that is not a number, a state file that cannot
// be written, and a state file that is not JSON — none of them blocks or breaks.
func TestTheStopHookSurvivesItsOwnOddStates(t *testing.T) {
	s := startHuddle(t)
	for _, name := range []string{"senior", "odd"} {
		if code, body := op(t, s, "hooks", "join", name, "{}"); code != 200 {
			t.Fatalf("join %s: %d %s", name, code, body)
		}
	}
	if code, body := op(t, s, "hooks", "send", "senior", `{"to":"odd","msg":"an age that is not a number","ask":true}`); code != 200 {
		t.Fatalf("send: %d %s", code, body)
	}
	notNumber, _, _ := runHook(t, "stop", "{}", map[string]string{
		"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "odd", "HUDDLE_STOP_MAX_AGE": "banana"})
	if notNumber != "" {
		t.Errorf("a HUDDLE_STOP_MAX_AGE that is not a number blocked the stop: %q", notNumber)
	}
	home := filepath.Join(t.TempDir(), "home")
	if err := os.WriteFile(home, []byte("a file, not a dir"), 0o600); err != nil {
		t.Fatalf("write the broken home: %v", err)
	}
	unwritable, _, _ := runHook(t, "stop", "{}", map[string]string{
		"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "odd", "HUDDLE_HOME": home})
	if unwritable != "" { // a stop that cannot record what it raised does not block at all
		t.Errorf("an unwritable state blocked the stop: %q", unwritable)
	}
	garbageHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(garbageHome, "stop-raised.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write the broken state: %v", err)
	}
	corrupt, _, _ := runHook(t, "stop", "{}", map[string]string{
		"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "odd", "HUDDLE_HOME": garbageHome})
	var j struct {
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal([]byte(corrupt), &j); err != nil || j.Decision != "block" {
		t.Errorf("stop output %q, want it to block past a corrupt state", corrupt)
	}
	raw, err := os.ReadFile(filepath.Join(garbageHome, "stop-raised.json"))
	if err != nil || !json.Valid(raw) {
		t.Errorf("the rewritten state: %v %s", err, raw)
	}
}

// TestTheListenHookNamesTheEventItServesWhenTheInputDidNot: a hook input without
// hook_event_name still answers under the event the hook serves.
func TestTheListenHookNamesTheEventItServesWhenTheInputDidNot(t *testing.T) {
	world := t.TempDir()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			fmt.Fprint(w, `{"result":{"warn":"payments edited src/cart.ts lately"}}`)
			return
		}
		fmt.Fprint(w, `{"result":42,"text":""}`) // the feed's read: nothing it can use
	}))
	t.Cleanup(s.Close)
	input := fmt.Sprintf(`{"session_id":"s-1","tool_name":"Write","tool_input":{"file_path":%q},"cwd":%q}`,
		filepath.Join(world, "src", "cart.ts"), world)
	stdout, _, _ := runHook(t, "listen", input,
		map[string]string{"HUDDLE_URL": s.URL, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "shop"})
	var j struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(stdout), &j); err != nil {
		t.Fatalf("listen output %q: %v", stdout, err)
	}
	if j.HookSpecificOutput.HookEventName != "PostToolUse" {
		t.Errorf("hookEventName = %q, want PostToolUse", j.HookSpecificOutput.HookEventName)
	}
	if !strings.Contains(j.HookSpecificOutput.AdditionalContext, "payments edited src/cart.ts") {
		t.Errorf("context = %q", j.HookSpecificOutput.AdditionalContext)
	}
}

// The one-time import leaves its notice in the home's run/import-notice; the session-start hook
// says it once — whatever the project configures — and the file is gone, so it is never said
// again.
func TestTheSessionStartHookSaysTheImportNoticeOnce(t *testing.T) {
	world := t.TempDir()
	vars := map[string]string{
		"HOME":               t.TempDir(),
		"HUDDLE_HOME":        world,
		"XDG_STATE_HOME":     t.TempDir(),
		"RADAR_HOME":         t.TempDir(),
		"CLAUDE_PROJECT_DIR": world,
		"HUDDLE_CHANNEL":     "",
		"HUDDLE_AS":          "",
		"HUDDLE_AUTOSTART":   "0",
		"PATH":               os.Getenv("PATH"),
	}
	entries := make([]string, 0, len(vars))
	for key, value := range vars {
		entries = append(entries, key+"="+value)
	}
	w := &cli.World{
		Getenv:  func(key string) string { return vars[key] },
		Environ: func() []string { return append([]string{}, entries...) },
		Dir:     world,
		Now:     func() time.Time { return time.Now() },
		Bin:     huddleBin,
	}
	notice := "Huddle moved 3 channels from /an/old/home into ~/.config/huddle; the old folder is untouched."
	if err := os.MkdirAll(filepath.Join(world, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(world, "run", "import-notice"), []byte(notice+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	SessionStart(context.Background(), w, strings.NewReader(`{"source":"startup"}`), &out)
	if got := hookContext(t, out.String()); got != notice {
		t.Errorf("the notice said = %q, want %q", got, notice)
	}
	if _, err := os.Stat(filepath.Join(world, "run", "import-notice")); !os.IsNotExist(err) {
		t.Errorf("the notice file stayed: %v", err)
	}
	var again strings.Builder
	SessionStart(context.Background(), w, strings.NewReader(`{"source":"startup"}`), &again)
	if again.Len() != 0 {
		t.Errorf("the notice was said again: %q", again.String())
	}
}
