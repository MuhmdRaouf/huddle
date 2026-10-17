package server

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The suite's server runs as this binary re-executed in child mode: the real listener, the real
// environment wiring and the real process lifecycle, the way the plugin spawns it.
const childEnv = "HUDDLE_SERVER_CHILD"

// runChildServer is the child mode: this binary as the real server, its environment the test's.
func runChildServer() {
	version := os.Getenv("HUDDLE_TEST_VERSION")
	if version == "" {
		version = "0.0.0"
	}
	o := OptionsFromEnv(version, os.Getenv, os.Stdout, os.Stderr, os.Stdin)
	o.Environ = os.Environ() // the radar tab reads the environment the test pinned
	o.Signals = true
	_ = Serve(context.Background(), o)
}

func TestMain(m *testing.M) {
	if os.Getenv(childEnv) == "serve" {
		runChildServer()
		// no test matches the child's -test.run; m.Run's teardown flushes a coverage run's counters
		os.Exit(m.Run())
	}
	// Every throwaway directory (tempDir's, the children's) lands under one root removed at the end.
	root, err := os.MkdirTemp("", "huddle-server-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("TMPDIR", root)
	code := m.Run()
	os.RemoveAll(root)
	os.Exit(code)
}

// childServer is one server process on a port of its own, ready to answer.
type childServer struct {
	cmd       *exec.Cmd
	port      int
	base      string
	token     string
	dataDir   string
	home      string
	radarHome string
	config    string
	log       *syncBuffer
}

// syncBuffer collects the child's stdout from any goroutine.
type syncBuffer struct {
	mu    sync.Mutex
	lines []string
}

func (b *syncBuffer) add(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, line)
}

func (b *syncBuffer) joined() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.lines, "\n")
}

// Write captures one whole line of the child's output.
func (b *syncBuffer) Write(p []byte) (int, error) {
	line := strings.TrimSuffix(string(p), "\n")
	if line != "" {
		b.add(line)
	}
	return len(p), nil
}

// tempDir answers a throwaway directory (no *testing.T in TestMain's hands).
func tempDir(prefix string) string {
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		panic(err)
	}
	return dir
}

// spawnServer starts one child server with env over the defaults. A value of "" removes the
// variable (PORT: "" leaves the port unset, so the server draws one).
func spawnServer(env map[string]string) (*childServer, error) {
	full := map[string]string{
		"PORT":                "0",
		"HUDDLE_TEST_VERSION": "9.8.7",
		"HUDDLE_DATA":         tempDir("huddle-data-"),
		"HUDDLE_HOME":         tempDir("huddle-home-"),
		"RADAR_HOME":          tempDir("huddle-radar-"),
		"HUDDLE_NOTIFY_LOG":   filepath.Join(tempDir("huddle-notify-"), "notify.log"),
		// the radar tab reads HOME and CLAUDE_CONFIG_DIR for transcripts and state; a pass-through
		// would tail the real ~/.claude, and a shell exporting the XDG dirs would point the child
		// at the real ones
		"HOME":              tempDir("huddle-home-env-"),
		"CLAUDE_CONFIG_DIR": tempDir("huddle-claude-"),
		"XDG_CONFIG_HOME":   tempDir("huddle-xdg-config-"),
		"XDG_STATE_HOME":    tempDir("huddle-xdg-state-"),
		"XDG_DATA_HOME":     tempDir("huddle-xdg-data-"),
	}
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}
	full["HUDDLE_TOKEN"] = "test-" + hex.EncodeToString(token)
	for k, v := range env {
		if v == "" {
			delete(full, k)
			continue
		}
		if k == "HUDDLE_TOKEN" || k == "HUDDLE_DATA" || k == "HUDDLE_HOME" {
			full[k] = v
			continue
		}
		full[k] = v
	}
	full[childEnv] = "serve"

	childArgs := []string{"-test.run=^$", "-test.v=false"}
	if dir := os.Getenv("GOCOVERDIR"); dir != "" && testing.CoverMode() != "" {
		// a coverage-instrumented run keeps the children's counts too
		childArgs = append(childArgs, "-test.gocoverdir="+dir)
	}
	srv := &childServer{
		cmd:       exec.Command(os.Args[0], childArgs...),
		token:     full["HUDDLE_TOKEN"],
		dataDir:   full["HUDDLE_DATA"],
		home:      full["HUDDLE_HOME"],
		radarHome: full["RADAR_HOME"],
		log:       &syncBuffer{},
	}
	stdout, err := srv.cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	srv.cmd.Stderr = io.Discard
	environment := []string{}
	for _, entry := range os.Environ() {
		name := entry
		if at := strings.Index(entry, "="); at >= 0 {
			name = entry[:at]
		}
		if _, ours := full[name]; ours {
			continue
		}
		environment = append(environment, entry)
	}
	for k, v := range full {
		environment = append(environment, k+"="+v)
	}
	srv.cmd.Env = environment
	if err := srv.cmd.Start(); err != nil {
		return nil, err
	}

	portc := make(chan int, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			srv.log.add(line)
			if m := regexp.MustCompile(`on http://[\d.]+:(\d+)`).FindStringSubmatch(line); m != nil {
				p, _ := strconv.Atoi(m[1])
				portc <- p
			}
		}
	}()
	select {
	case srv.port = <-portc:
	case <-time.After(30 * time.Second):
		_ = srv.cmd.Process.Kill()
		return nil, fmt.Errorf("server did not start: %s", srv.log.joined())
	}
	srv.base = fmt.Sprintf("http://127.0.0.1:%d", srv.port)
	probe := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}
	deadline := time.Now().Add(15 * time.Second)
	for {
		response, err := probe.Get(srv.base + "/health")
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode == 200 {
				return srv, nil
			}
		}
		if time.Now().After(deadline) {
			_ = srv.cmd.Process.Kill()
			return nil, fmt.Errorf("server did not answer /health: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// startServer spawns a server the test owns; nil env takes the defaults.
func startServer(t *testing.T, env map[string]string) *childServer {
	t.Helper()
	srv, err := spawnServer(env)
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(srv.kill)
	return srv
}

// kill ends the child: a signal first, so a coverage run keeps its counters, then the hard end.
func (s *childServer) kill() {
	if s.cmd.Process == nil {
		return
	}
	_ = s.cmd.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() { _ = s.cmd.Wait(); close(done) }()
	select {
	case <-done:

	case <-time.After(5 * time.Second):

		_ = s.cmd.Process.Kill()
		<-done
	}
}

// request runs one HTTP request; a "Host" header names the Host header, not a real one. A write
// that names no action gets the route's path as one, the way the real clients do; the guard
// tests build their own requests instead.
func (s *childServer) request(ctx context.Context, method, path, body string, headers map[string]string) (int, http.Header, string, error) {
	var payload io.Reader
	if body != "" {
		payload = strings.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, s.base+path, payload)
	if err != nil {
		return 0, nil, "", err
	}
	if host, ok := headers["Host"]; ok {
		request.Host = host
	}
	for name, value := range headers {
		if name == "Host" {
			continue
		}
		request.Header.Set(name, value)
	}
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
		if request.Header.Get(ActionHeader) == "" {
			request.Header.Set(ActionHeader, strings.SplitN(path, "?", 2)[0])
		}
	}
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, "", err
	}
	defer response.Body.Close()
	text, err := io.ReadAll(response.Body)
	if err != nil {
		return response.StatusCode, response.Header, "", err
	}
	return response.StatusCode, response.Header, string(text), nil
}

