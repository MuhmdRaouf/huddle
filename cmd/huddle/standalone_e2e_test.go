package main

// The daemon is the only server, end to end: with no huddle home, the radar
// word and the session-start hook start no dashboard and no board server of
// their own, and the gateway words point at huddle-router, which serves the
// gateway's routes under /gateway. Every process here is this test's child,
// every home a temp one, and the browser opener is a fake on PATH.

import (
	"fmt"
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

	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

func TestE2EStandaloneWordsStartNothing(t *testing.T) {
	root := t.TempDir()
	user := filepath.Join(root, "user")
	huddleHome := filepath.Join(root, "huddle-home") // step 1 never creates it
	radarHome := filepath.Join(user, "radar")
	kanbanHome := filepath.Join(user, "kanban")
	routerHome := filepath.Join(root, "router-home")
	routerPort := ports.Free(t)

	// the fake browser opener: it records the url and answers at once
	opener := filepath.Join(root, "bin")
	opened := filepath.Join(root, "opened")
	script := "#!/bin/sh\nprintf '%s\\n' \"$1\" >> " + strconv.Quote(opened) + "\n"
	if err := os.MkdirAll(opener, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"open", "xdg-open"} {
		if err := os.WriteFile(filepath.Join(opener, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	env := argvLayer(map[string]string{
		"PATH":                   opener + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME":                   user,
		"XDG_CONFIG_HOME":        filepath.Join(user, ".config"),
		"XDG_STATE_HOME":         filepath.Join(user, ".local", "state"),
		"HUDDLE_HOME":            huddleHome,
		"RADAR_HOME":             radarHome,
		"KANBAN_HOME":            kanbanHome,
		"HUDDLE_ROUTER_HOME":     routerHome,
		"HUDDLE_ROUTER_PORT":     strconv.Itoa(routerPort),
		"HUDDLE_NO_PROJECT_CRED": "1",
		"HUDDLE_URL":             "",
		"HUDDLE_CHANNEL":         "",
		"HUDDLE_AS":              "",
		"HUDDLE_TOKEN":           "",
		// the hook starts the hub only when the project's identity says so; this
		// leg owns the "nothing starts" proof, so no autostart rides along
		"HUDDLE_AUTOSTART": "0",
		"RADAR_AUTOSTART":  "0",
	}, nil)

	t.Run("radar start with no huddle home exits 6", func(t *testing.T) {
		code, out, errOut := argvRun(t, env, root, "radar", "start")
		if code != 6 {
			t.Fatalf("radar start = %d, stdout %q, stderr %q", code, out, errOut)
		}
		if want := "radar: the huddle hub is not set up; run /huddle:setup\n"; errOut != want {
			t.Errorf("stderr = %q, want %q", errOut, want)
		}
	})

	t.Run("the session-start hook exits 0 in silence", func(t *testing.T) {
		code, out, errOut := runSh(t, 30*time.Second, env, root,
			strconv.Quote(huddleBin)+" hook session-start",
			`{"hook_event_name":"SessionStart"}`)
		if code != 0 {
			t.Fatalf("the hook = %d, stdout %q, stderr %q", code, out, errOut)
		}
	})

	t.Run("no radar dashboard and no board server was started", func(t *testing.T) {
		for _, path := range []string{
			filepath.Join(radarHome, "server.json"),
			filepath.Join(huddleHome, "gateway.port"),
			filepath.Join(huddleHome, "gateway-run.json"),
		} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("%s exists; a standalone server was started", path)
			}
		}
		if entries, err := filepath.Glob(filepath.Join(kanbanHome, "*")); err == nil && len(entries) != 0 {
			t.Errorf("the kanban state dir holds %v, want it empty: no kanban serve was started", entries)
		}
		// nothing of this test's world runs a server of its own: a process whose
		// command line names the temp root as a dashboard or board server is a
		// leak this test made
		out, err := exec.Command("pgrep", "-f", "radar start --foreground|kanban serve").Output()
		if err == nil {
			for _, pid := range strings.Fields(strings.TrimSpace(string(out))) {
				line, err := exec.Command("ps", "-o", "command=", "-p", pid).Output()
				if err != nil {
					continue
				}
				if strings.Contains(string(line), root) {
					t.Errorf("pid %s still runs %q: a standalone server outlived the words", pid, line)
				}
			}
		}
	})

	t.Run("gateway status with no router running is exit 6", func(t *testing.T) {
		code, out, errOut := argvRun(t, env, root, "gateway", "status")
		if code != 6 {
			t.Fatalf("gateway status = %d, stdout %q, stderr %q", code, out, errOut)
		}
		if want := "gateway: huddle-router is not running; huddle router start starts it\n"; errOut != want {
			t.Errorf("stderr = %q, want %q", errOut, want)
		}
	})

	t.Run("gateway stop is only words", func(t *testing.T) {
		code, out, errOut := argvRun(t, env, root, "gateway", "stop")
		if code != 0 {
			t.Fatalf("gateway stop = %d, stderr %q", code, errOut)
		}
		if want := "gateway: served by huddle-router; it stops with the router\n"; out != want {
			t.Errorf("stdout = %q, want %q", out, want)
		}
	})

	// From here the daemon runs: a fake upstream, a gateway.json route to it, and
	// `huddle router serve` as this test's child.
	upstreamSeen := ""
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 4096)
		n, _ := r.Body.Read(body)
		upstreamSeen = r.URL.Path + " " + string(body[:n])
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1"}`))
	}))
	t.Cleanup(upstream.Close)

	if err := os.MkdirAll(huddleHome, 0o700); err != nil {
		t.Fatal(err)
	}
	gatewayJSON := filepath.Join(huddleHome, "gateway.json")
	route := fmt.Sprintf(`{"routes":[{"match":"e2e-*","upstream":%q}]}`, upstream.URL)
	if err := os.WriteFile(gatewayJSON, []byte(route), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(routerHome, 0o700); err != nil {
		t.Fatal(err)
	}
	providers := filepath.Join(routerHome, "providers.json")
	if err := os.WriteFile(providers, []byte(`{"providers":["zai"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	daemonEnv := argvLayer(env, map[string]string{
		"ZAI_ROUTER_URL":              upstream.URL,
		"ZAI_API_KEY":                 "e2e-key",
		"HUDDLE_ROUTER_ANTHROPIC_URL": upstream.URL,
	})
	serve := exec.Command(huddleBin, "router", "serve")
	serve.Env = argvEntries(daemonEnv)
	serve.Dir = root
	if err := serve.Start(); err != nil {
		t.Fatalf("start huddle router serve: %v", err)
	}
	t.Cleanup(func() {
		if serve.Process != nil {
			_ = serve.Process.Signal(syscall.SIGTERM)
			done := make(chan struct{})
			go func() { _ = serve.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				_ = serve.Process.Kill()
				<-done
			}
		}
	})
	routerHealth := fmt.Sprintf("http://127.0.0.1:%d/huddle-router", routerPort)
	until(t, 15*time.Second, func() bool { return healthy(routerHealth) }, "the daemon to answer its health path")

	t.Run("gateway status names the router's /gateway address", func(t *testing.T) {
		code, out, errOut := argvRun(t, daemonEnv, root, "gateway", "status")
		if code != 0 {
			t.Fatalf("gateway status = %d, stdout %q, stderr %q", code, out, errOut)
		}
		want := fmt.Sprintf("gateway: served by huddle-router at http://127.0.0.1:%d/gateway\n", routerPort)
		if out != want {
			t.Errorf("stdout = %q, want %q", out, want)
		}
	})

	t.Run("a request under /gateway reaches the route's upstream", func(t *testing.T) {
		url := fmt.Sprintf("http://127.0.0.1:%d/gateway/v1/chat/completions", routerPort)
		resp, err := http.Post(url, "application/json", strings.NewReader(`{"model":"e2e-1","messages":[]}`))
		if err != nil {
			t.Fatalf("POST %s: %v", url, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("the gateway request = %d, want 200", resp.StatusCode)
		}
		if !strings.HasPrefix(upstreamSeen, "/v1/chat/completions ") {
			t.Errorf("the upstream saw %q, want the /gateway prefix stripped and the body kept", upstreamSeen)
		}
		if !strings.Contains(upstreamSeen, "e2e-1") {
			t.Errorf("the upstream saw %q, want the model in the body", upstreamSeen)
		}
	})
}
