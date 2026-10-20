// The CLI suite's end-to-end leg, over the built binary: a server, the creator's invite and two
// members joining with it, events and a long-poll, tasks across sessions, knowledge search, the
// dashboard sign-in, a live stream, listen in its four shapes, and huddle up/down. Throwaway
// everything: temp HOME (so the huddle home under it), temp state and data, free ports.
package cli

import (
	"bufio"
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
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/server"
)

// e2eBase is a clean child environment: the real one minus every Huddle and Claude variable, so
// nothing of the real machine's session leaks in, with throwaway homes. The system's tools stay
// on PATH: the CLI looks its server's pid up with ps, and a stripped PATH would hide it. One
// state dir serves every session of a test, and no project-wide credential hides a session's own.
func e2eBase(t *testing.T) map[string]string {
	t.Helper()
	base := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "HUDDLE_") || strings.HasPrefix(key, "CLAUDE") {
			continue
		}
		base[key] = value
	}
	base["HOME"] = t.TempDir()
	// the huddle home sits under XDG_CONFIG_HOME: pin it under the throwaway HOME, or a shell
	// that exports its own would point the runs at the real ~/.config
	base["XDG_CONFIG_HOME"] = filepath.Join(base["HOME"], ".config")
	base["XDG_STATE_HOME"] = filepath.Join(t.TempDir(), "state")
	base["RADAR_HOME"] = filepath.Join(t.TempDir(), "obs")
	base["HUDDLE_NO_PROJECT_CRED"] = "1"
	return base
}

// e2eEnv layers extra variables over a base.
func e2eEnv(base map[string]string, extra ...map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range base {
		out[key] = value
	}
	for _, layer := range extra {
		for key, value := range layer {
			out[key] = value
		}
	}
	return out
}

// e2eEntries renders an env map for a child process.
func e2eEntries(env map[string]string) []string {
	entries := make([]string, 0, len(env))
	for key, value := range env {
		entries = append(entries, key+"="+value)
	}
	return entries
}

// e2eRun runs the binary once in env and answers its exit code and both outputs.
func e2eRun(t *testing.T, bin string, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = e2eEntries(env)
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		if exited, ok := err.(*exec.ExitError); ok {
			return exited.ExitCode(), out.String(), errOut.String()
		}
		t.Fatalf("run huddle %v: %v", args, err)
	}
	return 0, out.String(), errOut.String()
}

// e2eServer is one Huddle server this test started, on a port of its own, with its root
// credential. base is the session environment its CLI calls run in.
type e2eServer struct {
	base map[string]string
	url  string
	port int
	root string
	stop func()
}

// startE2EServer spawns the binary's serve entry on any free port and waits for /health.
func startE2EServer(t *testing.T, bin string, base map[string]string) *e2eServer {
	t.Helper()
	root := "e2e-root-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	child := e2eEnv(base, map[string]string{
		"PORT":         "0",
		"HUDDLE_DATA":  filepath.Join(t.TempDir(), "data"),
		"HUDDLE_TOKEN": root,
		"HUDDLE_HOME":  filepath.Join(t.TempDir(), "home"),
	})
	cmd := exec.Command(bin, "serve")
	cmd.Env = e2eEntries(child)
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("pipe the server: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the server: %v", err)
	}
	lines := make(chan string, 16)
	go func() {
		reader := bufio.NewReader(stdout)
		for {
			line, err := reader.ReadString('\n')
			if line != "" {
				lines <- line
			}
			if err != nil {
				close(lines)
				return
			}
		}
	}()
	port := 0
	for line := range lines {
		if match := regexp.MustCompile(`on http://[\d.]+:(\d+)`).FindStringSubmatch(line); match != nil {
			port, _ = strconv.Atoi(match[1])
			break
		}
	}
	if port == 0 {
		t.Fatal("the server never said its port")
	}
	srv := &e2eServer{base: base, url: fmt.Sprintf("http://127.0.0.1:%d", port), port: port, root: root}
	srv.stop = func() {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}
	t.Cleanup(srv.stop)
	go func() {
		for range lines {
		}
	}() // the rest of the log goes nowhere
	until(t, 30*time.Second, func() bool { return e2eHealthy(srv.url) }, "the server to answer /health")
	return srv
}

