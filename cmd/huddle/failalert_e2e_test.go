package main

// The fail alert, end to end: three claude requests the router cannot forward raise exactly one
// health event with the failure code in Radar's spool, and the router log names the fix. Every
// process here is this test's child, every home a temp one, and the router dies with the test.

import (
	"bufio"
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
	"syscall"
	"testing"
	"time"
)

// spoolHealth is one router health line of Radar's spool, the fields the assertion reads.
type spoolHealth struct {
	Plugin string `json:"plugin"`
	Event  string `json:"event"`
	Reason string `json:"reason"`
	Model  string `json:"model"`
}

func TestE2EFailAlert(t *testing.T) {
	root := t.TempDir()
	routerHome := filepath.Join(root, "router-home")
	radarHome := filepath.Join(root, "radar")
	// The Anthropic the router points at: a TLS upstream whose certificate the router does not
	// trust, so every claude-* request fails with ECERT — the incident's own shape.
	anthropic := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(anthropic.Close)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	routerPort := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	env := argvLayer(map[string]string{
		"PATH":                        os.Getenv("PATH"),
		"HOME":                        filepath.Join(root, "user"),
		"XDG_CONFIG_HOME":             filepath.Join(root, "xdg-config"),
		"XDG_STATE_HOME":              filepath.Join(root, "xdg-state"),
		"RADAR_HOME":                  radarHome,
		"HUDDLE_HOME":                 filepath.Join(root, "huddle-home"),
		"HUDDLE_NO_PROJECT_CRED":      "1",
		"HUDDLE_ROUTER_HOME":          routerHome,
		"HUDDLE_ROUTER_PORT":          strconv.Itoa(routerPort),
		"HUDDLE_ROUTER_ANTHROPIC_URL": anthropic.URL,
	}, nil)
	if err := os.MkdirAll(routerHome, 0o700); err != nil {
		t.Fatal(err)
	}
	providersFile := filepath.Join(routerHome, "providers.json")
	if err := os.WriteFile(providersFile, []byte(`{"providers":["zai"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(huddleBin, "router", "serve")
	cmd.Env = argvEntries(env)
	cmd.Dir = root
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	healthy := func() bool {
		client := &http.Client{Timeout: time.Second}
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/huddle-router/health", routerPort))
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == 200
	}
	if !pollFor(10*time.Second, healthy) {
		t.Fatal("the router never answered its health path")
	}

	// healthEvents reads Radar's spool under RADAR_HOME and returns its router health lines.
	healthEvents := func() []spoolHealth {
		dir := filepath.Join(radarHome, "spool")
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil
		}
		var out []spoolHealth
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".jsonl") {
				continue
			}
			file, err := os.Open(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				var line struct {
					Kind string `json:"kind"`
					spoolHealth
				}
				if json.Unmarshal(scanner.Bytes(), &line) != nil || line.Kind != "router.event" {
					continue
				}
				out = append(out, line.spoolHealth)
			}
			if scanner.Err() != nil {
				continue
			}
			_ = file.Close()
		}
		return out
	}
	failing := func() []spoolHealth {
		var out []spoolHealth
		for _, line := range healthEvents() {
			if line.Event == "upstream_failing" && line.Reason == "ECERT" {
				out = append(out, line)
			}
		}
		return out
	}

	client := &http.Client{Timeout: 10 * time.Second}
	for i := range 3 {
		resp, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/v1/messages", routerPort), "application/json",
			strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[]}`))
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatalf("read the answer: %v", err)
		}
		if resp.StatusCode != 502 || !strings.Contains(string(body), "ECERT") {
			t.Fatalf("request %d = %d %q, want a 502 naming ECERT", i+1, resp.StatusCode, body)
		}
	}

	// Within two seconds the spool holds exactly one upstream_failing event with the code.
	if !pollFor(2*time.Second, func() bool { return len(failing()) == 1 }) {
		t.Fatalf("spool health events = %+v, want exactly one upstream_failing with ECERT", healthEvents())
	}
	if lines := failing(); len(lines) != 1 || lines[0].Plugin != "anthropic" || lines[0].Model != "claude-sonnet-4-5" {
		t.Errorf("failing lines = %+v, want one naming anthropic and claude-sonnet-4-5", lines)
	}

	logText, err := os.ReadFile(filepath.Join(routerHome, "router.log"))
	if err != nil {
		t.Fatalf("read the router log: %v", err)
	}
	if want := "huddle-router: Anthropic path failing (ECERT): a certificate check failed"; !strings.Contains(string(logText), want) {
		t.Errorf("router.log = %q, want %q in it", logText, want)
	}
}
