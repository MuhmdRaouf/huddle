package main

// zcode routes through huddle-router end to end: `huddle harness install zcode` writes huddle's
// MCP server into a temp home's ~/.zcode/cli/config.json and an override of the two Z.ai coding
// plan providers into its ~/.zcode/v2/provider_config.json, whose base URL is the router's
// /gateway. A stub using exactly the written base URL and header reaches a fake Z.ai through
// the gateway, and radar's spool holds the request under zcode's name. When zcode is on PATH it
// runs on that home with no login: zcode's own network.httpProxy names a guard proxy that
// forwards nothing, and a personal provider with a made-up key on the written base URL answers
// one prompt. zcode prints no base URL before a model call, so the proof comes after the
// prompt: the fake heard /v1/messages naming zcode, and the guard heard no host off this
// machine. Every process is this test's child, every home a temp one.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	zcodeengine "github.com/MuhmdRaouf/huddle/internal/core/adapters/engines/zcode"
	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// zcodeMCP is the config.json the install wrote, as zcode reads it.
type zcodeMCP struct {
	MCP struct {
		Servers map[string]struct {
			Type      string            `json:"type"`
			Command   string            `json:"command"`
			Args      []string          `json:"args"`
			Env       map[string]string `json:"env"`
			TimeoutMs int64             `json:"timeoutMs"`
		} `json:"servers"`
	} `json:"mcp"`
}

// zcodeRule is one provider rule of provider_config.json.
type zcodeRule struct {
	ProviderID string `json:"providerId"`
	Config     struct {
		API struct {
			BaseURL string            `json:"baseUrl"`
			Headers map[string]string `json:"headers"`
		} `json:"api"`
	} `json:"config"`
}

// zcodeProviders is the provider_config.json the install wrote, as zcode reads it.
type zcodeProviders struct {
	Config struct {
		ProviderConfigRules struct {
			ProviderRules []zcodeRule `json:"providerRules"`
		} `json:"providerConfigRules"`
	} `json:"config"`
}

// The model zcode's coding plans speak, the one the gateway's GLM route names.
const zcodeModel = "GLM-5.3"

