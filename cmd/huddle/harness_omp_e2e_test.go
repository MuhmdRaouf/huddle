package main

// omp routes through huddle-router end to end: `huddle harness install omp` writes huddle's
// MCP server into a temp home's ~/.omp/agent/mcp.json and an override of omp's anthropic
// provider into its models.yml, whose base URL is the router's /gateway. A stub using exactly
// the written base URL and header reaches a fake Anthropic through the gateway, and radar's
// spool holds the request under omp's name. When omp itself is on PATH it runs on that home
// behind a guard proxy that answers nothing, so no request of omp's can leave the machine: a
// model refresh first proves the provider never asks for api.anthropic.com, and only then does
// one prompt run, landing on the same fake. Every process is this test's child, every home a
// temp one.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// ompSeen is one request the fake Anthropic heard.
type ompSeen struct {
	Path    string
	Key     string // x-api-key, else the Authorization header
	Harness string
	Model   string
}

// ompUpstream is the fake Anthropic: it keeps every request's path, key, harness header and
// model, answers the stub 200, and answers every other model a 400 so a launched omp stops at
// once.
type ompUpstream struct {
	mu   sync.Mutex
	seen []ompSeen
}

func (u *ompUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(raw, &body)
	u.mu.Lock()
	key := r.Header.Get("X-Api-Key")
	if key == "" {
		key = r.Header.Get("Authorization")
	}
	u.seen = append(u.seen, ompSeen{
		Path: r.URL.Path, Key: key,
		Harness: r.Header.Get("X-Huddle-Harness"), Model: body.Model,
	})
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if body.Model != ompStubModel {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"e2e: the fake upstream stops omp here"}}`))
		return
	}
	_, _ = w.Write([]byte(`{"id":"msg-e2e","type":"message","role":"assistant","content":[],"model":"` + ompStubModel + `","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
}

func (u *ompUpstream) all() []ompSeen {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]ompSeen(nil), u.seen...)
}

// The model the stub asks for, so the fake tells its request from the launched omp's.
const ompStubModel = "claude-e2e-stub"