// until polls f until it answers true or the deadline passes.
func until(t *testing.T, within time.Duration, f func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// e2eHealthy answers whether a Huddle answers at url.
func e2eHealthy(url string) bool {
	resp, err := http.Get(url + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// api calls the server's API with the root credential.
func (s *e2eServer) api(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, s.url+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-huddle-token", s.root)
	req.Header.Set(server.ActionHeader, strings.SplitN(path, "?", 2)[0])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(raw)
}

// op runs one channel operation as who.
func (s *e2eServer) op(t *testing.T, channel, name, who, body string) string {
	t.Helper()
	status, raw := s.api(t, http.MethodPost, fmt.Sprintf("/api/c/%s/op/%s?as=%s", channel, name, who), body)
	if status != 200 {
		t.Fatalf("op %s as %s: HTTP %d: %s", name, who, status, raw)
	}
	return raw
}

// seqOf reads the seq out of an operation's answer.
func seqOf(t *testing.T, raw string) int64 {
	t.Helper()
	var answer struct {
		Result struct {
			Seq int64 `json:"seq"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(raw), &answer); err != nil {
		t.Fatalf("op answer %q", raw)
	}
	return answer.Result.Seq
}

// cli runs the CLI as one named session of channel: its own credential, kept under the shared
// state dir by session. extra comes last, so a call overrides anything.
func (s *e2eServer) cli(t *testing.T, who, channel string, extra map[string]string, args ...string) (int, string, string) {
	t.Helper()
	env := e2eEnv(s.base, map[string]string{
		"HUDDLE_URL":     s.url,
		"HUDDLE_CHANNEL": channel,
		"HUDDLE_AS":      who,
		"HUDDLE_SESSION": strings.SplitN(who, ".", 2)[0],
	}, extra)
	return e2eRun(t, huddleBin, env, args...)
}

// inviteJoin mints the creator's invite and joins each named session with it, so the test's
// sessions hold their own credentials (as e2e's one shared server did).
func (s *e2eServer) inviteJoin(t *testing.T, channel string, who ...string) {
	t.Helper()
	_, out, errOut := s.cli(t, "owner", channel, map[string]string{"HUDDLE_TOKEN": s.root}, "token", "create", "--print-join-command")
	if errOut != "" {
		t.Fatalf("token create: %s", errOut)
	}
	args := joinLineArgs(t, out)
	for _, one := range who {
		code, _, errOut := s.cli(t, one, channel, map[string]string{"HUDDLE_URL": ""}, append(append([]string{}, args...), "--as", one, "--channel", channel)...)
		if code != 0 {
			t.Fatalf("%s joins with the invite: %s", one, errOut)
		}
	}
}

// joinLineArgs reads the /huddle:join line a command printed as huddle arguments.
func joinLineArgs(t *testing.T, out string) []string {
	t.Helper()
	match := regexp.MustCompile(`/huddle:join (\S+) --token (\S+)`).FindStringSubmatch(out)
	if match == nil {
		t.Fatalf("no join line in the output: %q", out)
	}
	return []string{"join", match[1], "--token", match[2]}
}

// tokenOf reads the token out of a join line.
func tokenOf(t *testing.T, out string) string {
	t.Helper()
	match := regexp.MustCompile(`--token ([a-z0-9]{6}\.[a-z0-9]+)`).FindStringSubmatch(out)
	if match == nil {
		t.Fatalf("no token in the output: %q", out)
	}
	return match[1]
}

// e2eListen is one `huddle listen` this test started, collecting the JSON lines it prints.
type e2eListen struct {
	cmd   *exec.Cmd
	lines chan string
	done  chan error
}

// startE2EListen spawns `huddle listen` with the given env and arguments.
func startE2eListen(t *testing.T, bin string, env map[string]string, args ...string) *e2eListen {
	t.Helper()
	cmd := exec.Command(bin, append([]string{"listen"}, args...)...)
	cmd.Env = e2eEntries(env)
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("pipe listen: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start listen: %v", err)
	}
	l := &e2eListen{cmd: cmd, lines: make(chan string, 64), done: make(chan error, 1)}
	go func() {
		reader := bufio.NewReader(stdout)
		for {
			line, err := reader.ReadString('\n')
			if line = strings.TrimRight(line, "\n"); line != "" {
				l.lines <- line
			}
			if err != nil {
				close(l.lines)
				return
			}
		}
	}()
	t.Cleanup(l.stop)
	return l
}

// stop ends the listen process and waits for it.
func (l *e2eListen) stop() {
	_ = l.cmd.Process.Kill()
	select {
	case <-l.done:
	case <-time.After(5 * time.Second):
	}
}

// next waits for the next JSON line and decodes it.
func (l *e2eListen) next(t *testing.T, within time.Duration) map[string]any {
	t.Helper()
	select {
	case line, ok := <-l.lines:
		if !ok {
			t.Fatal("listen exited")
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("listen printed %q", line)
		}
		return event
	case <-time.After(within):
		t.Fatal("listen printed nothing in time")
		return nil
	}
}

// quiet answers the lines already collected, without waiting.
func (l *e2eListen) quiet() []string {
	lines := []string{}
	for {
		select {
		case line, ok := <-l.lines:
			if !ok {
				return lines
			}
			lines = append(lines, line)
		default:
			return lines
		}
	}
}

// The server runs from the binary and reports its version; the Host guard holds.
func TestE2ETheServerRunsFromTheBinaryWithItsVersion(t *testing.T) {
	base := e2eBase(t)
	srv := startE2EServer(t, huddleBin, base)
	_, out, _ := e2eRun(t, huddleBin, base, "--version")
	version := strings.TrimSpace(strings.TrimPrefix(out, "huddle "))
	status, raw := srv.api(t, http.MethodGet, "/health", "")
	if status != 200 {
		t.Fatalf("/health: HTTP %d", status)
	}
	var health struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(raw), &health); err != nil {
		t.Fatalf("/health: %v", err)
	}
	if !health.OK || health.Version != version {
		t.Errorf("/health = %+v, want ok with version %q", health, version)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.url+"/health", nil)
	req.Host = fmt.Sprintf("evil.test:%d", srv.port)
	guard, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("guard request: %v", err)
	}
	guard.Body.Close()
	if guard.StatusCode != 421 {
		t.Errorf("a foreign Host got %d, want 421", guard.StatusCode)
	}
}

// Members join with the creator's invite and get their own credentials.
func TestE2EMembersJoinWithTheCreatorsInviteAndGetTheirOwnCredentials(t *testing.T) {
	srv := startE2EServer(t, huddleBin, e2eBase(t))
	code, out, errOut := srv.cli(t, "owner", "nodeleg", map[string]string{"HUDDLE_TOKEN": srv.root}, "token", "create", "--print-join-command")
	if code != 0 {
		t.Fatalf("token create: %d %s", code, errOut)
	}
	if !regexp.MustCompile(`(?m)^join: /huddle:join 127\.0\.0\.1:\d+ --token [a-z0-9]{6}\.[a-z0-9]+`).MatchString(out) {
		t.Fatalf("join line: %q", out)
	}
	args := joinLineArgs(t, out)
	for _, who := range []string{"alpha", "beta"} {
		code, _, errOut := srv.cli(t, who, "nodeleg", map[string]string{"HUDDLE_URL": ""}, append(args, "--as", who, "--channel", "nodeleg")...)
		if code != 0 {
			t.Fatalf("%s joins: %d %s", who, code, errOut)
		}
		if !strings.Contains(errOut, "joined "+srv.url+" as "+who) {
			t.Errorf("%s join said %q", who, errOut)
		}
		if !regexp.MustCompile(`dashboard: http://127\.0\.0\.1:\d+/\?code=`).MatchString(errOut) {
			t.Errorf("the join prints the dashboard link itself: %q", errOut)
		}
		state := filepath.Join(srv.base["HOME"], ".config", "huddle", "sessions", strings.SplitN(who, ".", 2)[0]+".json")
		if _, err := os.Stat(state); err != nil {
			t.Errorf("no credential file at %s: %v", state, err)
		}
	}
	_, out, _ = srv.cli(t, "owner", "nodeleg", map[string]string{"HUDDLE_TOKEN": srv.root}, "members")
	if !(strings.Contains(out, "alpha") && strings.Index(out, "alpha") < strings.Index(out, "beta")) {
		t.Errorf("members listed %q", out)
	}
	code, _, _ = srv.cli(t, "intruder", "nodeleg", nil, "status")
	if code == 0 {
		t.Error("an intruder with no credential ran an operation")
	}
}

// A message, an ask that wakes a long-poll, the reply.
func TestE2EEventsAMessageAnAskThatWakesALongPollTheReply(t *testing.T) {
	srv := startE2EServer(t, huddleBin, e2eBase(t))
	srv.inviteJoin(t, "nodeleg", "alpha", "beta")
	waitEnv := e2eEnv(srv.base, map[string]string{
		"HUDDLE_URL":     srv.url,
		"HUDDLE_CHANNEL": "nodeleg",
		"HUDDLE_AS":      "beta",
		"HUDDLE_SESSION": "beta",
	})
	wait := exec.Command(huddleBin, "wait", "--timeout", "30")
	wait.Env = e2eEntries(waitEnv)
	var out strings.Builder
	wait.Stdout = &out
	wait.Stderr = io.Discard
	if err := wait.Start(); err != nil {
		t.Fatalf("start wait: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	if code, _, errOut := srv.cli(t, "alpha", "nodeleg", nil, "send", "beta", "ready for the schema?", "--ask"); code != 0 {
		_ = wait.Process.Kill()
		t.Fatalf("send: %d %s", code, errOut)
	}
	exited := make(chan error, 1)
	go func() { exited <- wait.Wait() }()
	select {
	case err := <-exited:
		if err == nil || wait.ProcessState.ExitCode() != 3 {
			t.Fatalf("wait exited %v, want 3 on a message for you (it printed %q)", err, out.String())
		}
	case <-time.After(20 * time.Second):
		_ = wait.Process.Kill()
		t.Fatal("wait never exited")
	}
	var event struct {
		Msg string `json:"msg"`
		Seq int64  `json:"seq"`
	}
	if err := json.Unmarshal([]byte(out.String()), &event); err != nil {
		t.Fatalf("wait printed %q", out.String())
	}
	if event.Msg != "ready for the schema?" {
		t.Errorf("wait printed %+v", event)
	}
	if code, _, errOut := srv.cli(t, "beta", "nodeleg", nil, "reply", strconv.FormatInt(event.Seq, 10), "yes"); code != 0 {
		t.Fatalf("reply: %s", errOut)
	}
	if code, _, errOut := srv.cli(t, "alpha", "nodeleg", nil, "pub", "build.ready", "api", "green"); code != 0 {
		t.Fatalf("pub: %s", errOut)
	}
	_, raw := srv.api(t, http.MethodGet, "/api/c/nodeleg/timeline?after=0", "")
	if !strings.Contains(raw, `"topic":"build.ready"`) || !strings.Contains(raw, `"from":"alpha"`) {
		t.Errorf("the timeline holds the pub: %s", raw)
	}
	code, _, _ := srv.cli(t, "beta", "nodeleg", nil, "wait", "nothing.*", "--timeout", "1")
	if code != 124 {
		t.Errorf("a wait that times out exited %d, want 124", code)
	}
}

// Tasks: one waits on another session's, and is released when it finishes.
func TestE2ETasksOneWaitsOnAnotherSessionsAndIsReleasedWhenItFinishes(t *testing.T) {
	srv := startE2EServer(t, huddleBin, e2eBase(t))
	srv.inviteJoin(t, "nodeleg", "alpha", "beta")
	cli := func(who string, args ...string) (int, string) {
		code, out, errOut := srv.cli(t, who, "nodeleg", nil, args...)
		return code, out + errOut
	}
	if code, out := cli("alpha", "new", "Ship the API", "--id", "api"); code != 0 {
		t.Fatalf("new api: %d %s", code, out)
	}
	if code, out := cli("alpha", "new", "Client", "--id", "client", "--owner", "beta", "--after", "api"); code != 0 {
		t.Fatalf("new client: %d %s", code, out)
	}
	if _, out := cli("beta", "start", "client"); !strings.Contains(out, "NOT STARTED: waits on api") {
		t.Fatalf("start client said %q", out)
	}
	if code, out := cli("alpha", "start", "api"); code != 0 {
		t.Fatalf("start api: %d %s", code, out)
	}
	code, out, errOut := srv.cli(t, "alpha", "nodeleg", nil, "finish", "tests green", "--result", "GET /orders, cursor-based")
	if code != 0 {
		t.Fatalf("finish: %d %s", code, errOut)
	}
	if !strings.Contains(out, "client") {
		t.Errorf("finish said %q, want the release named", out)
	}
	if code, _ := cli("beta", "start", "client"); code != 0 {
		t.Fatalf("start client after the finish: %d", code)
	}
	if _, out := cli("beta", "tasks"); !strings.Contains(out, "api [done]") {
		t.Errorf("tasks said %q", out)
	}
}

// Knowledge: remember, then recall it (ranked FTS5), read it, refuse a duplicate, share
// server-wide entries, verify, export.
func TestE2EKnowledgeRemembersRecallsVerifiesAndExports(t *testing.T) {
	srv := startE2EServer(t, huddleBin, e2eBase(t))
	cli := func(who string, args ...string) (int, string, string) {
		return srv.cli(t, who, "nodeleg", nil, args...)
	}
	srv.inviteJoin(t, "nodeleg", "alpha", "beta")
	// e2e.mjs shares one server across its file: the task its tasks test left behind is what the
	// search below finds, so this test makes the same task on its own server
	if code, _, errOut := cli("alpha", "new", "Ship the API", "--id", "api"); code != 0 {
		t.Fatalf("new api: %d %s", code, errOut)
	}
	_, raw := srv.api(t, http.MethodGet, "/api/c/nodeleg/board", "")
	var board struct {
		Meta struct {
			FTS bool `json:"fts"`
		} `json:"meta"`
	}
	if err := json.Unmarshal([]byte(raw), &board); err != nil {
		t.Fatalf("board: %v", err)
	}
	if !board.Meta.FTS {
		t.Errorf("FTS5 is off: %s", raw)
	}
	if code, _, errOut := cli("alpha.explore", "join", "--role", "maps the schema"); code != 0 {
		t.Fatalf("subagent join: %d %s", code, errOut)
	}
	if code, _, errOut := cli("alpha.explore", "remember", "context", "orders schema", "orders are indexed and paginated by created_at", "--tags", "orders"); code != 0 {
		t.Fatalf("remember: %s", errOut)
	}
	code, hit, errOut := cli("beta", "recall", "paginating")
	if code != 0 {
		t.Fatalf("recall: %s", errOut)
	}
	if !strings.Contains(hit, "orders schema") {
		t.Errorf("recall said %q", hit)
	}
	_, kbRaw := srv.api(t, http.MethodGet, "/api/c/nodeleg/kb?q=index", "")
	if !strings.Contains(kbRaw, "orders schema") {
		t.Errorf("kb said %s", kbRaw)
	}
	_, search := srv.api(t, http.MethodGet, "/api/c/nodeleg/search?q=api", "")
	if !strings.Contains(search, `"id":"api"`) {
		t.Errorf("search said %s", search)
	}
	dupCode, dupOut, dupErr := cli("alpha.explore", "remember", "context", "orders schema", "orders are indexed and paginated by created_at")
	if dupCode != 0 {
		t.Fatalf("duplicate remember: %d %s", dupCode, dupErr)
	}
	if !regexp.MustCompile(`(?m)^not added: #\d+ .*supersedes=`).MatchString(dupOut) {
		t.Errorf("duplicate said %q", dupOut)
	}
	code, out, errOut := cli("beta", "remember", "lesson", "node sqlite needs a flag before 22.13", "pass --experimental-sqlite", "--scope", "server")
	if code != 0 {
		t.Fatalf("scoped remember: %d %s", code, errOut)
	}
	idMatch := regexp.MustCompile(`#(\d+)`).FindStringSubmatch(out)
	if idMatch == nil {
		t.Fatalf("no id in %q", out)
	}
	id := idMatch[1]
	if code, _, errOut := cli("beta", "verify", id); code != 0 {
		t.Fatalf("verify: %s", errOut)
	}
	srv.op(t, "elsewhere", "join", "gamma", "{}")
	_, there := srv.api(t, http.MethodGet, "/api/c/elsewhere/kb?q="+queryEscape("sqlite flag"), "")
	if !strings.Contains(there, `"id":`+id) || !strings.Contains(there, `"scope":"server"`) || !strings.Contains(there, `"verified_by":"beta"`) {
		t.Errorf("the server-wide entry did not reach the other channel: %s", there)
	}
	mdCode, md, mdErr := cli("beta", "knowledge", "export", "--verified")
	if mdCode != 0 {
		t.Fatalf("export: %d %s", mdCode, mdErr)
	}
	if !strings.Contains(md, "### Lessons\n\n- **node sqlite needs a flag before 22.13**: pass --experimental-sqlite (every channel)") {
		t.Errorf("export said %q", md)
	}
	if strings.Contains(md, "orders schema") {
		t.Errorf("export carried an unverified entry: %q", md)
	}
}

// The dashboard: a one-time sign-in code becomes a cookie, then the UI and the API.
func TestE2ETheDashboardAOneTimeSignInCodeBecomesACookie(t *testing.T) {
	srv := startE2EServer(t, huddleBin, e2eBase(t))
	srv.op(t, "dash", "join", "a", "{}")
	_, raw := srv.api(t, http.MethodPost, "/api/login", "{}")
	var login struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(raw), &login); err != nil || login.Code == "" {
		t.Fatalf("login: %q", raw)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(fmt.Sprintf("%s/?code=%s", srv.url, queryEscape(login.Code)))
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 303 {
		t.Fatalf("the sign-in got %d, want 303", resp.StatusCode)
	}
	set := resp.Header.Get("set-cookie")
	if !strings.HasPrefix(set, fmt.Sprintf("huddle_session_%d=", srv.port)) {
		t.Fatalf("the cookie %q", set)
	}
	again, _ := client.Get(fmt.Sprintf("%s/?code=%s", srv.url, queryEscape(login.Code)))
	again.Body.Close()
	if again.StatusCode != 401 {
		t.Errorf("a used code got %d, want 401", again.StatusCode)
	}
	cookie := strings.SplitN(set, ";", 2)[0]
	req, _ := http.NewRequest(http.MethodGet, srv.url+"/api/whoami", nil)
	req.Header.Set("cookie", cookie)
	who, err := http.DefaultClient.Do(req)
	if err != nil || who.StatusCode != 200 {
		t.Fatalf("whoami: %v %d", err, who.StatusCode)
	}
	who.Body.Close()
	page, err := http.Get(srv.url + "/")
	if err != nil {
		t.Fatalf("the page: %v", err)
	}
	if !strings.Contains(page.Header.Get("content-type"), "text/html") {
		t.Errorf("the page's content type %q", page.Header.Get("content-type"))
	}
	body, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if !strings.Contains(string(body), "<html") {
		t.Error("the page is not HTML")
	}
	css, _ := http.Get(srv.url + "/static/app.css")
	css.Body.Close()
	if css.StatusCode != 200 {
		t.Errorf("app.css got %d", css.StatusCode)
	}
	missing, _ := http.Get(srv.url + "/static/nope.css")
	missing.Body.Close()
	if missing.StatusCode != 404 {
		t.Errorf("a missing asset got %d", missing.StatusCode)
	}
	connect, _ := http.Get(srv.url + "/connect.md")
	doc, _ := io.ReadAll(connect.Body)
	connect.Body.Close()
	if !strings.Contains(string(doc), "Huddle") {
		t.Error("connect.md does not mention Huddle")
	}
}

// A live stream (SSE) streams events, and a dropped long-poll frees its waiter.
func TestE2EALiveStreamStreamsEventsAndADroppedLongPollFreesItsWaiter(t *testing.T) {
	srv := startE2EServer(t, huddleBin, e2eBase(t))
	srv.inviteJoin(t, "nodeleg", "alpha", "beta")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.url+"/api/c/nodeleg/live", nil)
	req.Header.Set("x-huddle-token", srv.root)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("the live stream: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("content-type"); ct != "text/event-stream" {
		t.Fatalf("the live stream's content type %q", ct)
	}
	var carried sync.Mutex
	buf := ""
	go func() {
		chunk := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(chunk)
			carried.Lock()
			buf += string(chunk[:n])
			carried.Unlock()
			if err != nil {
				return
			}
		}
	}()
	said := func() string {
		carried.Lock()
		defer carried.Unlock()
		return buf
	}
	if code, _, errOut := srv.cli(t, "alpha", "nodeleg", nil, "pub", "deploy.done", "prod", "shipped over sse"); code != 0 {
		t.Fatalf("pub: %s", errOut)
	}
	until(t, 8*time.Second, func() bool { return strings.Contains(said(), "shipped over sse") },
		"the stream to carry the event (it carried "+said()+")")
	cancel()
	// a wait whose client goes away: the server must not hang on to it
	waitCtx, waitCancel := context.WithCancel(context.Background())
	waitReq, _ := http.NewRequestWithContext(waitCtx, http.MethodPost, srv.url+"/api/c/nodeleg/op/wait?as=beta",
		strings.NewReader(`{"topics":["never.*"],"timeout":30}`))
	waitReq.Header.Set("content-type", "application/json")
	waitReq.Header.Set("x-huddle-token", srv.root)
	waitReq.Header.Set(server.ActionHeader, "/api/c/nodeleg/op/wait")
	waitDone := make(chan error, 1)
	go func() {
		waitResp, err := http.DefaultClient.Do(waitReq)
		if err == nil {
			io.Copy(io.Discard, waitResp.Body)
			waitResp.Body.Close()
		}
		waitDone <- err
	}()
	time.Sleep(300 * time.Millisecond)
	waitCancel()
	select {
	case err := <-waitDone:
		if err == nil {
			t.Error("the dropped wait answered instead of failing")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the dropped wait never came back")
	}
	until(t, 5*time.Second, func() bool { return e2eHealthy(srv.url) }, "the server to stay healthy")
}

// huddle up starts the bundled server; server and down manage it.
func TestE2EHuddleUpStartsTheBundledServerAndDownStopsIt(t *testing.T) {
	base := e2eBase(t)
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	env := e2eEnv(base, map[string]string{
		"HUDDLE_HOME":        filepath.Join(proj, ".agents", "huddle"),
		"HUDDLE_URL":         url,
		"HUDDLE_CHANNEL":     "up",
		"HUDDLE_AS":          "boss",
		"HUDDLE_SESSION":     "boss",
		"CLAUDE_PROJECT_DIR": proj,
	})
	run := func(args ...string) (int, string) {
		code, out, errOut := e2eRun(t, huddleBin, env, args...)
		return code, out + errOut
	}
	code, out := run("up")
	if code != 0 {
		t.Fatalf("up: %d %s", code, out)
	}
	if !strings.Contains(out, "Huddle started at") {
		t.Fatalf("up said %q", out)
	}
	raw, err := os.ReadFile(filepath.Join(proj, ".agents", "huddle", "run", "huddle.pid"))
	if err != nil {
		t.Fatalf("no pid file: %v", err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	if pid <= 0 {
		t.Fatalf("pid file %q", raw)
	}
	t.Cleanup(func() { stopProcess(pid) })
	if said, ok := procCommand(pid); !ok || !strings.Contains(said, "serve") {
		t.Errorf("pid %d runs %q", pid, said)
	}
	if _, out := run("server"); !strings.Contains(out, "started by huddle up") {
		t.Errorf("server said %q", out)
	}
	if code, _ := run("join"); code != 0 {
		t.Errorf("join after up: %d", code)
	}
	if code, out := run("down"); code != 0 {
		t.Errorf("down: %d %s", code, out)
	}
	until(t, 5*time.Second, func() bool { return !e2eHealthy(url) }, "the server to stop")
}

// An invite after holding a root credential uses the project name, not a boolean.
func TestE2EAnInviteAfterHoldingARootCredentialUsesTheProjectName(t *testing.T) {
	srv := startE2EServer(t, huddleBin, e2eBase(t))
	sessions := filepath.Join(srv.base["HOME"], ".config", "huddle", "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sessions, "rejoin.json"),
		`{"url":"`+srv.url+`","credential":"`+srv.root+`","root":true}`)
	member := e2eEnv(srv.base, map[string]string{
		"HUDDLE_URL":     srv.url,
		"HUDDLE_CHANNEL": "team",
		"HUDDLE_AS":      "project-name",
		"HUDDLE_SESSION": "rejoin",
	})
	owner := e2eEnv(srv.base, map[string]string{
		"HUDDLE_URL":     srv.url,
		"HUDDLE_TOKEN":   srv.root,
		"HUDDLE_CHANNEL": "team",
		"HUDDLE_AS":      "lead",
		"HUDDLE_SESSION": "owner",
	})
	if code, _, errOut := e2eRun(t, huddleBin, owner, "join"); code != 0 {
		t.Fatalf("owner join: %s", errOut)
	}
	_, out, errOut := e2eRun(t, huddleBin, owner, "token", "create", "--print-join-command")
	if errOut != "" {
		t.Fatalf("token create: %s", errOut)
	}
	code, joined, joinErr := e2eRun(t, huddleBin, member, joinLineArgs(t, out)...)
	if code != 0 {
		t.Fatalf("join with the invite: %d %s", code, joinErr)
	}
	if !strings.Contains(joined, "you are project-name") || strings.Contains(joined, "you are false") {
		t.Errorf("the join said %q", joined)
	}
}

// A project's credential: the next Claude session in a project that joined is in too; another
// project is not; the invite's channel wins.
func TestE2EAProjectsCredentialTheNextSessionIsInTooAndTheInvitesChannelWins(t *testing.T) {
	srv := startE2EServer(t, huddleBin, e2eBase(t))
	projA, projB := t.TempDir(), t.TempDir()
	for _, p := range []string{projA, projB} {
		if err := os.MkdirAll(filepath.Join(p, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	state := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(filepath.Join(projA, ".agents", "huddle"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(projA, ".agents", "huddle", "huddle.json"), `{"channel":"mine","as":"web"}`)
	sess := func(proj, sid string) map[string]string {
		return e2eEnv(srv.base, map[string]string{
			"HUDDLE_URL":             srv.url,
			"HUDDLE_NO_PROJECT_CRED": "",
			"HUDDLE_HOME":            filepath.Join(proj, ".agents", "huddle"),
			"CLAUDE_PROJECT_DIR":     proj,
			"XDG_STATE_HOME":         state,
			"HUDDLE_SESSION":         sid,
		})
	}
	owner := e2eEnv(srv.base, map[string]string{
		"HUDDLE_URL":     srv.url,
		"HUDDLE_TOKEN":   srv.root,
		"HUDDLE_CHANNEL": "team",
		"HUDDLE_AS":      "lead",
		"HUDDLE_SESSION": "owner",
		"XDG_STATE_HOME": filepath.Join(t.TempDir(), "own"),
	})
	if code, _, errOut := e2eRun(t, huddleBin, owner, "join"); code != 0 {
		t.Fatalf("owner join: %s", errOut)
	}
	_, out, errOut := e2eRun(t, huddleBin, owner, "token", "create", "--print-join-command")
	if errOut != "" {
		t.Fatalf("token create: %s", errOut)
	}
	first := sess(projA, "a-1")
	code, joined, joinErr := e2eRun(t, huddleBin, first, joinLineArgs(t, out)...)
	if code != 0 {
		t.Fatalf("a-1 joins: %d %s", code, joinErr)
	}
	if !strings.Contains(joinErr, "joined channel team (the invite's); this project's file named mine") {
		t.Errorf("the join said %q", joinErr)
	}
	if !strings.Contains(joined, "channel team") {
		t.Errorf("the join output %q", joined)
	}
	_, status, _ := e2eRun(t, huddleBin, first, "status")
	if !strings.Contains(status, "channel team") {
		t.Errorf("status said %q", status)
	}
	next := sess(projA, "a-2")
	if code, _, errOut := e2eRun(t, huddleBin, next, "send", "a new session, still in"); code != 0 {
		t.Fatalf("a-2 send: %d %s", code, errOut)
	}
	_, status, _ = e2eRun(t, huddleBin, next, "status")
	if !strings.Contains(status, "you are web") {
		t.Errorf("a-2 status said %q", status)
	}
	other := sess(projB, "b-1")
	code, _, errOut = e2eRun(t, huddleBin, other, "send", "me too?")
	if code != 2 || !strings.Contains(errOut, "/huddle:invite") {
		t.Errorf("b-1 send = %d %q, want 2 with a way back in", code, errOut)
	}
}

// The invite command prints the join line itself, single use included.
func TestE2ETheInviteCommandPrintsTheJoinLineItself(t *testing.T) {
	srv := startE2EServer(t, huddleBin, e2eBase(t))
	owner := e2eEnv(srv.base, map[string]string{
		"HUDDLE_URL":     srv.url,
		"HUDDLE_TOKEN":   srv.root,
		"HUDDLE_CHANNEL": "team",
		"HUDDLE_AS":      "lead",
		"HUDDLE_SESSION": "owner",
		"CLAUDECODE":     "1",
	})
	if code, _, errOut := e2eRun(t, huddleBin, owner, "join"); code != 0 {
		t.Fatalf("owner join: %s", errOut)
	}
	code, out, errOut := e2eRun(t, huddleBin, owner, "token", "create", "--print-join-command", "--single-use")
	if code != 0 {
		t.Fatalf("token create: %d %s", code, errOut)
	}
	if !strings.Contains(out, fmt.Sprintf("/huddle:join 127.0.0.1:%d --token %s", srv.port, tokenOf(t, out))) {
		t.Errorf("the join line %q", out)
	}
	if !regexp.MustCompile(`(?m)^join: /huddle:join \S+ --token [a-z0-9]{6}\.[a-z0-9]{16}   \(valid .*single use\)$`).MatchString(out) {
		t.Errorf("the printed line %q", out)
	}
}

// The digest over the CLI: what happened since, per session, with what is blocked now.
func TestE2ETheDigestOverTheCLI(t *testing.T) {
	srv := startE2EServer(t, huddleBin, e2eBase(t))
	srv.inviteJoin(t, "shop", "api", "web")
	srv.op(t, "shop", "task_create", "api", `{"id":"b1","title":"Migrate"}`)
	srv.op(t, "shop", "task_status", "api", `{"id":"b1","status":"blocked","note":"disk full"}`)
	code, out, errOut := srv.cli(t, "web", "shop", nil, "digest", "--since", "24h")
	if code != 0 {
		t.Fatalf("digest: %d %s", code, errOut)
	}
	if !strings.Contains(out, "blocked now:") {
		t.Errorf("digest said %q", out)
	}
}

// huddle listen streams every message from the others as JSON lines, and replays from --after.
func TestE2EListenStreamsEveryMessageFromTheOthersAndReplaysFromAfter(t *testing.T) {
	srv := startE2EServer(t, huddleBin, e2eBase(t))
	srv.inviteJoin(t, "l1", "a", "b", "c")
	before := seqOf(t, srv.op(t, "l1", "send", "a", `{"to":"b","msg":"earlier"}`))
	listenEnv := e2eEnv(srv.base, map[string]string{
		"HUDDLE_URL":     srv.url,
		"HUDDLE_CHANNEL": "l1",
		"HUDDLE_AS":      "b",
		"HUDDLE_SESSION": "b",
	})
	l := startE2eListen(t, huddleBin, listenEnv, "--after", strconv.FormatInt(before-1, 10))
	if got := l.next(t, 8*time.Second); got["msg"] != "earlier" {
		t.Fatalf("the replay gave %v", got)
	}
	srv.op(t, "l1", "send", "a", `{"to":"c","msg":"for c"}`)
	srv.op(t, "l1", "publish", "b", `{"topic":"mine.x","msg":"own"}`)
	srv.op(t, "l1", "send", "c", `{"to":"b","msg":"for b","ask":true}`)
	second := l.next(t, 8*time.Second)
	third := l.next(t, 8*time.Second)
	if second["msg"] != "for c" || second["from"] != "a" || second["to"] != "c" {
		t.Errorf("the second line %v", second)
	}
	if third["msg"] != "for b" || third["from"] != "c" || third["to"] != "b" {
		t.Errorf("the third line %v", third)
	}
	if third["needs_reply"] != true {
		t.Errorf("the ask's needs_reply %v", third["needs_reply"])
	}
	if third["channel"] != "l1" {
		t.Errorf("the channel field %v", third["channel"])
	}
	l.stop()
	if extra := l.quiet(); len(extra) != 0 {
		t.Errorf("the own publish leaked: %v", extra)
	}
}

// huddle listen follows the channel and its listen channels together.
func TestE2EListenFollowsTheChannelAndItsListenChannelsTogether(t *testing.T) {
	srv := startE2EServer(t, huddleBin, e2eBase(t))
	srv.inviteJoin(t, "m1", "a")
	srv.op(t, "m2", "join", "a", `{}`)
	listenEnv := e2eEnv(srv.base, map[string]string{
		"HUDDLE_URL":     srv.url,
		"HUDDLE_TOKEN":   srv.root,
		"HUDDLE_CHANNEL": "m1",
		"HUDDLE_AS":      "b",
		"HUDDLE_SESSION": "b",
		"HUDDLE_LISTEN":  "m2",
	})
	l := startE2eListen(t, huddleBin, listenEnv)
	defer l.stop()
	time.Sleep(1500 * time.Millisecond) // both streams connect
	srv.op(t, "m1", "send", "a", `{"msg":"in m1"}`)
	srv.op(t, "m2", "send", "a", `{"msg":"in m2"}`)
	first := l.next(t, 8*time.Second)
	second := l.next(t, 8*time.Second)
	pair := [][2]string{{first["channel"].(string), first["msg"].(string)}, {second["channel"].(string), second["msg"].(string)}}
	sort.Slice(pair, func(i, j int) bool { return pair[i][0] < pair[j][0] })
	if pair[0] != [2]string{"m1", "in m1"} || pair[1] != [2]string{"m2", "in m2"} {
		t.Errorf("listen gave %v", pair)
	}
}

// huddle listen --state resumes where the last listen stopped, missing nothing in between.
func TestE2EListenStateResumesWhereTheLastListenStopped(t *testing.T) {
	srv := startE2EServer(t, huddleBin, e2eBase(t))
	srv.inviteJoin(t, "r1", "a")
	listenEnv := e2eEnv(srv.base, map[string]string{
		"HUDDLE_URL":     srv.url,
		"HUDDLE_TOKEN":   srv.root,
		"HUDDLE_CHANNEL": "r1",
		"HUDDLE_AS":      "b",
		"HUDDLE_SESSION": "b",
		"HUDDLE_LISTEN":  "",
	})
	state := filepath.Join(t.TempDir(), "lanes.json")
	first := seqOf(t, srv.op(t, "r1", "send", "a", `{"msg":"one"}`))
	listen := func(until int) []string {
		l := startE2eListen(t, huddleBin, listenEnv, "--after", strconv.FormatInt(first-1, 10), "--state", state)
		var got []string
		for len(got) < until {
			got = append(got, l.next(t, 8*time.Second)["msg"].(string))
		}
		l.stop()
		return got
	}
	if got := listen(1); len(got) != 1 || got[0] != "one" {
		t.Fatalf("the first listen gave %v", got)
	}
	srv.op(t, "r1", "send", "a", `{"msg":"two, while nobody listens"}`)
	if got := listen(1); len(got) != 1 || got[0] != "two, while nobody listens" {
		t.Fatalf("the second listen gave %v, want only what it missed", got)
	}
}

// huddle listen survives a stream that goes quiet: it reconnects, and re-reads what it missed.
func TestE2EListenSurvivesAStreamThatGoesQuiet(t *testing.T) {
	var mu sync.Mutex
	var events []string
	streams := 0
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/live"):
			mu.Lock()
			streams++
			mu.Unlock()
			w.Header().Set("content-type", "text/event-stream")
			io.WriteString(w, "data: {\"type\":\"hello\",\"data\":{}}\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-r.Context().Done() // hello, then nothing, ever
		case strings.HasSuffix(r.URL.Path, "/timeline"):
			after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
			mu.Lock()
			defer mu.Unlock()
			out := []string{}
			for _, raw := range events {
				var event struct {
					Seq int64 `json:"seq"`
				}
				if json.Unmarshal([]byte(raw), &event) != nil || event.Seq <= after {
					continue
				}
				out = append(out, raw)
			}
			io.WriteString(w, "["+strings.Join(out, ",")+"]")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(fake.Close)
	listenEnv := e2eEnv(e2eBase(t), map[string]string{
		"HUDDLE_URL":              fake.URL,
		"HUDDLE_CHANNEL":          "q",
		"HUDDLE_AS":               "b",
		"HUDDLE_SESSION":          "b",
		"HUDDLE_LISTEN":           "",
		"HUDDLE_LISTEN_RESYNC_MS": "300",
		"HUDDLE_LISTEN_QUIET_MS":  "800",
	})
	l := startE2eListen(t, huddleBin, listenEnv, "--after", "1")
	until(t, 8*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return streams >= 1 }, "the stream to open")
	mu.Lock()
	events = []string{`{"seq":2,"from":"a","to":null,"topic":"msg","msg":"sent while the stream was quiet"}`}
	mu.Unlock()
	if got := l.next(t, 8*time.Second); got["msg"] != "sent while the stream was quiet" {
		t.Fatalf("the periodic re-read gave %v", got)
	}
	until(t, 8*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return streams >= 2 }, "the quiet stream to be dropped and opened again")
	time.Sleep(700 * time.Millisecond)
	if extra := l.quiet(); len(extra) != 0 {
		t.Errorf("re-reads and reconnects showed something twice: %v", extra)
	}
}

// A serve no one handed a root to (a service manager's, a terminal's) leaves it in the home's
// run directory, so `huddle open --print` on the same machine signs the owner in — and a clean
// stop takes the key with it.
func TestE2EAServeStartedByHandStillSignsTheOwnerIn(t *testing.T) {
	base := e2eBase(t)
	homeDir := filepath.Join(t.TempDir(), "home")
	data := filepath.Join(t.TempDir(), "data")
	port := freePort(t)
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	// the home knows its port the way a set-up machine's does: saved in config.toml
	home.SavePort(home.Home{Root: homeDir}, port, false)
	cmd := exec.Command(huddleBin, "serve")
	cmd.Env = e2eEntries(e2eEnv(base, map[string]string{
		"HUDDLE_HOME": homeDir,
		"HUDDLE_DATA": data,
		"PORT":        strconv.Itoa(port),
	}))
	// the server writes while the test reads: a locked buffer, not a strings.Builder
	var out syncBuffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the server: %v", err)
	}
	t.Cleanup(func() { stopProcess(cmd.Process.Pid) })
	until(t, 15*time.Second, func() bool { return e2eHealthy(url) }, "the server to answer /health")

	key := filepath.Join(homeDir, "run", "owner.key")
	raw, err := os.ReadFile(key)
	if err != nil {
		t.Fatalf("no owner key: %v", err)
	}
	root := strings.TrimSpace(string(raw))
	if root == "" {
		t.Fatal("the owner key is empty")
	}
	if strings.Contains(out.String(), root) {
		t.Errorf("the server's output says its root credential")
	}
	info, err := os.Stat(key)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("owner.key mode %o, want 0600", info.Mode().Perm())
	}

	code, printed, errOut := e2eRun(t, huddleBin, e2eEnv(base, map[string]string{"HUDDLE_HOME": homeDir}), "open", "--print")
	if code != 0 {
		t.Fatalf("open --print: %d %s %s", code, printed, errOut)
	}
	if !strings.Contains(printed, url+"/?code=") {
		t.Fatalf("open --print said %q, want a link with a code", printed)
	}

	// a clean stop takes the key with it
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("stop the server: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the server never stopped")
	}
	if _, err := os.Stat(key); !os.IsNotExist(err) {
		t.Errorf("owner.key after a clean stop: %v, want it gone", err)
	}
}

// The server restarted more roots than it keeps digests of, so the credential this session
// saved (the first start's) no longer works: the owner key of the last start still opens the
// dashboard.
func TestE2EARootThatAgedOutOverNineRestartsStillOpensThroughTheOwnerKey(t *testing.T) {
	base := e2eBase(t)
	homeDir := filepath.Join(t.TempDir(), "home")
	data := filepath.Join(t.TempDir(), "data")
	port := freePort(t)
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	run := filepath.Join(homeDir, "run")
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(run, "huddle.port"), strconv.Itoa(port))
	env := e2eEnv(base, map[string]string{
		"HUDDLE_HOME": homeDir,
		"HUDDLE_DATA": data,
		"PORT":        strconv.Itoa(port),
	})
	var stop func()
	t.Cleanup(func() {
		if stop != nil {
			stop()
		}
	})
	start := func() {
		cmd := exec.Command(huddleBin, "serve")
		cmd.Env = e2eEntries(env)
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		if err := cmd.Start(); err != nil {
			t.Fatalf("start the server: %v", err)
		}
		stop = func() {
			_ = cmd.Process.Signal(os.Interrupt)
			done := make(chan struct{})
			go func() { _ = cmd.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
		}
		until(t, 15*time.Second, func() bool { return e2eHealthy(url) }, "the server to answer /health")
	}

	start()
	first, err := os.ReadFile(filepath.Join(run, "owner.key"))
	if err != nil {
		t.Fatalf("no owner key: %v", err)
	}
	// the session that started the first server holds its root, the way `huddle up` keeps it
	sessions := filepath.Join(homeDir, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sessions, "terminal.json"),
		fmt.Sprintf(`{"url":%q,"credential":%q,"root":true}`, url, strings.TrimSpace(string(first))))
	for i := 0; i < 8; i++ {
		stop()
		start()
	}

	link := Dashboard(context.Background(), world(t, t.TempDir(), map[string]string{"HUDDLE_HOME": homeDir}), url, "", 1500)
	if !strings.Contains(link, "/?code=") {
		t.Fatalf("Dashboard = %q, want a link with a code through the owner key", link)
	}
	stop()
}