func TestE2EHarnessZcodeRoutesThroughHuddleRouter(t *testing.T) {
	root := t.TempDir()
	user := filepath.Join(root, "user")
	huddleHome := filepath.Join(root, "huddle-home")
	radarHome := filepath.Join(root, "radar-home")
	routerHome := filepath.Join(root, "router-home")
	work := filepath.Join(root, "work")
	routerPort := ports.Free(t)
	for _, dir := range []string{user, huddleHome, radarHome, routerHome, work} {
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
	configPath := filepath.Join(user, ".zcode", "cli", "config.json")
	providersPath := filepath.Join(user, ".zcode", "v2", "provider_config.json")

	code, out, errOut := argvRun(t, env, root, "harness", "install", "zcode")
	if code != 0 {
		t.Fatalf("harness install zcode = %d, stdout %q, stderr %q", code, out, errOut)
	}
	if !strings.Contains(out, "routing through huddle-router in "+providersPath) {
		t.Errorf("install said %q; want it to name the routing in %s", out, providersPath)
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("the install wrote no zcode config.json: %v", err)
	}
	var mcp zcodeMCP
	if err := json.Unmarshal(raw, &mcp); err != nil {
		t.Fatalf("config.json does not parse: %v\n%s", err, raw)
	}
	server, ok := mcp.MCP.Servers["huddle"]
	if !ok || server.Type != "stdio" || server.Command == "" || strings.Join(server.Args, " ") != "mcp" ||
		server.Env["HUDDLE_HARNESS"] != "zcode" || server.TimeoutMs < 60000 {
		t.Fatalf("config.json holds no huddle stdio server naming zcode:\n%s", raw)
	}

	raw, err = os.ReadFile(providersPath)
	if err != nil {
		t.Fatalf("the install wrote no zcode provider_config.json: %v", err)
	}
	var providers zcodeProviders
	if err := json.Unmarshal(raw, &providers); err != nil {
		t.Fatalf("provider_config.json does not parse: %v\n%s", err, raw)
	}
	wantBase := fmt.Sprintf("http://127.0.0.1:%d/gateway", routerPort)
	rules := providers.Config.ProviderConfigRules.ProviderRules
	var plans []string
	for _, rule := range rules {
		if rule.Config.API.BaseURL != wantBase || rule.Config.API.Headers["X-Huddle-Harness"] != "zcode" {
			t.Fatalf("provider_config.json routes %s as %+v, want base %s naming zcode:\n%s",
				rule.ProviderID, rule.Config.API, wantBase, raw)
		}
		plans = append(plans, rule.ProviderID)
	}
	if strings.Join(plans, " ") != "account:zai-individual-coding-plan account:zai-team-coding-plan" {
		t.Fatalf("provider_config.json overrides %v, want the two Z.ai coding plans:\n%s", plans, raw)
	}

	// the gateway's GLM route points at the fake; without the file it is Z.ai's Anthropic route
	if err := os.WriteFile(filepath.Join(huddleHome, "gateway.json"),
		[]byte(fmt.Sprintf(`{"routes":[{"match":"GLM-*","upstream":%q}]}`, fake.URL)), 0o600); err != nil {
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

	base, headers := rules[0].Config.API.BaseURL, rules[0].Config.API.Headers
	t.Run("a stub on the written base URL reaches the fake with its own key", func(t *testing.T) {
		// zcode's Anthropic provider posts to baseUrl + "/v1/messages"
		req, err := http.NewRequest(http.MethodPost, base+"/v1/messages",
			strings.NewReader(`{"model":"`+zcodeModel+`","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Api-Key", "sk-zcode-stub")
		req.Header.Set("Anthropic-Version", "2023-06-01")
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", req.URL, err)
		}
		_ = resp.Body.Close()
		// the fake answers every model but omp's stub a 400 of its own; the gateway's own
		// failures are 404 and 502
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("the gateway answered %d, want the fake's 400", resp.StatusCode)
		}
		want := ompSeen{Path: "/v1/messages", Key: "sk-zcode-stub", Harness: "zcode", Model: zcodeModel}
		seen := upstream.all()
		if len(seen) != 1 || seen[0] != want {
			t.Fatalf("the fake heard %+v, want exactly %+v", seen, want)
		}
	})

	t.Run("radar's spool holds the request under zcode's name", func(t *testing.T) {
		until(t, 10*time.Second, func() bool {
			return spoolHolds(radarHome, `"model":"`+zcodeModel+`"`, `"harness":"zcode"`)
		}, "a spool line for the stub's model from zcode")
	})

	t.Run("the installed zcode runs on the written wiring", func(t *testing.T) {
		found, err := exec.LookPath("zcode")
		if err != nil {
			t.Skip("zcode is not on PATH: the launch runs only where zcode is installed")
		}
		script := found
		if cli := zcodeengine.CLIScriptFor(found); cli != "" {
			script = cli
		}
		bin, args := zcodeengine.ZcodeCommand(script)
		guard := newGuardProxy(t)

		// zcode's own proxy setting sends every model call that is not loopback to the guard;
		// the standard variables cover its web tools. NODE_USE_ENV_PROXY stays unset: with it,
		// zcode tunnels even its loopback calls through the proxy.
		var cfg map[string]any
		if err := json.Unmarshal(zcodeFile(t, configPath), &cfg); err != nil {
			t.Fatal(err)
		}
		cfg["network"] = map[string]any{"httpProxy": guard.url(), "noProxy": "127.0.0.1,localhost"}
		zcodeWriteJSON(t, configPath, cfg)

		// no login in this home: a personal provider with a made-up key on the written base URL
		// and header answers the prompt, beside the two plan overrides install wrote
		var doc map[string]any
		if err := json.Unmarshal(zcodeFile(t, providersPath), &doc); err != nil {
			t.Fatal(err)
		}
		config := doc["config"].(map[string]any)
		ruleSet := config["providerConfigRules"].(map[string]any)
		ruleSet["providerRules"] = append(ruleSet["providerRules"].([]any), map[string]any{
			"providerId": "e2e-probe", "providerName": "e2e-probe", "enabled": true,
			"config": map[string]any{
				"group":            "standard-personal",
				"access":           map[string]any{"type": "api-key", "apiKey": "sk-zcode-launch"},
				"api":              map[string]any{"type": "anthropic-messages", "baseUrl": base, "headers": headers},
				"personalModelIds": []string{zcodeModel},
			},
		})
		config["defaultModelSelection"] = map[string]any{"providerId": "e2e-probe", "modelId": zcodeModel}
		zcodeWriteJSON(t, providersPath, doc)

		launchEnv := append(append([]string{}, entries...),
			"ELECTRON_RUN_AS_NODE=1",
			"HTTPS_PROXY="+guard.url(), "HTTP_PROXY="+guard.url(), "ALL_PROXY="+guard.url(),
			"https_proxy="+guard.url(), "http_proxy="+guard.url(), "all_proxy="+guard.url(),
			"NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost",
			// zcode's own service endpoints point at the guard too, and telemetry and updates stay off
			"ZCODE_ENDPOINT_ORIGIN="+guard.url(), "ZCODE_TELEMETRY_REPORT_ENDPOINT="+guard.url(),
			"ZCODE_MODEL_TELEMETRY_ENABLED=0", "ZCODE_AUTO_UPDATE_ENABLED=0",
		)
		// the CLI names its built-in and personal provider files together or not at all
		if builtin := zcodeengine.BuiltinProviderConfig(script); builtin != "" {
			launchEnv = append(launchEnv,
				"ZCODE_BUILTIN_PROVIDER_CONFIG_FILE="+builtin,
				"ZCODE_PERSONAL_PROVIDER_CONFIG_FILE="+providersPath)
		}

		prompt := ompCommand(t, bin, work, launchEnv, append(args, "-p", "say hi")...)
		launched := func() *ompSeen {
			for _, one := range upstream.all() {
				if one.Key != "sk-zcode-stub" {
					return &one
				}
			}
			return nil
		}
		prompt.until(75*time.Second, func() bool { return launched() != nil })
		got := launched()
		if got == nil {
			t.Fatalf("zcode never reached the fake through the gateway; its output:\n%s\nthe guard heard: %v",
				prompt.output.String(), guard.seen())
		}
		if got.Path != "/v1/messages" || got.Key != "sk-zcode-launch" || got.Harness != "zcode" || got.Model != zcodeModel {
			t.Errorf("zcode's request = %+v, want /v1/messages for %s with its own key, naming zcode", *got, zcodeModel)
		}
		for _, line := range guard.seen() {
			if host := requestHost(line); host != "" && host != "127.0.0.1" && host != "localhost" {
				t.Errorf("zcode asked the guard for %s, a host off this machine: %q", host, line)
			}
		}
		if log := zcodeLogs(user); !zcodeConnected(log, "huddle") {
			t.Errorf("zcode's log never shows the huddle MCP server connected\n%s", tailOf(log))
		}
		t.Logf("the guard refused, and forwarded nothing of: %v", guard.seen())
	})
}

// zcodeFile reads one file the test owns.
func zcodeFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// zcodeWriteJSON writes one config the test edits.
func zcodeWriteJSON(t *testing.T, path string, doc any) {
	t.Helper()
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// requestHost answers the host one proxy request line names: CONNECT's authority, or the
// absolute URL's host; "" when the line names none.
func requestHost(line string) string {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return ""
	}
	target := fields[1]
	if fields[0] == http.MethodConnect {
		target = "//" + target
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return target
	}
	return parsed.Hostname()
}

// zcodeLogs answers every log zcode wrote under the temp home.
func zcodeLogs(user string) string {
	logs, _ := filepath.Glob(filepath.Join(user, ".zcode", "cli", "log", "*.jsonl"))
	var all strings.Builder
	for _, log := range logs {
		raw, _ := os.ReadFile(log)
		all.Write(raw)
	}
	return all.String()
}

// zcodeConnected answers whether a zcode log line records the named MCP server connected.
func zcodeConnected(log, name string) bool {
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, `"event":"mcp.server.connected"`) && strings.Contains(line, `"mcpServerName":"`+name+`"`) {
			return true
		}
	}
	return false
}
