package main

// pi routes through huddle-router end to end: `huddle harness install pi` writes huddle's MCP
// server into a temp home's ~/.pi/agent/mcp.json and an anthropic provider into its
// models.json, whose base URL is the router's /gateway. A stub using exactly the written base
// URL and header reaches a fake Anthropic through the gateway, and radar's spool holds the
// request under pi's name. When pi itself is on PATH it runs in RPC mode on that home behind a
// guard proxy that answers nothing, so no request of pi's can leave the machine: pi's own
// state must name the test's gateway as the model's base URL, its MCP status must show huddle
// connected, and the guard must have heard pi without any request for anthropic.com; only then
// does one prompt run, landing on the same fake. Every process is this test's child, every home
// a temp one.

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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// piMCP is the mcp.json the install wrote, as pi reads it.
type piMCP struct {
	Servers map[string]struct {
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
		Timeout int               `json:"timeout"`
	} `json:"mcpServers"`
}

// piModels is the models.json the install wrote, as pi reads it.
type piModels struct {
	Providers map[string]struct {
		BaseURL string            `json:"baseUrl"`
		Headers map[string]string `json:"headers"`
	} `json:"providers"`
}

func TestE2EHarnessPiRoutesThroughHuddleRouter(t *testing.T) {
	root := t.TempDir()
	user := filepath.Join(root, "user")
	agent := filepath.Join(user, ".pi", "agent")
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
		"PI_CODING_AGENT_DIR=",
	)
	env := isoenv.Lookup(entries)

	code, out, errOut := argvRun(t, env, root, "harness", "install", "pi")
	if code != 0 {
		t.Fatalf("harness install pi = %d, stdout %q, stderr %q", code, out, errOut)
	}
	if !strings.Contains(out, "routing through huddle-router in "+filepath.Join(agent, "models.json")) {
		t.Errorf("install said %q; want it to name the routing file", out)
	}

	raw, err := os.ReadFile(filepath.Join(agent, "mcp.json"))
	if err != nil {
		t.Fatalf("the install wrote no pi mcp.json: %v", err)
	}
	var mcp piMCP
	if err := json.Unmarshal(raw, &mcp); err != nil {
		t.Fatalf("mcp.json does not parse: %v\n%s", err, raw)
	}
	server, ok := mcp.Servers["huddle"]
	if !ok || server.Command == "" || strings.Join(server.Args, " ") != "mcp" ||
		server.Env["HUDDLE_HARNESS"] != "pi" || server.Timeout < 60 {
		t.Fatalf("mcp.json holds no huddle stdio server naming pi with a wait-long timeout:\n%s", raw)
	}

	raw, err = os.ReadFile(filepath.Join(agent, "models.json"))
	if err != nil {
		t.Fatalf("the install wrote no pi models.json: %v", err)
	}
	var models piModels
	if err := json.Unmarshal(raw, &models); err != nil {
		t.Fatalf("models.json does not parse: %v\n%s", err, raw)
	}
	provider := models.Providers["anthropic"]
	wantBase := fmt.Sprintf("http://127.0.0.1:%d/gateway", routerPort)
	if provider.BaseURL != wantBase || provider.Headers["X-Huddle-Harness"] != "pi" {
		t.Fatalf("models.json routes anthropic as %+v, want base %s naming pi:\n%s", provider, wantBase, raw)
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
		// pi's Anthropic client posts to baseUrl + "/v1/messages"
		req, err := http.NewRequest(http.MethodPost, provider.BaseURL+"/v1/messages",
			strings.NewReader(`{"model":"`+ompStubModel+`","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Api-Key", "sk-pi-stub")
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
		want := ompSeen{Path: "/v1/messages", Key: "sk-pi-stub", Harness: "pi", Model: ompStubModel}
		seen := upstream.all()
		if len(seen) != 1 || seen[0] != want {
			t.Fatalf("the fake heard %+v, want exactly %+v", seen, want)
		}
	})

	t.Run("radar's spool holds the request under pi's name", func(t *testing.T) {
		until(t, 10*time.Second, func() bool {
			return spoolHolds(radarHome, `"model":"`+ompStubModel+`"`, `"harness":"pi"`)
		}, "a spool line for the stub's model from pi")
	})

	t.Run("the installed pi runs on the written wiring", func(t *testing.T) {
		bin, err := exec.LookPath("pi")
		if err != nil {
			t.Skip("pi is not on PATH: the launch runs only where pi is installed")
		}
		guard := newGuardProxy(t)
		// the guard catches everything that is not loopback; the key is the only thing the
		// process environment adds, the routing is the written models.json
		launchEnv := append(append([]string{}, entries...),
			"ANTHROPIC_API_KEY=sk-pi-launch",
			"HTTPS_PROXY="+guard.url(), "HTTP_PROXY="+guard.url(), "ALL_PROXY="+guard.url(),
			"https_proxy="+guard.url(), "http_proxy="+guard.url(), "all_proxy="+guard.url(),
			"NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost",
			"PI_TELEMETRY=0",
		)
		anthropicAsked := func() []string {
			var hits []string
			for _, line := range guard.seen() {
				if strings.Contains(line, "anthropic.com") {
					hits = append(hits, line)
				}
			}
			return hits
		}

		rpc := startPiRPC(t, bin, root, launchEnv, "--mode", "rpc", "--no-session", "--model", "anthropic/claude-sonnet-4-5")

		// first no prompt: pi's own state must name the gateway as the model's base URL
		rpc.send(t, `{"id":"state","type":"get_state"}`)
		stateLine := rpc.waitLine(30*time.Second, func(line string) bool {
			return strings.Contains(line, `"id":"state"`) && strings.Contains(line, `"type":"response"`)
		})
		var state struct {
			Data struct {
				Model struct {
					Provider string `json:"provider"`
					BaseURL  string `json:"baseUrl"`
				} `json:"model"`
			} `json:"data"`
		}
		if stateLine == "" || json.Unmarshal([]byte(stateLine), &state) != nil {
			t.Fatalf("pi never answered get_state: no prompt runs\n%s", rpc.output())
		}
		if state.Data.Model.Provider != "anthropic" || state.Data.Model.BaseURL != wantBase {
			t.Fatalf("pi's model is %+v, want anthropic on %s: no prompt runs", state.Data.Model, wantBase)
		}

		// pi's /mcp is answered locally, without a model request; the server connects in the
		// background, so ask until it says connected
		connected := ""
		for i := 0; connected == "" && i < 15; i++ {
			rpc.send(t, fmt.Sprintf(`{"id":"mcp-%d","type":"prompt","message":"/mcp"}`, i))
			connected = rpc.waitLine(2*time.Second, func(line string) bool {
				return strings.Contains(line, `huddle: connected`)
			})
		}
		if connected == "" {
			t.Fatalf("pi's MCP status never shows huddle connected: no prompt runs\n%s", rpc.output())
		}

		until(t, 30*time.Second, func() bool { return len(guard.seen()) > 0 },
			"the guard to hear pi's off-machine requests, proving it is in force")
		if hits := anthropicAsked(); len(hits) > 0 {
			t.Fatalf("pi asked for anthropic.com before any prompt (%v): no prompt runs", hits)
		}
		if launched := upstream.all(); len(launched) > 1 {
			t.Fatalf("the fake heard pi before the prompt: %+v", launched)
		}

		rpc.send(t, `{"id":"go","type":"prompt","message":"say hi"}`)
		launched := func() *ompSeen {
			for _, one := range upstream.all() {
				if one.Model != ompStubModel {
					return &one
				}
			}
			return nil
		}
		until(t, 75*time.Second, func() bool { return launched() != nil }, "pi's prompt to reach the fake through the gateway")
		got := launched()
		if got.Path != "/v1/messages" || got.Key != "sk-pi-launch" || got.Harness != "pi" {
			t.Errorf("pi's request = %+v, want /v1/messages with its own key, naming pi", *got)
		}
		if hits := anthropicAsked(); len(hits) > 0 {
			t.Errorf("pi asked for anthropic.com during the prompt: %v", hits)
		}
		t.Logf("pi's MCP status: %s", connected)
		t.Logf("pi's request on the fake: %+v", *got)
		t.Logf("the guard refused, and forwarded nothing of: %v", guard.seen())
	})
}

// piRPC is one pi process in RPC mode this test started, in its own process group: JSON
// commands go in on stdin, every stdout line is kept.
type piRPC struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	mu    sync.Mutex
	lines []string
}

// startPiRPC starts pi; the test's cleanup closes its stdin and kills the group.
func startPiRPC(t *testing.T, bin, dir string, env []string, args ...string) *piRPC {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	run := &piRPC{}
	run.cmd = exec.CommandContext(ctx, bin, args...)
	run.cmd.Env = env
	run.cmd.Dir = dir
	run.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	run.cmd.Cancel = func() error { return syscall.Kill(-run.cmd.Process.Pid, syscall.SIGKILL) }
	run.cmd.Stderr = io.Discard
	stdin, err := run.cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stdout, err := run.cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	run.stdin = stdin
	if err := run.cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start pi %v: %v", args, err)
	}
	read := make(chan struct{})
	go func() {
		defer close(read)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for scanner.Scan() {
			run.mu.Lock()
			run.lines = append(run.lines, scanner.Text())
			run.mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		// a closed stdin ends RPC mode, and pi stops the MCP servers it started on its way out;
		// whatever is left after 10 s goes with the group
		_ = run.stdin.Close()
		select {
		case <-read:
		case <-time.After(10 * time.Second):
		}
		_ = syscall.Kill(-run.cmd.Process.Pid, syscall.SIGKILL)
		<-read
		_ = run.cmd.Wait()
		cancel()
	})
	return run
}

// send writes one command record.
func (r *piRPC) send(t *testing.T, record string) {
	t.Helper()
	if _, err := io.WriteString(r.stdin, record+"\n"); err != nil {
		t.Fatalf("write %s to pi: %v", record, err)
	}
}

// waitLine answers the first stdout line the match accepts, waiting up to within; "" when none
// came.
func (r *piRPC) waitLine(within time.Duration, match func(string) bool) string {
	deadline := time.Now().Add(within)
	for {
		r.mu.Lock()
		for _, line := range r.lines {
			if match(line) {
				r.mu.Unlock()
				return line
			}
		}
		r.mu.Unlock()
		if time.Now().After(deadline) {
			return ""
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// output answers every stdout line so far, enough to read a failure by.
func (r *piRPC) output() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return tailOf(strings.Join(r.lines, "\n"))
}