func (s *childServer) get(t *testing.T, path string, headers map[string]string) (int, http.Header, string) {
	t.Helper()
	if headers == nil {
		headers = map[string]string{}
	}
	if _, ok := headers["x-huddle-token"]; !ok {
		headers["x-huddle-token"] = s.token
	}
	code, header, body, err := s.request(context.Background(), http.MethodGet, path, "", headers)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return code, header, body
}

func (s *childServer) post(t *testing.T, path, body string, headers map[string]string) (int, string) {
	t.Helper()
	if headers == nil {
		headers = map[string]string{}
	}
	if _, ok := headers["content-type"]; !ok {
		headers["content-type"] = "application/json"
	}
	if _, ok := headers["x-huddle-token"]; !ok {
		headers["x-huddle-token"] = s.token
	}
	code, _, out, err := s.request(context.Background(), http.MethodPost, path, body, headers)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return code, out
}

// op runs one channel operation as a session.
func (s *childServer) op(t *testing.T, ch, name, as string, body string) (int, map[string]any) {
	t.Helper()
	code, out := s.post(t, fmt.Sprintf("/api/c/%s/op/%s?as=%s", ch, name, as), body, nil)
	return code, bodyMap(t, out)
}

// rpc runs one MCP request and answers its parsed body.
func (s *childServer) rpc(t *testing.T, ch, as, method, params string, id int) map[string]any {
	t.Helper()
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`, id, method, params)
	if params == "" {
		body = fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q}`, id, method)
	}
	_, out := s.post(t, fmt.Sprintf("/mcp/%s?as=%s", ch, as), body, nil)
	return bodyMap(t, out)
}

func until(ms time.Duration, check func() bool) bool {
	deadline := time.Now().Add(ms)
	for !check() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
	return true
}

