package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/outside"
	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
)

// The registry the removal watch reads: one installed plugin entry.
const pluginKey = "huddle@muhmdraouf"

// writeRegistry writes the plugin registry the child's watch reads.
func writeRegistry(t *testing.T, config, plugins string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(config, "plugins"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"version": 2, "plugins": {` + plugins + `}}`
	if err := os.WriteFile(filepath.Join(config, "plugins", "installed_plugins.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeTorn(config string) {
	_ = os.WriteFile(filepath.Join(config, "plugins", "installed_plugins.json"), []byte("{torn"), 0o600)
}

// watchedServer is a server under a temp home and a temp Claude config, watching for its plugin
// every 100 ms, with a conversation worth keeping and the run files a `huddle up` leaves.
func watchedServer(t *testing.T) *childServer {
	t.Helper()
	home := tempDir("huddle-gone-")
	config := tempDir("huddle-gone-cfg-")
	srv, err := spawnServer(map[string]string{
		"HUDDLE_HOME":          home,
		"HUDDLE_DATA":          "",
		"CLAUDE_CONFIG_DIR":    config,
		"HUDDLE_REMOVAL_WATCH": "1",
		"HUDDLE_REMOVAL_MS":    "100",
	})
	if err != nil {
		t.Fatalf("start watched server: %v", err)
	}
	t.Cleanup(srv.kill)
	srv.home = home
	srv.config = config
	entry := `"scope": "user", "installPath": "/x", "version": "1"`
	writeRegistry(t, config, `"`+pluginKey+`": [{`+entry+`}]`)
	if code, out := srv.op(t, "shop", "join", "a", "{}"); code != 200 {
		srv.kill()
		t.Fatalf("join = %d %s", code, out)
	}
	if code, out := srv.op(t, "shop", "send", "a", `{"msg": "are you still there?"}`); code != 200 {
		srv.kill()
		t.Fatalf("send = %d %s", code, out)
	}
	if err := os.MkdirAll(filepath.Join(home, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "run", "huddle.pid"), []byte(fmt.Sprint(srv.cmd.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "run", "huddle.log"), []byte("started\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return srv
}

// exited waits for the child to end by itself and answers its exit code (-1 on a timeout).
func (s *childServer) exited(ms time.Duration) int {
	done := make(chan int, 1)
	go func() {
		err := s.cmd.Wait()
		code := 0
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		}
		done <- code
	}()
	select {
	case code := <-done:
		return code
	case <-time.After(ms):
		return -1
	}
}

// get0 is one plain GET whose transport failure is the answer.
func (s *childServer) get0(path string) error {
	_, _, _, err := s.request(context.Background(), http.MethodGet, path, "", nil)
	return err
}

func TestLifecycleRemoval(t *testing.T) {
	t.Run("a watched server stays up while installed, then removes itself when the plugin is uninstalled", func(t *testing.T) {
		srv := watchedServer(t)
		time.Sleep(700 * time.Millisecond) // many checks, all saying present
		if code, _, _ := srv.get(t, "/health", nil); code != 200 {
			t.Fatalf("still installed: health = %d", code)
		}
		writeTorn(srv.config)
		time.Sleep(400 * time.Millisecond) // many checks, all unknown
		if code, _, _ := srv.get(t, "/health", nil); code != 200 {
			t.Fatalf("a torn registry is never an uninstall: health = %d", code)
		}
		writeRegistry(t, srv.config, `"other@market": [{"scope": "user", "installPath": "/x", "version": "1"}]`)
		if code := srv.exited(15 * time.Second); code != 0 {
			t.Fatalf("the server exited %d, want a clean 0", code)
		}
		if err := srv.get0("/health"); err == nil {
			t.Fatal("the server still answers after its exit")
		}
		if _, err := os.Stat(filepath.Join(srv.home, "run")); err == nil {
			t.Fatal("the run directory stayed behind")
		}
		note, err := os.ReadFile(filepath.Join(srv.home, "LEFT-BEHIND.md"))
		if err != nil {
			t.Fatalf("no LEFT-BEHIND.md: %v", err)
		}
		if !strings.Contains(string(note), filepath.Join(srv.home, "channels")) {
			t.Fatalf("the note = %s", note)
		}
		// the channel database stayed, whole and readable, its WAL folded back into the file
		db := filepath.Join(srv.home, "channels", "shop.db")
		if _, err := os.Stat(db); err != nil {
			t.Fatalf("the channel database: %v", err)
		}
		st, err := store.Open(context.Background(), db)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := st.All(context.Background(), "SELECT msg FROM events WHERE topic = 'msg'")
		if err != nil {
			t.Fatal(err)
		}
		_ = st.Close()
		var msgs []string
		for _, r := range rows {
			if m, ok := r["msg"].(string); ok {
				msgs = append(msgs, m)
			}
		}
		if len(msgs) != 1 || msgs[0] != "are you still there?" {
			t.Fatalf("the kept events = %v", msgs)
		}
		if wal, err := os.Stat(db + "-wal"); err == nil && wal.Size() > 0 {
			t.Fatalf("the WAL holds %d bytes", wal.Size())
		}
	})

	t.Run("a disabled plugin stops the server the same way, and the channel databases stay", func(t *testing.T) {
		srv := watchedServer(t)
		if err := os.WriteFile(filepath.Join(srv.config, "settings.json"),
			[]byte(`{"enabledPlugins": {"`+pluginKey+`": false}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if code := srv.exited(15 * time.Second); code != 0 {
			t.Fatalf("the server exited %d, want a clean 0", code)
		}
		if err := srv.get0("/health"); err == nil {
			t.Fatal("the server still answers after its exit")
		}
		if _, err := os.Stat(filepath.Join(srv.home, "channels", "shop.db")); err != nil {
			t.Fatalf("the channel database: %v", err)
		}
		if _, err := os.Stat(filepath.Join(srv.home, "run", "huddle.pid")); err == nil {
			t.Fatal("the pid file stayed behind")
		}
		if _, err := os.Stat(filepath.Join(srv.home, "LEFT-BEHIND.md")); err != nil {
			t.Fatal("no LEFT-BEHIND.md")
		}
	})
}

