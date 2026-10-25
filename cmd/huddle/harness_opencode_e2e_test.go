package main

// opencode routes through huddle-router end to end: `huddle harness install opencode` writes
// huddle's MCP server and an anthropic provider into a temp home's opencode.json, the
// provider's base URL the router's /gateway. A stub using exactly the written base URL and
// header reaches a fake Anthropic through the gateway, and radar's spool holds the request
// under opencode's name. When opencode itself is on PATH it runs on that home behind a guard
// proxy that answers nothing, so no request of opencode's can leave the machine: its resolved
// config and its MCP list come first, and only once they show the gateway route and a
// connected huddle server does one prompt run, landing on the same fake. Every process is this
// test's child, every home a temp one.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// openCodeConfig is the opencode.json the install wrote, as opencode reads it.
type openCodeConfig struct {
	MCP map[string]struct {
		Type        string            `json:"type"`
		Command     []string          `json:"command"`
		Environment map[string]string `json:"environment"`
	} `json:"mcp"`
	Provider map[string]struct {
		Options struct {
			BaseURL string            `json:"baseURL"`
			Headers map[string]string `json:"headers"`
		} `json:"options"`
	} `json:"provider"`
}

func TestE2EHarnessOpencodeRoutesThroughHuddleRouter(t *testing.T) {
	root := t.TempDir()
	user := filepath.Join(root, "user")
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
	configPath := filepath.Join(env["XDG_CONFIG_HOME"], "opencode", "opencode.json")

	code, out, errOut := argvRun(t, env, root, "harness", "install", "opencode")
	if code != 0 {
		t.Fatalf("harness install opencode = %d, stdout %q, stderr %q", code, out, errOut)
	}
	if !strings.Contains(out, "routing through huddle-router in "+configPath) {
		t.Errorf("install said %q; want it to name the routing in %s", out, configPath)
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("the install wrote no opencode.json: %v", err)
	}
	var config openCodeConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatalf("opencode.json does not parse: %v\n%s", err, raw)
	}
	server, ok := config.MCP["huddle"]
	if !ok || server.Type != "local" || len(server.Command) != 2 || server.Command[1] != "mcp" ||
		server.Environment["HUDDLE_HARNESS"] != "opencode" {
		t.Fatalf("opencode.json holds no huddle local server naming opencode:\n%s", raw)
	}
	provider := config.Provider["anthropic"].Options
	wantBase := fmt.Sprintf("http://127.0.0.1:%d/gateway/v1", routerPort)
	if provider.BaseURL != wantBase || provider.Headers["X-Huddle-Harness"] != "opencode" {
		t.Fatalf("opencode.json routes anthropic as %+v, want base %s naming opencode:\n%s", provider, wantBase, raw)
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
		// the AI SDK's Anthropic provider posts to baseURL + "/messages"
		req, err := http.NewRequest(http.MethodPost, provider.BaseURL+"/messages",
			strings.NewReader(`{"model":"`+ompStubModel+`","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Api-Key", "sk-opencode-stub")
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
		want := ompSeen{Path: "/v1/messages", Key: "sk-opencode-stub", Harness: "opencode", Model: ompStubModel}
		seen := upstream.all()
		if len(seen) != 1 || seen[0] != want {
			t.Fatalf("the fake heard %+v, want exactly %+v", seen, want)
		}
	})

	t.Run("radar's spool holds the request under opencode's name", func(t *testing.T) {
		until(t, 10*time.Second, func() bool {
			return spoolHolds(radarHome, `"model":"`+ompStubModel+`"`, `"harness":"opencode"`)
		}, "a spool line for the stub's model from opencode")
	})

	t.Run("the installed opencode runs on the written wiring", func(t *testing.T) {
		bin, err := exec.LookPath("opencode")
		if err != nil {
			t.Skip("opencode is not on PATH: the launch runs only where opencode is installed")
		}
		guard := newGuardProxy(t)
		// the guard catches everything that is not loopback; the key is the only thing the
		// process environment adds, the routing is the written opencode.json
		launchEnv := append(append([]string{}, entries...),
			"ANTHROPIC_API_KEY=sk-opencode-launch",
			"HTTPS_PROXY="+guard.url(), "HTTP_PROXY="+guard.url(), "ALL_PROXY="+guard.url(),
			"https_proxy="+guard.url(), "http_proxy="+guard.url(), "all_proxy="+guard.url(),
			"NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost",
			"OPENCODE_DISABLE_AUTOUPDATE=1",
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

		// --pure leaves out the config's plugins: a plugin makes opencode install its plugin
		// package from npm first, which behind the guard never finishes. This leg proves the MCP
		// server and the route, not the hooks.
		// first no prompt: the resolved config must route anthropic to the gateway, and the
		// MCP list must show huddle connected
		resolved := ompCommand(t, bin, root, launchEnv, "--pure", "debug", "config")
		if err := resolved.run(60 * time.Second); err != nil {
			t.Fatalf("opencode debug config: %v\n%s\nguard: %v", err, resolved.output.String(), guard.seen())
		}
		if !strings.Contains(resolved.output.String(), wantBase) {
			t.Fatalf("opencode's resolved config never names %s: no prompt runs\n%s", wantBase, resolved.output.String())
		}
		mcp := ompCommand(t, bin, root, launchEnv, "--pure", "mcp", "list")
		if err := mcp.run(60 * time.Second); err != nil {
			t.Fatalf("opencode mcp list: %v\n%s\nguard: %v", err, mcp.output.String(), guard.seen())
		}
		if listed := mcp.output.String(); !strings.Contains(listed, "✓ huddle") {
			t.Fatalf("opencode's MCP list shows no connected huddle server: no prompt runs\n%s", listed)
		}
		if len(guard.seen()) == 0 {
			t.Fatalf("the guard heard nothing before the prompt: it is not in force, so no prompt runs")
		}
		if hits := anthropicAsked(); len(hits) > 0 {
			t.Fatalf("opencode asked for anthropic.com before any prompt (%v): no prompt runs", hits)
		}

		prompt := ompCommand(t, bin, root, launchEnv,
			"--pure", "run", "--model", "anthropic/claude-sonnet-4-5", "say hi")
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
			t.Fatalf("opencode never reached the fake through the gateway; its output:\n%s\nthe guard heard: %v",
				prompt.output.String(), guard.seen())
		}
		if got.Path != "/v1/messages" || got.Key != "sk-opencode-launch" || got.Harness != "opencode" {
			t.Errorf("opencode's request = %+v, want /v1/messages with its own key, naming opencode", *got)
		}
		if hits := anthropicAsked(); len(hits) > 0 {
			t.Errorf("opencode asked for anthropic.com during the prompt: %v", hits)
		}
		t.Logf("the guard refused, and forwarded nothing of: %v", guard.seen())
	})
}