// ompMCP is the mcp.json the install wrote, as omp reads it.
type ompMCP struct {
	Servers map[string]struct {
		Type    string            `json:"type"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
	} `json:"mcpServers"`
}

// ompModels is the models.yml the install wrote, as omp reads it.
type ompModels struct {
	Providers map[string]struct {
		BaseURL string            `yaml:"baseUrl"`
		Headers map[string]string `yaml:"headers"`
	} `yaml:"providers"`
}

// guardProxy is a proxy that forwards nothing: it notes the first line of every request it is
// handed and closes the connection, so a launched omp's off-machine traffic ends here.
type guardProxy struct {
	mu    sync.Mutex
	lines []string
	ln    net.Listener
}

func newGuardProxy(t *testing.T) *guardProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := &guardProxy{ln: ln}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				line, _ := bufio.NewReader(conn).ReadString('\n')
				g.mu.Lock()
				g.lines = append(g.lines, strings.TrimSpace(line))
				g.mu.Unlock()
			}()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return g
}

func (g *guardProxy) url() string { return "http://" + g.ln.Addr().String() }

func (g *guardProxy) seen() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.lines...)
}

func TestE2EHarnessOmpRoutesThroughHuddleRouter(t *testing.T) {
	root := t.TempDir()
	user := filepath.Join(root, "user")
	agent := filepath.Join(user, ".omp", "agent")
	huddleHome := filepath.Join(root, "huddle-home")
	radarHome := filepath.Join(root, "radar-home")
	routerHome := filepath.Join(root, "router-home")
	routerPort := ports.Free(t)
	for _, dir := range []string{user, huddleHome, radarHome, routerHome} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	upstream := &ompUpstream{}
	fake := httptest.NewServer(upstream)
	t.Cleanup(fake.Close)

	entries := isoenv.Environ(t, user,
		"HUDDLE_HOME="+huddleHome,
		"RADAR_HOME="+radarHome,
		"HUDDLE_ROUTER_HOME="+routerHome,
		"HUDDLE_ROUTER_PORT="+strconv.Itoa(routerPort),
		"HUDDLE_NO_PROJECT_CRED=1",
		"HUDDLE_AUTOSTART=0",
		"RADAR_AUTOSTART=0",
		"ZAI_ROUTER_URL="+fake.URL,
		"ZAI_API_KEY=e2e-lane-key",
		"HUDDLE_ROUTER_ANTHROPIC_URL="+fake.URL,
		"ANTHROPIC_API_KEY=",
	)
	env := isoenv.Lookup(entries)

	code, out, errOut := argvRun(t, env, root, "harness", "install", "omp")
	if code != 0 {
		t.Fatalf("harness install omp = %d, stdout %q, stderr %q", code, out, errOut)
	}
	if !strings.Contains(out, filepath.Join(agent, "models.yml")) {
		t.Errorf("install said %q; want it to name the routing file", out)
	}

	raw, err := os.ReadFile(filepath.Join(agent, "mcp.json"))
	if err != nil {
		t.Fatalf("the install wrote no omp mcp.json: %v", err)
	}
	var mcp ompMCP
	if err := json.Unmarshal(raw, &mcp); err != nil {
		t.Fatalf("mcp.json does not parse: %v\n%s", err, raw)
	}
	server, ok := mcp.Servers["huddle"]
	if !ok || server.Type != "stdio" || server.Command == "" || strings.Join(server.Args, " ") != "mcp" ||
		server.Env["HUDDLE_HARNESS"] != "omp" {
		t.Fatalf("mcp.json holds no huddle stdio server naming omp:\n%s", raw)
	}

	raw, err = os.ReadFile(filepath.Join(agent, "models.yml"))
	if err != nil {
		t.Fatalf("the install wrote no omp models.yml: %v", err)
	}
	var models ompModels
	if err := yaml.Unmarshal(raw, &models); err != nil {
		t.Fatalf("models.yml does not parse: %v\n%s", err, raw)
	}
	provider := models.Providers["anthropic"]
	wantBase := fmt.Sprintf("http://127.0.0.1:%d/gateway", routerPort)
	if provider.BaseURL != wantBase || provider.Headers["X-Huddle-Harness"] != "omp" {
		t.Fatalf("models.yml routes anthropic as %+v, want base %s naming omp:\n%s", provider, wantBase, raw)
	}

	// the gateway's anthropic default points at the fake; without the file it is api.anthropic.com
	if err := os.WriteFile(filepath.Join(huddleHome, "gateway.json"),
		[]byte(fmt.Sprintf(`{"anthropic":%q}`, fake.URL)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(routerHome, "providers.json"), []byte(`{"providers":["zai"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	serve := exec.Command(huddleBin, "router", "serve")
	serve.Env = entries
	serve.Dir = root
	if err := serve.Start(); err != nil {
		t.Fatalf("start huddle router serve: %v", err)
	}
	t.Cleanup(func() { stopChild(serve) })
	routerHealth := fmt.Sprintf("http://127.0.0.1:%d/huddle-router", routerPort)
	until(t, 15*time.Second, func() bool { return healthy(routerHealth) }, "the router to answer its health path")

	t.Run("a stub on the written base URL reaches the fake with its own key", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, provider.BaseURL+"/v1/messages",
			strings.NewReader(`{"model":"`+ompStubModel+`","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Api-Key", "sk-omp-stub")
		req.Header.Set("Anthropic-Version", "2023-06-01")
		for key, value := range provider.Headers {
			req.Header.Set(key, value)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", req.URL, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("the gateway answered %d, want the fake's 200", resp.StatusCode)
		}
		want := ompSeen{Path: "/v1/messages", Key: "sk-omp-stub", Harness: "omp", Model: ompStubModel}
		seen := upstream.all()
		if len(seen) != 1 || seen[0] != want {
			t.Fatalf("the fake heard %+v, want exactly %+v", seen, want)
		}
	})

	t.Run("radar's spool holds the request under omp's name", func(t *testing.T) {
		until(t, 10*time.Second, func() bool {
			return spoolHolds(radarHome, `"model":"`+ompStubModel+`"`, `"harness":"omp"`)
		}, "a spool line for the stub's model from omp")
	})

	t.Run("the installed omp runs on the written wiring", func(t *testing.T) {
		bin, err := exec.LookPath("omp")
		if err != nil {
			t.Skip("omp is not on PATH: the launch runs only where omp is installed")
		}
		guard := newGuardProxy(t)
		// the guard catches everything that is not loopback; the key is the only thing the
		// process environment adds, the routing is the written models.yml
		launchEnv := append(append([]string{}, entries...),
			"ANTHROPIC_API_KEY=sk-omp-launch",
			"HTTPS_PROXY="+guard.url(), "HTTP_PROXY="+guard.url(), "ALL_PROXY="+guard.url(),
			"https_proxy="+guard.url(), "http_proxy="+guard.url(), "all_proxy="+guard.url(),
			"NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost",
		)
		anthropicAsked := func() []string {
			var hits []string
			for _, line := range guard.seen() {
				if strings.Contains(line, "api.anthropic.com") {
					hits = append(hits, line)
				}
			}
			return hits
		}

		// first no prompt: a model refresh asks every provider for its models, anthropic's
		// with the key, so a provider still on api.anthropic.com would show at the guard
		refresh := ompCommand(t, bin, root, launchEnv, "models", "refresh")
		if err := refresh.run(60 * time.Second); err != nil {
			t.Fatalf("omp models refresh: %v\n%s", err, refresh.output.String())
		}
		if len(guard.seen()) == 0 {
			t.Fatalf("the guard heard nothing during the refresh: it is not in force, so no prompt runs\n%s", refresh.output.String())
		}
		if hits := anthropicAsked(); len(hits) > 0 {
			t.Fatalf("omp asked for api.anthropic.com despite models.yml (%v): no prompt runs", hits)
		}
		if log := ompLogs(user); strings.Contains(log, `"provider":"anthropic","source":"none"`) {
			t.Fatalf("omp's log resolves the anthropic provider with no route: no prompt runs\n%s", tailOf(log))
		}

		prompt := ompCommand(t, bin, root, launchEnv,
			"-p", "--no-session", "--no-title", "--model", "anthropic/claude-sonnet-4-5", "say hi")
		launched := func() *ompSeen {
			for _, one := range upstream.all() {
				if one.Model != ompStubModel {
					return &one
				}
			}
			return nil
		}
		prompt.until(75*time.Second, func() bool { return launched() != nil })
		got := launched()
		if got == nil {
			t.Fatalf("omp never reached the fake through the gateway; its output:\n%s\nits log:\n%s",
				prompt.output.String(), tailOf(ompLogs(user)))
		}
		// omp sends its key as a bearer token to a base URL it does not know as Anthropic's; the
		// gateway forwards it unchanged either way
		if got.Path != "/v1/messages" || (got.Key != "sk-omp-launch" && got.Key != "Bearer sk-omp-launch") || got.Harness != "omp" {
			t.Errorf("omp's request = %+v, want /v1/messages with its own key, naming omp", *got)
		}
		if hits := anthropicAsked(); len(hits) > 0 {
			t.Errorf("omp asked for api.anthropic.com during the prompt: %v", hits)
		}
		if log := ompLogs(user); !strings.Contains(log, "mcp:huddle") {
			t.Errorf("omp's log never names mcp:huddle: the MCP server did not start\n%s", tailOf(log))
		}
	})
}

// ompRun is one omp process this test started, in its own process group.
type ompRun struct {
	cmd    *exec.Cmd
	output bytes.Buffer
	cancel context.CancelFunc
}

// ompCommand starts omp with the given environment; the test's cleanup kills the group.
func ompCommand(t *testing.T, bin, dir string, env []string, args ...string) *ompRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	run := &ompRun{cancel: cancel}
	run.cmd = exec.CommandContext(ctx, bin, args...)
	run.cmd.Env = env
	run.cmd.Dir = dir
	run.cmd.Stdin = bytes.NewReader(nil)
	run.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	run.cmd.Cancel = func() error { return syscall.Kill(-run.cmd.Process.Pid, syscall.SIGKILL) }
	run.cmd.Stdout, run.cmd.Stderr = &run.output, &run.output
	if err := run.cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start omp %v: %v", args, err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-run.cmd.Process.Pid, syscall.SIGKILL)
		cancel()
	})
	return run
}

// run waits for omp to exit within the bound, then kills what is left of its group.
func (r *ompRun) run(within time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- r.cmd.Wait() }()
	select {
	case err := <-done:
		_ = syscall.Kill(-r.cmd.Process.Pid, syscall.SIGKILL)
		return err
	case <-time.After(within):
		_ = syscall.Kill(-r.cmd.Process.Pid, syscall.SIGKILL)
		<-done
		return fmt.Errorf("no exit within %s", within)
	}
}

// until waits for the condition or omp's exit, whichever comes first, then kills the group.
func (r *ompRun) until(within time.Duration, cond func() bool) {
	done := make(chan error, 1)
	go func() { done <- r.cmd.Wait() }()
	deadline := time.After(within)
wait:
	for !cond() {
		select {
		case <-done:
			_ = syscall.Kill(-r.cmd.Process.Pid, syscall.SIGKILL)
			return
		case <-deadline:
			break wait
		case <-time.After(100 * time.Millisecond):
		}
	}
	_ = syscall.Kill(-r.cmd.Process.Pid, syscall.SIGKILL)
	<-done
}

// ompLogs answers every log omp wrote under the temp home.
func ompLogs(user string) string {
	logs, _ := filepath.Glob(filepath.Join(user, ".omp", "logs", "*.log"))
	var all strings.Builder
	for _, log := range logs {
		raw, _ := os.ReadFile(log)
		all.Write(raw)
	}
	return all.String()
}

// tailOf answers the last 6000 bytes of a log, enough to read a failure by.
func tailOf(log string) string { return log[max(0, len(log)-6000):] }