// TestServeInProcess runs the server in-process: a cancelled context stops it cleanly, and the
// removal watch ends it with the full self-removal once the registry agrees the plugin is gone.
func TestServeInProcess(t *testing.T) {
	serve := func(t *testing.T, o Options) (*syncBuffer, string, context.CancelFunc, <-chan error) {
		t.Helper()
		log := &syncBuffer{}
		o.Stdout = log
		o.Home = t.TempDir()
		o.Data = t.TempDir()
		if o.Version == "" {
			o.Version = "0.0.0"
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- Serve(ctx, o) }()
		deadline := time.Now().Add(10 * time.Second)
		for !strings.Contains(log.joined(), "on http://127.0.0.1:") {
			if time.Now().After(deadline) {
				cancel()
				t.Fatalf("the server never started: %s", log.joined())
			}
			time.Sleep(10 * time.Millisecond)
		}
		port := 0
		line := log.joined()
		at := strings.Index(line, "on http://127.0.0.1:") + len("on http://127.0.0.1:")
		for _, c := range line[at:] {
			if c < '0' || c > '9' {
				break
			}
			port = port*10 + int(c-'0')
		}
		base := fmt.Sprintf("http://127.0.0.1:%d", port)
		for {
			response, err := http.Get(base + "/health")
			if err == nil {
				_ = response.Body.Close()
				if response.StatusCode == 200 {
					break
				}
			}
			if time.Now().After(deadline) {
				cancel()
				t.Fatalf("the server never answered: %v", err)
			}
			time.Sleep(10 * time.Millisecond)
		}
		return log, base, cancel, done
	}

	t.Run("a cancelled context stops the server cleanly", func(t *testing.T) {
		log, _, cancel, done := serve(t, Options{Token: "root-in-process"})
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Serve = %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Serve never returned")
		}
		if !strings.Contains(log.joined(), "on http://127.0.0.1:") {
			t.Fatalf("the startup line = %s", log.joined())
		}
	})

	t.Run("the removal watch stops the server and leaves the channels behind", func(t *testing.T) {
		config := t.TempDir()
		if err := os.MkdirAll(filepath.Join(config, "plugins"), 0o700); err != nil {
			t.Fatal(err)
		}
		write := func(plugins string) {
			writeRegistry(t, config, plugins)
		}
		write(`"` + pluginKey + `": [{"scope": "user", "installPath": "/x", "version": "1"}]`)
		_, base, cancel, done := serve(t, Options{
			Token:        "root-in-process",
			AutoCreate:   true,
			RemovalWatch: true,
			RemovalMS:    40,
			Env:          map[string]string{"CLAUDE_CONFIG_DIR": config, "HOME": config},
		})
		defer cancel()
		// a channel with a parked wait and an open stream: the removal drains the wait and
		// closes the stream instead of stranding either
		client := &http.Client{Timeout: 20 * time.Second}
		post := func(path, body string) {
			t.Helper()
			request, err := http.NewRequest(http.MethodPost, base+path, strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("x-huddle-token", "root-in-process")
			request.Header.Set("content-type", "application/json")
			request.Header.Set(ActionHeader, strings.SplitN(path, "?", 2)[0])
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			out, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if response.StatusCode != 200 {
				t.Fatalf("POST %s = %d %s", path, response.StatusCode, out)
			}
		}
		post("/api/channels", `{"name": "bye"}`)
		post("/api/c/bye/op/join?as=dev", `{}`)
		streamCtx, stopStream := context.WithCancel(context.Background())
		defer stopStream()
		go func() {
			request, err := http.NewRequestWithContext(streamCtx, http.MethodGet, base+"/api/c/bye/live?as=dev", nil)
			if err != nil {
				return
			}
			request.Header.Set("x-huddle-token", "root-in-process")
			response, err := client.Do(request)
			if err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}()
		waitDone := make(chan struct{})
		go func() {
			defer close(waitDone)
			request, err := http.NewRequest(http.MethodPost, base+"/api/c/bye/op/wait?as=dev",
				strings.NewReader(`{"topics": ["x.*"], "timeout": 1}`))
			if err != nil {
				return
			}
			request.Header.Set("x-huddle-token", "root-in-process")
			request.Header.Set("content-type", "application/json")
			request.Header.Set(ActionHeader, "/api/c/bye/op/wait")
			response, err := client.Do(request)
			if err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}()
		time.Sleep(300 * time.Millisecond) // many checks, all saying present, both requests parked
		write(`"other@market": [{"scope": "user", "installPath": "/x", "version": "1"}]`)
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Serve = %v", err)
			}
		case <-time.After(10 * time.Second):
			cancel()
			t.Fatal("the watch never stopped the server")
		}
		select {
		case <-waitDone:
		case <-time.After(10 * time.Second):
			t.Fatal("the parked wait never returned")
		}
		stopStream()
	})

	t.Run("OptionsFromEnv reads the environment the way the entry does", func(t *testing.T) {
		dir := t.TempDir()
		o := OptionsFromEnv("3.2.1", func(name string) string {
			switch name {
			case "PORT":
				return "0"
			case "HUDDLE_HOME":
				return dir
			case "HUDDLE_DATA":
				return ""
			case "HUDDLE_TOKEN":
				return "the-root"
			case "HUDDLE_HOSTS":
				return "huddle.test, other.test"
			case "HUDDLE_AUTO_CREATE":
				return "0"
			case "CLAUDE_PROJECT_DIR":
				return "/tmp/proj"
			default:
				return ""
			}
		}, nil, nil, nil)
		if o.Version != "3.2.1" || o.Port != 0 || o.Home != dir || o.Data != dir ||
			o.Token != "the-root" || o.AutoCreate || len(o.Hosts) != 2 || o.Hosts[0] != "huddle.test" {
			t.Fatalf("options = %+v", o)
		}
		if o.Projects == nil || len(o.Projects()) == 0 {
			t.Fatal("the options carry no projects")
		}
		empty := OptionsFromEnv("3.2.1", func(string) string { return "" }, nil, nil, nil)
		if empty.Token == "" {
			t.Fatal("a server with no token handed over makes a fresh root")
		}
		if !empty.AutoCreate {
			t.Fatalf("auto-create defaults on: %+v", empty)
		}
		// nothing set names no home: the channels would sit in whatever HUDDLE_DATA says, and
		// a serve without any of it refuses rather than guessing a folder of the cwd
		if empty.Home != "" || empty.Data != "" {
			t.Fatalf("a homeless server answers no home: %+v", empty)
		}
	})
}