func TestHTTPSuite(t *testing.T) {
	shared := startServer(t, nil)
	t.Run("/health reports the plugin's version", func(t *testing.T) {
		code, _, body := shared.get(t, "/health", nil)
		if code != 200 {
			t.Fatalf("/health = %d", code)
		}
		health := bodyMap(t, body)
		version, _ := health["version"].(string)
		if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(version) {
			t.Fatalf("version %q is not semver", version)
		}
		if version != "9.8.7" { // what this run handed the server
			t.Fatalf("version = %q, want the one the server was started with", version)
		}
	})

	t.Run("guard: wrong Host 421, form post 403, foreign Origin 403", func(t *testing.T) {
		if code, header, _ := shared.get(t, "/health", map[string]string{"Host": "evil.example:80"}); code != 421 || header.Get("Content-Type") != "text/plain;charset=utf-8" {
			t.Fatalf("a foreign host = %d %q, want 421 text/plain;charset=utf-8", code, header.Get("Content-Type"))
		}
		if code, _, _ := shared.get(t, "/health", map[string]string{"Host": fmt.Sprintf("localhost:%d", shared.port)}); code != 200 {
			t.Fatalf("localhost for our port = %d, want 200", code)
		}
		if code, _, _ := shared.get(t, "/health", map[string]string{"Host": "localhost:1"}); code != 421 { // only for our port
			t.Fatalf("another port's localhost = %d, want 421", code)
		}
		code, header, _, _ := shared.request(context.Background(), http.MethodPost, "/api/c/g/op/join?as=a", "{}",
			map[string]string{"content-type": "text/plain"})
		if code != 403 || header.Get("Content-Type") != "text/plain;charset=utf-8" {
			t.Fatalf("a form post = %d %q, want 403 text/plain;charset=utf-8", code, header.Get("Content-Type"))
		}
		code, _, _, _ = shared.request(context.Background(), http.MethodPost, "/api/c/g/op/join?as=a", "{}",
			map[string]string{"content-type": "application/json", "Origin": "https://evil.example"})
		if code != 403 {
			t.Fatalf("a foreign origin = %d, want 403", code)
		}
	})

	t.Run("guard: HUDDLE_HOSTS adds hosts; loopback for the port always passes", func(t *testing.T) {
		srv := startServer(t, map[string]string{"HUDDLE_HOSTS": "huddle.test"})
		if code, _, _ := srv.get(t, "/health", nil); code != 200 {
			t.Fatalf("default host = %d, want 200", code)
		}
		if code, _, _ := srv.get(t, "/health", map[string]string{"Host": fmt.Sprintf("localhost:%d", srv.port)}); code != 200 {
			t.Fatalf("localhost = %d, want 200", code)
		}
		if code, _, _ := srv.get(t, "/health", map[string]string{"Host": "huddle.test"}); code != 200 {
			t.Fatalf("HUDDLE_HOSTS entry = %d, want 200", code)
		}
		if code, _, _ := srv.get(t, "/health", map[string]string{"Host": "evil.example"}); code != 421 {
			t.Fatalf("another host = %d, want 421", code)
		}
	})

	t.Run("join creates the channel; ops need an identity; unknown channel 404 for reads", func(t *testing.T) {
		code, out := shared.op(t, "c1", "join", "alpha", `{"role": "builds"}`)
		if code != 200 {
			t.Fatalf("join = %d %s", code, out)
		}
		result, _ := out["result"].(map[string]any)
		if result["me"] != "alpha" {
			t.Fatalf("join result = %v", out)
		}
		if code, _ := shared.post(t, "/api/c/c1/op/status", "{}", nil); code != 400 {
			t.Fatalf("an op without an identity = %d, want 400", code)
		}
		if code, _, _ := shared.get(t, "/api/c/nope/board", nil); code != 404 {
			t.Fatalf("an unknown channel's board = %d, want 404", code)
		}
		_, _, body := shared.get(t, "/api/channels", nil)
		var list []map[string]any
		if err := json.Unmarshal([]byte(body), &list); err != nil {
			t.Fatalf("channels: %v", err)
		}
		found := false
		for _, c := range list {
			if c["name"] == "c1" {
				found = true
			}
		}
		if !found {
			t.Fatalf("channels = %s, want c1 among them", body)
		}
	})

	t.Run("a long-poll wait is woken by another client's publish in milliseconds", func(t *testing.T) {
		shared.op(t, "c2", "join", "a", "{}")
		shared.op(t, "c2", "join", "b", "{}")
		shared.op(t, "c2", "ack", "b", `{"seq": 1000}`)
		started := time.Now()
		answer := make(chan map[string]any, 1)
		go func() {
			code, out := shared.op(t, "c2", "wait", "b", `{"topics": ["go.*"], "timeout": 10}`)
			if code == 200 {
				answer <- out
			} else {
				answer <- map[string]any{"error": out}
			}
		}()
		time.Sleep(50 * time.Millisecond)
		shared.op(t, "c2", "publish", "a", `{"topic": "go.now", "msg": "x"}`)
		select {
		case out := <-answer:
			result, _ := out["result"].(map[string]any)
			if result["kind"] != "event" || result["topic"] != "go.now" {
				t.Fatalf("wait result = %v", out)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the wait never answered")
		}
		if elapsed := time.Since(started); elapsed >= time.Second {
			t.Fatalf("the wake-up took %s", elapsed)
		}
	})

	t.Run("a client that drops its wait frees the waiter (no leak, no stale wake)", func(t *testing.T) {
		shared.op(t, "c3", "join", "a", "{}")
		shared.op(t, "c3", "join", "b", "{}")
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct {
			code int
			body string
			err  error
		}, 1)
		go func() {
			code, _, body, err := shared.request(ctx, http.MethodPost, "/api/c/c3/op/wait?as=b",
				`{"topics": ["never"], "timeout": 30}`, map[string]string{"content-type": "application/json", "x-huddle-token": shared.token})
			done <- struct {
				code int
				body string
				err  error
			}{code, body, err}
		}()
		time.Sleep(100 * time.Millisecond)
		cancel()
		select {
		case answer := <-done:
			if answer.err == nil {
				t.Fatalf("the dropped wait still answered: %d %s", answer.code, answer.body)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the dropped request never ended")
		}
		if !until(3*time.Second, func() bool {
			_, _, body := shared.get(t, "/api/c/c3/sessions", nil)
			var sessions struct {
				Sessions []struct {
					Name  string `json:"name"`
					State string `json:"state"`
				} `json:"sessions"`
			}
			if err := json.Unmarshal([]byte(body), &sessions); err != nil {
				return false
			}
			for _, s := range sessions.Sessions {
				if s.Name == "b" {
					return s.State != "waiting"
				}
			}
			return false
		}) {
			t.Fatal("b is still marked waiting after its client dropped the wait")
		}
	})

	t.Run("MCP: initialize, tools/list, tools/call, errors as tool results", func(t *testing.T) {
		init := shared.rpc(t, "c4", "alpha", "initialize", `{"protocolVersion": "2025-06-18", "capabilities": {}}`, 1)
		result, _ := init["result"].(map[string]any)
		serverInfo, _ := result["serverInfo"].(map[string]any)
		if serverInfo["name"] != "huddle" {
			t.Fatalf("initialize = %v", init)
		}
		capabilities, _ := result["capabilities"].(map[string]any)
		experimental, _ := capabilities["experimental"].(map[string]any)
		if _, ok := experimental["claude/channel"]; !ok {
			t.Fatalf("capabilities = %v, want the claude/channel experiment", capabilities)
		}
		tools := shared.rpc(t, "c4", "alpha", "tools/list", "", 2)
		tresult, _ := tools["result"].(map[string]any)
		list, _ := tresult["tools"].([]any)
		names := map[string]bool{}
		for _, item := range list {
			if tool, ok := item.(map[string]any); ok {
				if name, ok := tool["name"].(string); ok {
					names[name] = true
				}
			}
		}
		for _, want := range []string{"join", "wait", "gate", "task_create", "remember", "recall", "pause", "pass"} {
			if !names[want] {
				t.Fatalf("tools/list misses %s (has %v)", want, names)
			}
		}
		join := shared.rpc(t, "c4", "alpha", "tools/call", `{"name": "join", "arguments": {"role": "r"}}`, 3)
		jresult, _ := join["result"].(map[string]any)
		content, _ := jresult["content"].([]any)
		first, _ := content[0].(map[string]any)
		if !regexp.MustCompile(`channel c4 · you are alpha`).MatchString(fmt.Sprint(first["text"])) {
			t.Fatalf("join tool text = %v", join)
		}
		bare := shared.rpc(t, "c4", "alpha", "initialize", `{}`, 9)
		if _, ok := bare["result"]; !ok {
			t.Fatalf("initialize without protocolVersion = %v", bare)
		}
		bad := shared.rpc(t, "c4", "alpha", "tools/call", `{"name": "reply", "arguments": {"seq": 999, "msg": "x"}}`, 4)
		bresult, _ := bad["result"].(map[string]any)
		if bresult["isError"] != true {
			t.Fatalf("a failed tool = %v", bad)
		}
		unknown := shared.rpc(t, "c4", "alpha", "tools/call", `{"name": "nope", "arguments": {}}`, 5)
		uerr, _ := unknown["error"].(map[string]any)
		if uerr["code"] != float64(-32602) {
			t.Fatalf("an unknown tool = %v", unknown)
		}
	})

	t.Run("SSE with ?as= carries everyone else's events, also those addressed to another session", func(t *testing.T) {
		shared.op(t, "c5", "join", "a", "{}")
		shared.op(t, "c5", "join", "b", "{}")
		shared.op(t, "c5", "join", "c", "{}")
		ctx, cancel := context.WithCancel(context.Background())
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, shared.base+"/api/c/c5/live?as=b", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("x-huddle-token", shared.token)
		response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
		if err != nil {
			t.Fatal(err)
		}
		got := make(chan string, 64)
		go func() {
			defer close(got)
			scanner := bufio.NewScanner(response.Body)
			scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "data: ") {
					got <- strings.TrimPrefix(line, "data: ")
				}
			}
		}()
		time.Sleep(150 * time.Millisecond)
		shared.op(t, "c5", "send", "a", `{"to": "c", "msg": "private to c"}`)
		shared.op(t, "c5", "send", "a", `{"to": "b", "msg": "for b"}`)
		shared.op(t, "c5", "publish", "b", `{"topic": "mine.x", "msg": "own"}`)
		var msgs []string
		deadline := time.After(5 * time.Second)
	collect:
		for {
			select {
			case line, open := <-got:
				if !open {
					break collect
				}
				var m struct {
					Type string `json:"type"`
					Data struct {
						Msg string `json:"msg"`
					} `json:"data"`
				}
				if err := json.Unmarshal([]byte(line), &m); err != nil {
					continue
				}
				if m.Type == "event" {
					msgs = append(msgs, m.Data.Msg)
					if len(msgs) == 2 {
						break collect
					}
				}
			case <-deadline:
				break collect
			}
		}
		cancel()
		_ = response.Body.Close()
		if len(msgs) != 2 || msgs[0] != "private to c" || msgs[1] != "for b" {
			t.Fatalf("the stream carried %v, want the private to c and the one for b", msgs)
		}
	})

	t.Run("MCP: ops without a text of their own answer in one line, not a JSON object (json: true still gives it)", func(t *testing.T) {
		shared.op(t, "c11", "join", "a", "{}")
		shared.op(t, "c11", "join", "b", "{}")
		callAs := func(session, params string) string {
			t.Helper()
			reply := shared.rpc(t, "c11", session, "tools/call", params, 20)
			result, _ := reply["result"].(map[string]any)
			content, _ := result["content"].([]any)
			first, _ := content[0].(map[string]any)
			return fmt.Sprint(first["text"])
		}
		call := func(params string) string {
			t.Helper()
			return callAs("a", params)
		}
		if text := call(`{"name": "send", "arguments": {"to": "b", "msg": "hi", "ask": true}}`); !regexp.MustCompile(`^#\d+ sent to b \(an ask`).MatchString(text) {
			t.Fatalf("send text = %q", text)
		}
		if text := call(`{"name": "remember", "arguments": {"kind": "fact", "title": "short answers", "body": "one line each"}}`); !regexp.MustCompile(`^#\d+ remembered: short answers$`).MatchString(text) {
			t.Fatalf("remember text = %q", text)
		}
		if text := call(`{"name": "task_create", "arguments": {"title": "ship it", "owner": "b", "id": "ship"}}`); text != "created ship → b (ready)" {
			t.Fatalf("task_create text = %q", text)
		}
		if text := call(`{"name": "finish", "arguments": {"id": "ship", "note": "done by a"}}`); !regexp.MustCompile(`^done ship; released: none; next: `).MatchString(text) {
			t.Fatalf("finish text = %q", text)
		}
		if text := call(`{"name": "publish", "arguments": {"topic": "t.x", "msg": "note"}}`); !regexp.MustCompile(`^#\d+ published \(t\.x\) to everyone$`).MatchString(text) {
			t.Fatalf("publish text = %q", text)
		}
		ask := callAs("b", `{"name": "send", "arguments": {"to": "a", "msg": "need it", "ask": true}}`)
		seq := regexp.MustCompile(`^#(\d+) sent`).FindStringSubmatch(ask)
		if seq == nil {
			t.Fatalf("ask text = %q", ask)
		}
		if text := call(`{"name": "reply", "arguments": {"seq": ` + seq[1] + `, "msg": "here"}}`); !regexp.MustCompile(`^#\d+ replied$`).MatchString(text) {
			t.Fatalf("reply text = %q", text)
		}
		if text := call(`{"name": "task_create", "arguments": {"title": "blocker", "owner": "a", "id": "first"}}`); text != "created first → a (ready)" {
			t.Fatalf("task_create first text = %q", text)
		}
		if text := call(`{"name": "task_create", "arguments": {"title": "tail", "owner": "b", "id": "tail", "after": ["first"]}}`); text != "created tail → b (waits on first)" {
			t.Fatalf("task_create tail text = %q", text)
		}
		if text := call(`{"name": "task_create", "arguments": {"title": "follow", "owner": "b", "id": "later", "after": ["tail"]}}`); text != "created later → b (waits on tail)" {
			t.Fatalf("task_create waits text = %q", text)
		}
		if text := call(`{"name": "finish", "arguments": {"id": "first", "note": "done by a"}}`); !regexp.MustCompile(`^done first; released: tail→b; next: `).MatchString(text) {
			t.Fatalf("finish with released text = %q", text)
		}
		full := call(`{"name": "send", "arguments": {"to": "b", "msg": "full", "json": true}}`)
		var parsed map[string]any
		if err := json.Unmarshal([]byte(full), &parsed); err != nil {
			t.Fatalf("json: true text = %q", full)
		}
		if seq, _ := parsed["seq"].(float64); seq <= 0 {
			t.Fatalf("json: true text = %q, want a seq", full)
		}
	})
}

