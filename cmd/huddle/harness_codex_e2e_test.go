package main

// codex routes through huddle-router end to end: `huddle harness install codex` writes the
// opt-in huddle model provider into a temp CODEX_HOME, a stub speaking the Responses API with
// exactly what that provider names reaches a fake upstream through the router's /gateway, with
// its own Authorization header unchanged, and Radar's spool holds the request under codex's
// name. When codex itself is on PATH, it runs once on that provider and its request lands on the
// same fake. Every process is this test's child, every home a temp one.

import (
	"bytes"
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

	"github.com/BurntSushi/toml"

	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// codexSeen is one request the fake upstream heard.
type codexSeen struct {
	Path    string
	Auth    string
	Harness string
	Model   string
}

// codexUpstream is the fake OpenAI: it keeps every request's path, auth, harness header and
// model, answers the stub 200, and answers codex's own model a 400 so codex stops at once.
type codexUpstream struct {
	mu   sync.Mutex
	seen []codexSeen
}

func (u *codexUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(raw, &body)
	u.mu.Lock()
	u.seen = append(u.seen, codexSeen{
		Path: r.URL.Path, Auth: r.Header.Get("Authorization"),
		Harness: r.Header.Get("X-Huddle-Harness"), Model: body.Model,
	})
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if body.Model == codexLaunchModel {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"e2e: the fake upstream stops codex here","type":"invalid_request_error"}}`))
		return
	}
	_, _ = w.Write([]byte(`{"id":"resp-e2e","object":"response","status":"completed","output":[]}`))
}

func (u *codexUpstream) all() []codexSeen {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]codexSeen(nil), u.seen...)
}

// The model the launched codex asks for, so the fake tells its request from the stub's.
const codexLaunchModel = "gpt-e2e-codex"

// codexProvider is the provider table the install wrote, as codex reads it.
type codexProvider struct {
	BaseURL            string            `toml:"base_url"`
	WireAPI            string            `toml:"wire_api"`
	EnvKey             string            `toml:"env_key"`
	SupportsWebsockets *bool             `toml:"supports_websockets"`
	HTTPHeaders        map[string]string `toml:"http_headers"`
}

func TestE2EHarnessCodexRoutesThroughHuddleRouter(t *testing.T) {
	root := t.TempDir()
	user := filepath.Join(root, "user")
	codexHome := filepath.Join(user, ".codex")
	huddleHome := filepath.Join(root, "huddle-home")
	radarHome := filepath.Join(root, "radar-home")
	routerHome := filepath.Join(root, "router-home")
	routerPort := ports.Free(t)
	for _, dir := range []string{user, huddleHome, radarHome, routerHome} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	upstream := &codexUpstream{}
	fake := httptest.NewServer(upstream)
	t.Cleanup(fake.Close)

	entries := isoenv.Environ(t, user,
		"CODEX_HOME="+codexHome,
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
		"OPENAI_API_KEY=",
	)
	env := isoenv.Lookup(entries)

	code, out, errOut := argvRun(t, env, root, "harness", "install", "codex")
	if code != 0 {
		t.Fatalf("harness install codex = %d, stdout %q, stderr %q", code, out, errOut)
	}
	raw, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	if err != nil {
		t.Fatalf("the install wrote no codex config: %v", err)
	}
	var config struct {
		Providers     map[string]codexProvider `toml:"model_providers"`
		ModelProvider string                   `toml:"model_provider"`
		OpenAIBaseURL string                   `toml:"openai_base_url"`
	}
	if _, err := toml.Decode(string(raw), &config); err != nil {
		t.Fatalf("the codex config does not parse: %v\n%s", err, raw)
	}
	provider, ok := config.Providers["huddle"]
	if !ok {
		t.Fatalf("the codex config has no huddle provider:\n%s", raw)
	}
	wantBase := fmt.Sprintf("http://127.0.0.1:%d/gateway/v1", routerPort)
	if provider.BaseURL != wantBase || provider.WireAPI != "responses" || provider.EnvKey != "OPENAI_API_KEY" {
		t.Errorf("provider = %+v, want base %s, wire responses, key OPENAI_API_KEY", provider, wantBase)
	}
	if provider.SupportsWebsockets == nil || *provider.SupportsWebsockets {
		t.Errorf("supports_websockets = %v, want false: the gateway speaks HTTP only", provider.SupportsWebsockets)
	}
	if config.ModelProvider != "" || config.OpenAIBaseURL != "" {
		t.Errorf("the install chose codex's default provider (%q, %q); it only adds an opt-in one",
			config.ModelProvider, config.OpenAIBaseURL)
	}

	// the gateway's openai default points at the fake; without the file it is api.openai.com
	if err := os.WriteFile(filepath.Join(huddleHome, "gateway.json"),
		[]byte(fmt.Sprintf(`{"openai":%q}`, fake.URL)), 0o600); err != nil {
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

	t.Run("a stub on the written provider reaches the fake with its own key", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, provider.BaseURL+"/responses",
			strings.NewReader(`{"model":"gpt-e2e","input":"hi","stream":false}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer sk-codex-e2e")
		for key, value := range provider.HTTPHeaders {
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
		seen := upstream.all()
		if len(seen) != 1 {
			t.Fatalf("the fake heard %d requests, want 1: %+v", len(seen), seen)
		}
		want := codexSeen{Path: "/v1/responses", Auth: "Bearer sk-codex-e2e", Harness: "codex", Model: "gpt-e2e"}
		if seen[0] != want {
			t.Errorf("the fake heard %+v, want %+v: the client's own key, unchanged", seen[0], want)
		}
	})

	t.Run("radar's spool holds the request under codex's name", func(t *testing.T) {
		until(t, 10*time.Second, func() bool {
			return spoolHolds(radarHome, `"model":"gpt-e2e"`, `"harness":"codex"`)
		}, "a spool line for gpt-e2e from codex")
	})

	t.Run("the installed codex runs on the huddle provider", func(t *testing.T) {
		bin, err := exec.LookPath("codex")
		if err != nil {
			t.Skip("codex is not on PATH: the launch runs only where codex is installed")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		// -c is the override codex's --help documents; the huddle MCP server stays off here,
		// since this leg proves the model route, not the hub
		cmd := exec.CommandContext(ctx, bin, "exec", "--skip-git-repo-check",
			"-c", "model_provider=huddle", "-c", "mcp_servers.huddle.enabled=false",
			"-m", codexLaunchModel, "say hi")
		cmd.Env = append(append([]string{}, entries...), "OPENAI_API_KEY=sk-codex-launch")
		cmd.Dir = root
		cmd.Stdin = bytes.NewReader(nil)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &output
		if err := cmd.Start(); err != nil {
			t.Fatalf("start codex: %v", err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		heard := func() bool {
			for _, one := range upstream.all() {
				if one.Model == codexLaunchModel {
					return true
				}
			}
			return false
		}
		deadline := time.After(45 * time.Second)
	wait:
		for !heard() {
			select {
			case <-done:
				done <- nil
				break wait
			case <-deadline:
				break wait
			case <-time.After(100 * time.Millisecond):
			}
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		var got *codexSeen
		for _, one := range upstream.all() {
			if one.Model == codexLaunchModel {
				got = &one
				break
			}
		}
		if got == nil {
			t.Fatalf("codex never reached the fake through the router; its output:\n%s", output.String())
		}
		want := codexSeen{Path: "/v1/responses", Auth: "Bearer sk-codex-launch", Harness: "codex", Model: codexLaunchModel}
		if *got != want {
			t.Errorf("codex's request = %+v, want %+v", *got, want)
		}
	})
}

// spoolHolds answers whether one line of radar's spool holds every fragment.
func spoolHolds(radarHome string, fragments ...string) bool {
	files, _ := filepath.Glob(filepath.Join(radarHome, "spool", "*"))
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			all := true
			for _, fragment := range fragments {
				if !strings.Contains(line, fragment) {
					all = false
					break
				}
			}
			if all && line != "" {
				return true
			}
		}
	}
	return false
}

// stopChild ends a child this test started: SIGTERM, then a kill after 10 s.
func stopChild(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
}