// silentRunner is the runner that runs nothing: an install in a test only lays the files, so no
// real service manager is reached.
type silentRunner struct{}

func (silentRunner) Run(context.Context, string, ...string) error { return nil }

// TestLifecycleRemovalUninstallsTheService pins the plugin uninstall path's service half: a home
// whose ledger records an installed service has it unloaded and its unit file and ledger removed
// when the plugin leaves Claude Code. A PATH shim stands in for the system tool, so no real
// launchctl or systemctl ever runs.
func TestLifecycleRemovalUninstallsTheService(t *testing.T) {
	home := tempDir("huddle-svc-home-")
	userHome := tempDir("huddle-svc-user-")
	xdg := tempDir("huddle-svc-xdg-")
	shim := tempDir("huddle-svc-shim-")
	tool, install, unit, unload := "systemctl", outside.InstallSystemdService,
		filepath.Join(xdg, "systemd", "user", "huddle.service"), "--user disable --now huddle"
	if runtime.GOOS == "darwin" {
		tool = "launchctl"
		install = outside.InstallLaunchdService
		unit = filepath.Join(userHome, "Library", "LaunchAgents", "me.raouf.huddle.plist")
		unload = fmt.Sprintf("bootout gui/%d/me.raouf.huddle", os.Getuid())
	}
	saw := filepath.Join(shim, "argv")
	if err := os.WriteFile(filepath.Join(shim, tool), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+saw+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	childEnv := func(name string) string {
		switch name {
		case "HOME":
			return userHome
		case "XDG_CONFIG_HOME":
			return xdg
		}
		return ""
	}
	if _, err := install(bg, outside.ServiceOptions{
		Home: home, Bin: "/x/huddle", Env: childEnv, UID: os.Getuid(), Runner: silentRunner{},
	}); err != nil {
		t.Fatalf("install the service: %v", err)
	}
	config := tempDir("huddle-svc-cfg-")
	srv, err := spawnServer(map[string]string{
		"HUDDLE_HOME":          home,
		"HUDDLE_DATA":          "",
		"CLAUDE_CONFIG_DIR":    config,
		"HUDDLE_REMOVAL_WATCH": "1",
		"HUDDLE_REMOVAL_MS":    "100",
		"HOME":                 userHome,
		"XDG_CONFIG_HOME":      xdg,
		"PATH":                 shim,
	})
	if err != nil {
		t.Fatalf("start watched server: %v", err)
	}
	t.Cleanup(srv.kill)
	writeRegistry(t, config, `"`+pluginKey+`": [{"scope": "user", "installPath": "/x", "version": "1"}]`)
	time.Sleep(300 * time.Millisecond) // a check or two, all saying present
	if code, _, _ := srv.get(t, "/health", nil); code != 200 {
		t.Fatalf("still installed: health = %d", code)
	}
	writeRegistry(t, config, `"other@market": [{"scope": "user", "installPath": "/x", "version": "1"}]`)
	if code := srv.exited(15 * time.Second); code != 0 {
		t.Fatalf("the server exited %d, want a clean 0", code)
	}
	if _, err := os.Stat(unit); !os.IsNotExist(err) {
		t.Errorf("the unit file stayed behind: %v", err)
	}
	if _, ok := outside.ReadService(bg, home); ok {
		t.Error("the service ledger stayed behind")
	}
	raw, err := os.ReadFile(saw)
	if err != nil {
		t.Fatalf("the shim never ran: %v", err)
	}
	if !strings.Contains(string(raw), unload) {
		t.Errorf("the shim saw %q, want the unload %q", raw, unload)
	}
}