// TestAuthBoundary walks every protected route through the four verdicts a request can meet:
// no credential, a wrong one, a foreign Host or Origin, and the right one.
func TestAuthBoundary(t *testing.T) {
	srv := startServer(t, nil)
	root := map[string]string{"x-huddle-token": srv.token, "content-type": "application/json"}
	if code, out := srv.op(t, "team", "join", "lead", "{}"); code != 200 {
		t.Fatalf("join = %d %s", code, out)
	}
	routes := []struct {
		method    string
		path      string
		body      string
		write     bool
		rightCode int
	}{
		{method: "GET", path: "/api/channels", rightCode: 200},
		{method: "GET", path: "/api/c/team/board", rightCode: 200},
		{method: "POST", path: "/api/c/team/op/send?as=owner", body: `{"msg": "hi"}`, write: true, rightCode: 200},
		{method: "GET", path: "/api/tokens", rightCode: 200},
		{method: "GET", path: "/api/members", rightCode: 200},
		{method: "GET", path: "/api/settings", rightCode: 200},
		{method: "GET", path: "/api/whoami", rightCode: 200},
		{method: "POST", path: "/api/login", body: "{}", write: true, rightCode: 200},
		{method: "POST", path: "/mcp/team?as=owner", body: `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, write: true, rightCode: 200},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			headers := map[string]string{}
			if route.write {
				headers["content-type"] = "application/json"
			}
			code, _, body, err := srv.request(context.Background(), route.method, route.path, route.body, headers)
			if err != nil {
				t.Fatal(err)
			}
			if code != 401 || !strings.Contains(body, "Signed out") && !strings.Contains(body, "signin") {
				t.Fatalf("no credential = %d %s, want the browser's 401", code, body)
			}
			headers["x-huddle-token"] = "wrong"
			code, _, body, err = srv.request(context.Background(), route.method, route.path, route.body, headers)
			if err != nil {
				t.Fatal(err)
			}
			if code != 401 || !strings.Contains(body, "/huddle:invite") {
				t.Fatalf("a wrong credential = %d %s, want the session's 401", code, body)
			}
			probe := map[string]string{"Host": "evil.example:80", "x-huddle-token": srv.token}
			if route.write {
				probe["content-type"] = "application/json"
			}
			code, _, _, err = srv.request(context.Background(), route.method, route.path, route.body, probe)
			if err != nil {
				t.Fatal(err)
			}
			if code != 421 {
				t.Fatalf("a foreign host = %d, want 421", code)
			}
			if route.write {
				probe["Host"] = ""
				probe["Origin"] = "https://evil.example"
				code, _, _, err = srv.request(context.Background(), route.method, route.path, route.body, probe)
				if err != nil {
					t.Fatal(err)
				}
				if code != 403 {
					t.Fatalf("a foreign origin = %d, want 403", code)
				}
			}
			code, _, body, err = srv.request(context.Background(), route.method, route.path, route.body, root)
			if err != nil || code != route.rightCode {
				t.Fatalf("the root credential = %d %v (%s), want %d", code, err, body, route.rightCode)
			}
		})
	}
	// the open surface: health, the page, the assets
	for _, path := range []string{"/health", "/", "/static/app.css", "/connect.md"} {
		if code, _, _ := srv.get(t, path, nil); code != 200 {
			t.Fatalf("%s = %d, want 200 without a credential", path, code)
		}
	}
}

// TestInvites covers the invite story over the API alone: an invite lets another session join
// under its own name, the credential is bound to it, tokens never reach the channel, and a kick
// or a revoked invite closes the door.
func TestInvites(t *testing.T) {
	srv := startServer(t, nil)
	root := map[string]string{"x-huddle-token": srv.token}
	t.Run("an invite from the creator lets another session join under its own name; kick revokes it; tokens never reach the channel", func(t *testing.T) {
		if code, out := srv.op(t, "team", "join", "lead", "{}"); code != 200 {
			t.Fatalf("owner join = %d %s", code, out)
		}
		code, out := srv.post(t, "/api/tokens", "{}", root)
		if code != 200 {
			t.Fatalf("create invite = %d %s", code, out)
		}
		invite := bodyMap(t, out)
		token, _ := invite["token"].(string)
		id, _ := invite["id"].(string)
		secret := strings.SplitN(token, ".", 2)[1]
		if !regexp.MustCompile(`^[a-z0-9]{6}\.[a-z0-9]{16}$`).MatchString(token) {
			t.Fatalf("invite token = %q", token)
		}
		for _, bad := range []string{id + ".0000000000000000", "zzzzzz.0123456789abcdef", "junk"} {
			if code, _ := srv.post(t, "/api/join", fmt.Sprintf(`{"token": %q, "name": "x"}`, bad), nil); code != 401 {
				t.Fatalf("join with %s = %d, want 401", bad, code)
			}
		}
		// the rule reads with its delimiters, the way a JavaScript RegExp prints itself
		if code, out := srv.post(t, "/api/join", `{"token": "junk", "name": "The Owner"}`, nil); code != 400 ||
			!strings.Contains(out, `name must match /^[a-z][a-z0-9_-]{0,31}(\\.[a-z0-9][a-z0-9_-]{0,31})?$/ (no subagent, not owner)`) {
			t.Fatalf("join with a bad name = %d %s", code, out)
		}
		code, out = srv.post(t, "/api/join", fmt.Sprintf(`{"token": %q, "name": "dev"}`, token), nil)
		if code != 200 {
			t.Fatalf("join = %d %s", code, out)
		}
		joined := bodyMap(t, out)
		cred, _ := joined["credential"].(string)
		if cred == "" || joined["name"] != "dev" {
			t.Fatalf("join answer = %s", out)
		}
		dev := map[string]string{"x-huddle-token": cred, "content-type": "application/json"}
		// its place in the channel: the invite's session joins it under its own name
		if code, out := srv.op(t, "team", "join", "dev", "{}"); code != 200 {
			t.Fatalf("dev's channel join = %d %s", code, out)
		}
		// bound to its name: not another session, not the owner
		for _, as := range []string{"lead", "owner"} {
			path := fmt.Sprintf("/api/c/team/op/status?as=%s", as)
			if code, _, _, _ := srv.request(context.Background(), http.MethodPost, path, "{}", dev); code != 403 {
				t.Fatalf("status as %s = %d, want 403", as, code)
			}
		}
		if code, _, _, _ := srv.request(context.Background(), http.MethodGet, "/api/tokens", "", dev); code != 403 {
			t.Fatalf("the member's invite list = %d, want 403", code)
		}
		// what reached the channel carries no secret
		if code, out := srv.op(t, "team", "send", "dev", fmt.Sprintf(`{"msg": "the join line was %s and my credential %s"}`, token, cred)); code != 200 {
			t.Fatalf("send = %d %s", code, out)
		}
		_, _, timeline := srv.get(t, "/api/c/team/timeline", root)
		if !strings.Contains(timeline, id+".<redacted>") {
			t.Fatalf("the timeline hides the token id: %s", timeline)
		}
		if strings.Contains(timeline, secret) || strings.Contains(timeline, cred) {
			t.Fatal("the timeline carries a secret")
		}
		if _, _, list := srv.get(t, "/api/tokens", root); !strings.Contains(list, id) {
			t.Fatalf("the invite list misses %s: %s", id, list)
		}
		if _, _, members := srv.get(t, "/api/members", root); !strings.Contains(members, "dev") {
			t.Fatalf("the member list misses dev: %s", members)
		}
		request, _ := http.NewRequest(http.MethodDelete, srv.base+"/api/members/dev", nil)
		request.Header.Set("x-huddle-token", srv.token)
		request.Header.Set(ActionHeader, "/api/members/dev")
		response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != 200 || !strings.Contains(string(body), "dev") {
			t.Fatalf("kick = %d %s", response.StatusCode, body)
		}
		if code, _, body, _ := srv.request(context.Background(), http.MethodPost, "/api/c/team/op/send?as=dev", `{"msg": "still here?"}`, dev); code != 401 {
			t.Fatalf("the kicked member = %d %s, want 401", code, body)
		}
		request, _ = http.NewRequest(http.MethodDelete, srv.base+"/api/tokens/"+id, nil)
		request.Header.Set("x-huddle-token", srv.token)
		request.Header.Set(ActionHeader, "/api/tokens/"+id)
		response, err = (&http.Client{Timeout: 10 * time.Second}).Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("revoke = %d", response.StatusCode)
		}
		if code, _ := srv.post(t, "/api/join", fmt.Sprintf(`{"token": %q, "name": "dev"}`, token), nil); code != 401 {
			t.Fatalf("a revoked invite = %d, want 401", code)
		}
	})

	t.Run("a dashboard link signs the browser in once: cookie, then the clean URL", func(t *testing.T) {
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
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != 303 {
			t.Fatalf("the sign-in link = %d %s", response.StatusCode, body)
		}
		if response.Header.Get("Location") != "/" {
			t.Fatalf("location = %q", response.Header.Get("Location"))
		}
		set := response.Header.Get("Set-Cookie")
		for _, want := range []string{"HttpOnly", "SameSite=Strict"} {
			if !strings.Contains(set, want) {
				t.Fatalf("set-cookie %q misses %s", set, want)
			}
		}
		if strings.Contains(set, "Max-Age") {
			t.Fatalf("set-cookie %q names an expiry", set)
		}
		cookie := strings.SplitN(set, ";", 2)[0]
		if !strings.HasPrefix(cookie, fmt.Sprintf("huddle_session_%d=", srv.port)) {
			t.Fatalf("cookie = %q", cookie)
		}
		if code, _, _, _ := srv.request(context.Background(), http.MethodGet, "/api/channels", "", map[string]string{"Cookie": cookie}); code != 200 {
			t.Fatalf("the cookie's channels = %d, want 200", code)
		}
		request, _ = http.NewRequest(http.MethodGet, srv.base+"/?code="+login["code"].(string), nil)
		response, err = client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != 401 { // used
			t.Fatalf("a used code = %d, want 401", response.StatusCode)
		}
		request, _ = http.NewRequest(http.MethodGet, srv.base+"/?code=nope", nil)
		response, err = client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != 401 {
			t.Fatalf("an unknown code = %d, want 401", response.StatusCode)
		}
	})

	t.Run("every member signs its own browser in; that browser runs the dashboard but nothing of the creator's", func(t *testing.T) {
		if code, out := srv.op(t, "team2", "join", "lead", "{}"); code != 200 {
			t.Fatalf("owner join = %d %s", code, out)
		}
		_, out := srv.post(t, "/api/tokens", "{}", root)
		invite := bodyMap(t, out)
		token, _ := invite["token"].(string)
		code, out := srv.post(t, "/api/join", fmt.Sprintf(`{"token": %q, "name": "dev"}`, token), nil)
		if code != 200 {
			t.Fatalf("join = %d %s", code, out)
		}
		cred, _ := bodyMap(t, out)["credential"].(string)
		dev := map[string]string{"x-huddle-token": cred, "content-type": "application/json"}
		if code, out := srv.op(t, "team2", "join", "dev", "{}"); code != 200 {
			t.Fatalf("dev's channel join = %d %s", code, out)
		}
		_, out = srv.post(t, "/api/login", "{}", dev)
		codeValue, _ := bodyMap(t, out)["code"].(string)
		if codeValue == "" {
			t.Fatalf("login = %s", out)
		}
		client := &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Timeout:       10 * time.Second,
		}
		request, _ := http.NewRequest(http.MethodGet, srv.base+"/?code="+codeValue, nil)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != 303 {
			t.Fatalf("the member's browser sign-in = %d", response.StatusCode)
		}
		cookie := strings.SplitN(response.Header.Get("Set-Cookie"), ";", 2)[0]
		C := map[string]string{"Cookie": cookie, "content-type": "application/json"}
		// what the dashboard does: read, and act as the owner in the channels
		if code, _, _, _ := srv.request(context.Background(), http.MethodGet, "/api/channels", "", C); code != 200 {
			t.Fatalf("the browser's channels = %d", code)
		}
		if code, _, _, _ := srv.request(context.Background(), http.MethodGet, "/api/c/team2/board", "", C); code != 200 {
			t.Fatalf("the browser's board = %d", code)
		}
		if code, _, _, _ := srv.request(context.Background(), http.MethodPost, "/api/c/team2/op/send?as=owner", `{"to": "dev", "msg": "from the dashboard"}`, C); code != 200 {
			t.Fatalf("the dashboard's send as owner = %d", code)
		}
		// not a session, and none of the creator's: invites, members, kicks, more browsers
		if code, _, _, _ := srv.request(context.Background(), http.MethodPost, "/api/c/team2/op/send?as=lead", `{"msg": "x"}`, C); code != 403 {
			t.Fatalf("the dashboard's send as lead = %d, want 403", code)
		}
		for _, call := range []struct {
			method string
			path   string
			body   string
		}{
			{"GET", "/api/tokens", ""},
			{"POST", "/api/tokens", "{}"},
			{"GET", "/api/members", ""},
			{"DELETE", "/api/members/dev", ""},
			{"POST", "/api/login", "{}"},
		} {
			if code, _, _, _ := srv.request(context.Background(), call.method, call.path, call.body, C); code != 403 {
				t.Fatalf("%s %s = %d, want 403", call.method, call.path, code)
			}
		}
		_, _, who, err := srv.request(context.Background(), http.MethodGet, "/api/whoami", "", C)
		if err != nil {
			t.Fatal(err)
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(who), &parsed); err != nil {
			t.Fatalf("whoami = %s", who)
		}
		if parsed["name"] != "owner" || parsed["root"] != false || parsed["invite"] != false || parsed["browser"] != "dev" {
			t.Fatalf("whoami = %s", who)
		}
		// the creator's own browser keeps the creator's rights
		_, out = srv.post(t, "/api/login", "{}", root)
		ownerCode, _ := bodyMap(t, out)["code"].(string)
		request, _ = http.NewRequest(http.MethodGet, srv.base+"/?code="+ownerCode, nil)
		response, err = client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		rootCookie := strings.SplitN(response.Header.Get("Set-Cookie"), ";", 2)[0]
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if code, _, _, _ := srv.request(context.Background(), http.MethodGet, "/api/tokens", "", map[string]string{"Cookie": rootCookie}); code != 200 {
			t.Fatalf("the creator's browser = %d, want 200", code)
		}
		// a kick ends the member's browser too
		request, _ = http.NewRequest(http.MethodDelete, srv.base+"/api/members/dev", nil)
		request.Header.Set("x-huddle-token", srv.token)
		request.Header.Set(ActionHeader, "/api/members/dev")
		response, err = client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if code, _, _, _ := srv.request(context.Background(), http.MethodGet, "/api/channels", "", C); code != 401 {
			t.Fatalf("the kicked member's browser = %d, want 401", code)
		}
	})

	t.Run("a browser without a working sign-in gets a Signed out answer; a session gets a way back in", func(t *testing.T) {
		code, _, body, err := srv.request(context.Background(), http.MethodGet, "/api/channels", "",
			map[string]string{"Cookie": fmt.Sprintf("huddle_session_%d=stale", srv.port)})
		if err != nil || code != 401 {
			t.Fatalf("a stale cookie = %d %v", code, err)
		}
		browser := bodyMap(t, body)
		if browser["signin"] != true || !strings.Contains(fmt.Sprint(browser["error"]), "huddle open") {
			t.Fatalf("the browser's 401 = %s", body)
		}
		code, _, body, err = srv.request(context.Background(), http.MethodGet, "/api/channels", "",
			map[string]string{"x-huddle-token": "stale"})
		if err != nil || code != 401 {
			t.Fatalf("a stale session credential = %d %v", code, err)
		}
		session := bodyMap(t, body)
		if _, has := session["signin"]; has {
			t.Fatalf("the session's 401 = %s", body)
		}
		if !strings.Contains(body, "/huddle:invite") {
			t.Fatalf("the session's 401 = %s", body)
		}
		request, _ := http.NewRequest(http.MethodGet, srv.base+"/?code=used-or-old", nil)
		client := &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Timeout:       10 * time.Second,
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		page, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != 401 || !strings.Contains(response.Header.Get("Content-Type"), "text/html") {
			t.Fatalf("the used link = %d %s", response.StatusCode, response.Header.Get("Content-Type"))
		}
		for _, want := range []string{"Signed out", "huddle open", "/huddle:setup"} {
			if !strings.Contains(string(page), want) {
				t.Fatalf("the signed-out page misses %q", want)
			}
		}
		_, _, app := srv.get(t, "/app.js", nil)
		if !strings.Contains(app, "Signed out") {
			t.Fatal("the app bundle misses its Signed-out page")
		}
	})
}

// TestRestart keeps the members: a server restart on the same data keeps every member, browser
// and unused invite (digests only), and a kick still revokes.
func TestRestart(t *testing.T) {
	data := tempDir("huddle-restart-")
	port := freePort(t)
	env := map[string]string{"HUDDLE_DATA": data, "PORT": strconv.Itoa(port)}
	srv := startServer(t, env)
	root := map[string]string{"x-huddle-token": srv.token}

	if code, out := srv.op(t, "reset", "join", "lead", "{}"); code != 200 {
		t.Fatalf("join = %d %s", code, out)
	}
	// a member with a browser
	_, out := srv.post(t, "/api/tokens", "{}", root)
	invite := bodyMap(t, out)
	memberToken, _ := invite["token"].(string)
	code, out := srv.post(t, "/api/join", fmt.Sprintf(`{"token": %q, "name": "dev"}`, memberToken), nil)
	if code != 200 {
		t.Fatalf("join = %d %s", code, out)
	}
	cred, _ := bodyMap(t, out)["credential"].(string)
	if code, out := srv.op(t, "reset", "join", "dev", "{}"); code != 200 {
		t.Fatalf("dev's channel join = %d %s", code, out)
	}
	// an unused invite that lives an hour, and one that dies in a second
	_, spareOut := srv.post(t, "/api/tokens", `{"ttl": 3600}`, root)
	spare, _ := bodyMap(t, spareOut)["token"].(string)
	_, goneOut := srv.post(t, "/api/tokens", `{"ttl": 1}`, root)
	gone, _ := bodyMap(t, goneOut)["token"].(string)
	// the member's browser cookie
	_, loginOut := srv.post(t, "/api/login", "{}", map[string]string{"x-huddle-token": cred, "content-type": "application/json"})
	loginCode, _ := bodyMap(t, loginOut)["code"].(string)
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}
	request, _ := http.NewRequest(http.MethodGet, srv.base+"/?code="+loginCode, nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	cookie := strings.SplitN(response.Header.Get("Set-Cookie"), ";", 2)[0]
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()

	// what the server keeps: digests, 0600, no secret
	authFile := filepath.Join(data, "auth.json")
	info, err := os.Stat(authFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("auth.json mode = %v, want 0600", info.Mode().Perm())
	}
	kept, _ := os.ReadFile(authFile)
	for _, secret := range []string{cred, strings.SplitN(memberToken, ".", 2)[1], strings.SplitN(spare, ".", 2)[1]} {
		if strings.Contains(string(kept), secret) {
			t.Fatal("auth.json carries a secret")
		}
	}

	srv.kill()
	time.Sleep(1100 * time.Millisecond) // the one-second invite runs out

	again := startServer(t, map[string]string{"HUDDLE_DATA": data, "HUDDLE_TOKEN": srv.token, "PORT": strconv.Itoa(port)})
	root["content-type"] = "application/json"
	if code, _, _, _ := again.request(context.Background(), http.MethodPost, "/api/c/reset/op/send?as=lead", `{"msg": "after"}`, root); code != 200 {
		t.Fatalf("the creator after the restart = %d", code)
	}
	if code, _, _, _ := again.request(context.Background(), http.MethodPost, "/api/c/reset/op/send?as=dev", `{"msg": "after"}`, map[string]string{"x-huddle-token": cred, "content-type": "application/json"}); code != 200 {
		t.Fatalf("a member after the restart = %d", code)
	}
	if code, _, _, _ := again.request(context.Background(), http.MethodGet, "/api/channels", "", map[string]string{"Cookie": cookie}); code != 200 {
		t.Fatalf("a browser after the restart = %d", code)
	}
	if code, _ := again.post(t, "/api/join", fmt.Sprintf(`{"token": %q, "name": "late"}`, spare), nil); code != 200 {
		t.Fatalf("an unused invite after the restart = %d", code)
	}
	if code, _ := again.post(t, "/api/join", fmt.Sprintf(`{"token": %q, "name": "later"}`, gone), nil); code != 401 {
		t.Fatalf("an expired invite after the restart = %d, want 401", code)
	}
	// a kick revokes, and stays revoked across another restart
	request, _ = http.NewRequest(http.MethodDelete, again.base+"/api/members/dev", nil)
	request.Header.Set("x-huddle-token", srv.token)
	request.Header.Set(ActionHeader, "/api/members/dev")
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if code, _, _, _ := again.request(context.Background(), http.MethodPost, "/api/c/reset/op/send?as=dev", `{"msg": "still?"}`, map[string]string{"x-huddle-token": cred, "content-type": "application/json"}); code != 401 {
		t.Fatalf("the kicked member = %d, want 401", code)
	}
	again.kill()
	third := startServer(t, map[string]string{"HUDDLE_DATA": data, "HUDDLE_TOKEN": srv.token, "PORT": strconv.Itoa(port)})
	if code, _, _, _ := third.request(context.Background(), http.MethodPost, "/api/c/reset/op/send?as=dev", `{"msg": "after the kick"}`, map[string]string{"x-huddle-token": cred, "content-type": "application/json"}); code != 401 {
		t.Fatalf("the kicked member after a restart = %d, want 401", code)
	}
}

// TestReadRoutes walks every read the channel API offers, and the write that makes a channel:
// each answers 200 with the shape its view names, the misses answer 404 or 400.
func TestReadRoutes(t *testing.T) {
	srv := startServer(t, nil)
	root := map[string]string{"x-huddle-token": srv.token}
	code, out := srv.post(t, "/api/channels", `{"name": "docs", "title": "The docs"}`, root)
	if code != 200 {
		t.Fatalf("create channel = %d %s", code, out)
	}
	if !strings.Contains(out, `"title":"The docs"`) {
		t.Fatalf("create channel = %s", out)
	}
	if code, out := srv.post(t, "/api/channels", `{"name": "docs"}`, root); code != 409 {
		t.Fatalf("a second create = %d %s, want 409", code, out)
	}
	srv.op(t, "docs", "join", "a", `{"role": "writes"}`)
	srv.op(t, "docs", "join", "b", "{}")
	srv.op(t, "docs", "task_create", "a", `{"id": "t1", "title": "Write it", "owner": "b", "what": "the doc"}`)
	srv.op(t, "docs", "note", "a", `{"id": "t1", "kind": "question", "body": "which format?"}`)
	if _, out := srv.op(t, "docs", "remember", "a", `{"kind": "fact", "title": "the fact", "body": "x"}`); true {
		_ = out
	}
	srv.op(t, "docs", "send", "b", `{"to": "owner", "msg": "may I?", "ask": true}`)

	reads := []struct {
		path  string
		wants []string
	}{
		{"/api/c/docs", []string{`"name":"docs"`, `"views":[]`}},
		{"/api/c/docs/board", []string{`"phases":`, `"steps":[`, `"meta":`, `"fts":`}},
		{"/api/c/docs/sessions", []string{`"sessions":[`, `"turn":`, `"config":`}},
		{"/api/c/docs/timeline", []string{`"seq":`}},
		{"/api/c/docs/timeline?after=1&topic=ask", []string{`"topic":"ask"`}},
		{"/api/c/docs/search?q=write", []string{`"id":"t1"`}},
		{"/api/c/docs/review", []string{`"open":[`, "which format?"}},
		{"/api/c/docs/attention", []string{`"asks":[`, "may I?"}},
		{"/api/c/docs/kb?q=fact", []string{`"title":"the fact"`}},
		{"/api/c/docs/conflicts", []string{`"conflicts":`}},
		{"/api/c/docs/task/t1", []string{`"id":"t1"`, `"title":"Write it"`, `"comments":[`}},
		{"/api/c/docs/plan.json", []string{`"meta":`, `"phases":`, `"steps":[`}},
		{"/api/c/docs/x/rules", []string{`"rules":[`}},
	}
	for _, read := range reads {
		code, _, body := srv.get(t, read.path, nil)
		if code != 200 {
			t.Fatalf("%s = %d %s", read.path, code, body)
		}
		for _, want := range read.wants {
			if !strings.Contains(body, want) {
				t.Fatalf("%s misses %s: %s", read.path, want, body)
			}
		}
	}
	// the markdown answers
	code, header, body := srv.get(t, "/api/c/docs/export.md", nil)
	if code != 200 || !strings.Contains(header.Get("Content-Type"), "text/markdown") || !strings.Contains(body, "### t1 Write it") {
		t.Fatalf("export.md = %d %q %s", code, header.Get("Content-Type"), body)
	}
	code, header, body = srv.get(t, "/api/c/docs/knowledge.md", nil)
	if code != 200 || !strings.Contains(header.Get("Content-Type"), "text/markdown") || !strings.Contains(body, "the fact") {
		t.Fatalf("knowledge.md = %d %q %s", code, header.Get("Content-Type"), body)
	}
	// the misses
	if code, _, body := srv.get(t, "/api/c/docs/task/nope", nil); code != 404 || !strings.Contains(body, "no such task") {
		t.Fatalf("a missing task = %d %s", code, body)
	}
	if code, _, _ := srv.get(t, "/api/c/docs/diagram?name=../x.png", nil); code != 400 {
		t.Fatalf("a bad diagram = %d, want 400", code)
	}
	if code, _, _ := srv.get(t, "/static/missing.txt", nil); code != 404 {
		t.Fatalf("a missing static file = %d, want 404", code)
	}
	// the query-shaped reads: a bounded event window, one knowledge hit, the channel root, and a
	// board over an imported plan
	if code, _, out := srv.get(t, "/api/c/docs/timeline?after=0&before=99&limit=1&topic=x", nil); code != 200 {
		t.Fatalf("the bounded events = %d %s", code, out)
	}
	if code, _, out := srv.get(t, "/api/c/docs/kb?limit=1", nil); code != 200 {
		t.Fatalf("the limited kb = %d %s", code, out)
	}
	if code, _, out := srv.get(t, "/api/c/docs", nil); code != 200 || !strings.Contains(out, `"name":"docs"`) {
		t.Fatalf("the channel root = %d %s", code, out)
	}
	if code, out := srv.post(t, "/api/channels", `{}`, root); code != 400 {
		t.Fatalf("a channel without a name = %d %s", code, out)
	}
	if code, out := srv.op(t, "docs", "import_plan", "owner", `{"plan": {"phases": [{"n": 1, "title": "One", "steps": [{"id": "p1", "title": "Step"}]}]}}`); code != 200 {
		t.Fatalf("import_plan = %d %s", code, out)
	}
	if code, _, out := srv.get(t, "/api/c/docs/board", nil); code != 200 || !strings.Contains(out, `"plan_at"`) {
		t.Fatalf("the board with a plan = %d %s", code, out)
	}
	if code, _, body := srv.get(t, "/api/c/docs/nothing", nil); code != 404 || !strings.Contains(body, "not found") {
		t.Fatalf("an unknown view = %d %s", code, body)
	}
	// the knowledge entry's own read
	_, _, kbList := srv.get(t, "/api/c/docs/kb?q=fact", nil)
	var items []struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(kbList), &items); err != nil || len(items) != 1 {
		t.Fatalf("kb = %s (%v)", kbList, err)
	}
	if code, _, body := srv.get(t, fmt.Sprintf("/api/c/docs/kb/%d", items[0].ID), nil); code != 200 || !strings.Contains(body, "the fact") {
		t.Fatalf("kb/<id> = %d %s", code, body)
	}

	t.Run("an invite join that asks for a unique name takes one nothing else holds", func(t *testing.T) {
		_, out := srv.post(t, "/api/tokens", `{"channel": "docs"}`, root)
		invite := bodyMap(t, out)
		token, _ := invite["token"].(string)
		code, out = srv.post(t, "/api/join", fmt.Sprintf(`{"token": %q, "name": "b", "unique": true}`, token), nil)
		if code != 200 {
			t.Fatalf("unique join = %d %s", code, out)
		}
		if name := bodyMap(t, out)["name"]; name != "b-2" {
			t.Fatalf("the unique name = %v, want b-2 (%s)", name, out)
		}
		code, out = srv.post(t, "/api/tokens", `{"ttl": "later"}`, root)
		if code != 400 || !strings.Contains(out, "ttl: seconds") {
			t.Fatalf("a bad ttl = %d %s, want 400", code, out)
		}
		if code, out = srv.post(t, "/api/tokens", `{"ttl": "3600"}`, root); code != 200 {
			t.Fatalf("a numeric ttl as a string = %d %s, want 200", code, out)
		}
		if code, out = srv.post(t, "/api/tokens", `{"ttl": true}`, root); code != 400 || !strings.Contains(out, "ttl: seconds") {
			t.Fatalf("a boolean ttl = %d %s, want 400", code, out)
		}
		if code, out = srv.post(t, "/api/channels", "", root); code != 400 {
			t.Fatalf("an empty body = %d %s, want 400", code, out)
		}
		if code, out = srv.post(t, "/api/tokens", "{not json", root); code != 400 || !strings.Contains(out, "body is not JSON") {
			t.Fatalf("a body that is not JSON = %d %s, want 400", code, out)
		}
	})

	t.Run("the MCP endpoint's own shapes: 405, 204, 202, the batch and the header", func(t *testing.T) {
		if code, _, _ := srv.get(t, "/mcp/docs?as=a", nil); code != 405 {
			t.Fatalf("a GET = %d, want 405", code)
		}
		request, _ := http.NewRequest(http.MethodDelete, srv.base+"/mcp/docs?as=a", nil)
		request.Header.Set("x-huddle-token", srv.token)
		response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != 204 {
			t.Fatalf("a DELETE = %d, want 204", response.StatusCode)
		}
		// only notifications: no response at all
		code, _, _, _ = srv.request(context.Background(), http.MethodPost, "/mcp/docs?as=a",
			`[{"jsonrpc":"2.0","method":"notifications/initialized"}]`,
			map[string]string{"content-type": "application/json", "x-huddle-token": srv.token})
		if code != 202 {
			t.Fatalf("a notification batch = %d, want 202", code)
		}
		// a batch answers an array; the channel may also name the header
		_, out := srv.post(t, "/mcp", `[{"jsonrpc":"2.0","id":7,"method":"ping"},{"jsonrpc":"2.0","id":8,"method":"ping"}]`,
			map[string]string{"x-huddle-channel": "docs", "x-huddle-as": "a"})
		if !strings.HasPrefix(strings.TrimSpace(out), `[{"jsonrpc":"2.0","id":7,"result":{}},{"jsonrpc":"2.0","id":8,"result":{}}]`) {
			t.Fatalf("the batch = %s", out)
		}
		if code, _, _, _ := srv.request(context.Background(), http.MethodPost, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
			map[string]string{"content-type": "application/json", "x-huddle-token": srv.token}); code != 400 {
			t.Fatalf("no channel named = %d, want 400", code)
		}
		// the empty lists, the ping and an unknown method
		for _, probe := range []struct {
			method string
			want   string
		}{
			{"resources/list", "resources"},
			{"prompts/list", "prompts"},
			{"ping", ""},
		} {
			reply := srv.rpc(t, "docs", "a", probe.method, "", 40)
			if probe.want == "" {
				if result, ok := reply["result"].(map[string]any); !ok || len(result) != 0 {
					t.Fatalf("%s = %v", probe.method, reply)
				}
				continue
			}
			if result, ok := reply["result"].(map[string]any); !ok {
				t.Fatalf("%s = %v", probe.method, reply)
			} else if _, has := result[probe.want]; !has {
				t.Fatalf("%s = %v, want a %s list", probe.method, reply, probe.want)
			}
		}
		missing := srv.rpc(t, "docs", "a", "no/such", "", 41)
		errObj, _ := missing["error"].(map[string]any)
		if errObj["code"] != float64(-32601) || !strings.Contains(fmt.Sprint(errObj["message"]), "no/such") {
			t.Fatalf("an unknown method = %v", missing)
		}
		// a cancellation for nothing running is still just a notification
		code, _, _, _ = srv.request(context.Background(), http.MethodPost, "/mcp/docs?as=a",
			`{"jsonrpc":"2.0","method":"notifications/cancelled","params": {"requestId": 99}}`,
			map[string]string{"content-type": "application/json", "x-huddle-token": srv.token})
		if code != 202 {
			t.Fatalf("a stray cancellation = %d, want 202", code)
		}
	})

	t.Run("MCP: a cancellation ends a wait in flight (the requestId names it)", func(t *testing.T) {
		cancelled := make(chan string, 1)
		waitPath := "/mcp/docs?as=b"
		waitBody := `{"jsonrpc":"2.0","id":60,"method":"tools/call","params": {"requestId": "w1", "name": "wait", "arguments": {"topics": ["never.*"], "timeout": 5}}}`
		go func() {
			_, _, out, err := srv.request(context.Background(), http.MethodPost, waitPath, waitBody,
				map[string]string{"content-type": "application/json", "x-huddle-token": srv.token})
			if err != nil {
				cancelled <- "error: " + err.Error()
				return
			}
			cancelled <- out
		}()
		time.Sleep(300 * time.Millisecond)
		if code, _, _ := srv.get(t, "/api/c/docs", nil); code != 200 {
			t.Fatalf("the channel root = %d", code)
		}
		if _, out := srv.post(t, "/mcp/docs?as=b",
			`{"jsonrpc":"2.0","method":"notifications/cancelled","params": {"requestId": "w1"}}`, nil); !strings.Contains(out, "") {
			t.Fatal("the cancellation was not accepted")
		}
		select {
		case out := <-cancelled:
			if !strings.Contains(out, `"id":60`) {
				t.Fatalf("the cancelled wait answered %s", out)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the cancellation never ended the wait")
		}
	})

	t.Run("MCP: a wait that already has its event answers at once, in one line", func(t *testing.T) {
		srv.op(t, "docs", "publish", "a", `{"topic": "go.now", "msg": "the signal"}`)
		reply := srv.rpc(t, "docs", "b", "tools/call", `{"name": "wait", "arguments": {"topics": ["go.*"], "timeout": 5}}`, 30)
		result, _ := reply["result"].(map[string]any)
		content, _ := result["content"].([]any)
		first, _ := content[0].(map[string]any)
		text := fmt.Sprint(first["text"])
		if !strings.Contains(text, "event: #") || !strings.Contains(text, "the signal") || !strings.Contains(text, "ack ") {
			t.Fatalf("the wait line = %q", text)
		}
		{
			// a tool with no one-line form answers its whole JSON, indented
			reply := srv.rpc(t, "docs", "a", "tools/call", `{"name": "sessions"}`, 31)
			result, _ := reply["result"].(map[string]any)
			content, _ := result["content"].([]any)
			first, _ := content[0].(map[string]any)
			if !strings.Contains(fmt.Sprint(first["text"]), "\n  \"state\":") {
				t.Fatalf("the sessions text = %q", first["text"])
			}
		}
	})
}
