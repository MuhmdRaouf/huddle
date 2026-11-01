package main

// A channel join that the hub answers too slowly for the session start: the real binary's hook
// gives up inside its 3 s budget and says why, and the session's `huddle mcp` bridge finishes the
// join, so the channel lists the member with its Claude session id. The hub is reached through a
// small proxy that holds the channel's join op for 3 s. Every process here is this test's child
// and every home a temp one.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// slowJoinProxy fronts the hub at target and holds every join op of channel for hold before
// passing it on; a join the caller gave up on is dropped. Everything else passes at once.
func slowJoinProxy(t *testing.T, target, channel string, hold time.Duration) string {
	t.Helper()
	upstream, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	forward := httputil.NewSingleHostReverseProxy(upstream)
	slowPath := "/api/c/" + channel + "/op/join"
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", ports.Free(t)))
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == slowPath {
			// a hub reads the request before it works on it, so it sees the caller give up
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			select {
			case <-time.After(hold):
			case <-r.Context().Done():
				return
			}
		}
		r.Host = upstream.Host // the hub answers only a request that names it
		forward.ServeHTTP(w, r)
	}))
	proxy.Listener = listener
	proxy.Start()
	t.Cleanup(proxy.Close)
	return proxy.URL
}

// memberSessionID reads the Claude session id the channel's database holds for one member; empty
// when the member or the database is not there yet.
func memberSessionID(t *testing.T, channelDB, member string) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+channelDB+"?mode=rw&_pragma=query_only(1)&_pragma=busy_timeout(2000)")
	if err != nil {
		return ""
	}
	defer db.Close()
	var id sql.NullString
	if err := db.QueryRow("SELECT session_id FROM sessions WHERE name = ?", member).Scan(&id); err != nil {
		return ""
	}
	return id.String
}

func TestE2EChannelJoinSurvivesASlowHub(t *testing.T) {
	// a short root: the homes under it stay inside the unix path bounds
	root, err := os.MkdirTemp("", "hr-cj-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	hubPort := ports.Free(t)
	hubURL := fmt.Sprintf("http://127.0.0.1:%d", hubPort)
	hubHome := filepath.Join(root, "huddle-home")
	hubData := filepath.Join(root, "huddle-data")
	env := isoenv.Lookup(isoenv.Environ(t, filepath.Join(root, "user"),
		"RADAR_HOME="+filepath.Join(root, "radar"),
		"HUDDLE_HOME="+hubHome,
		"HUDDLE_DATA="+hubData,
		"HUDDLE_ROUTER_HOME="+filepath.Join(root, "router"),
		"HUDDLE_ROUTER_WATCH=0",
		"HUDDLE_NO_PROJECT_CRED=1",
		"HUDDLE_PORT="+strconv.Itoa(hubPort),
	))
	hubDir := filepath.Join(root, "hub-project")
	project := filepath.Join(root, "proxbeam")
	for _, dir := range []string{hubDir, project} {
		if err := os.MkdirAll(filepath.Join(dir, ".git", "info"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stopHub := func() {
		_, _, _ = runBin(t, huddleBin, env, hubDir, "down")
		pollFor(5*time.Second, func() bool { return !argvHealthy(hubURL) })
		// whatever still listens on the hub's port is ended by the pid that holds it, once its
		// command line is this test's binary
		out, _ := exec.Command("lsof", "-ti", "tcp:"+strconv.Itoa(hubPort), "-sTCP:LISTEN").Output()
		for _, field := range strings.Fields(string(out)) {
			pid, err := strconv.Atoi(field)
			if err != nil || pid <= 0 {
				continue
			}
			command, _ := exec.Command("ps", "-o", "command=", "-p", field).Output()
			if !strings.HasPrefix(strings.TrimSpace(string(command)), huddleBin) {
				continue
			}
			_ = syscall.Kill(pid, syscall.SIGTERM)
			if !pollFor(3*time.Second, func() bool { return !argvHealthy(hubURL) }) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	}
	t.Cleanup(stopHub)
	if code, out, errOut := runBin(t, huddleBin, env, hubDir, "up"); code != 0 {
		t.Fatalf("huddle up = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	until(t, 20*time.Second, func() bool { return argvHealthy(hubURL) }, "the hub to answer its health path")
	ownerKey, err := os.ReadFile(filepath.Join(hubHome, "run", "owner.key"))
	if err != nil {
		t.Fatalf("the hub left no owner key: %v", err)
	}

	slowURL := slowJoinProxy(t, hubURL, "homelab", 3*time.Second)
	config, err := json.Marshal(map[string]any{
		"channel": "homelab", "as": "proxbeam", "autostart": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(project, ".agents", "huddle"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".agents", "huddle", "huddle.json"), config, 0o644); err != nil {
		t.Fatal(err)
	}
	const sessionID = "a4c70c75-1111-2222-3333-444455556666"
	session := argvLayer(env, map[string]string{
		"CLAUDE_PROJECT_DIR": project,
		"HUDDLE_TOKEN":       strings.TrimSpace(string(ownerKey)),
		"HUDDLE_SESSION":     sessionID,
		// the session reaches the hub only through the slow proxy: an address from the
		// environment is taken as given, where a project file's loopback url follows the hub
		"HUDDLE_URL": slowURL,
	})

	// the session start: the hub holds the join past the hook's budget
	t0 := time.Now()
	code, out, errOut := hooksRun(t, session, project,
		`{"hook_event_name":"SessionStart","session_id":"`+sessionID+`","source":"startup","cwd":`+strconv.Quote(project)+`}`,
		"hook", "session-start")
	if code != 0 {
		t.Fatalf("hook session-start = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if elapsed := time.Since(t0); elapsed > 5*time.Second {
		t.Errorf("the session start took %s, want it inside its 3 s budget", elapsed)
	}
	if want := "huddle: could not join homelab as proxbeam: the hub did not answer in 3 s;"; !strings.Contains(out, want) {
		t.Errorf("the session start said %q, want the line %q", out, want)
	}

	// the session's bridge finishes the join
	mcp := exec.Command(huddleBin, "mcp")
	mcp.Env = argvEntries(session)
	mcp.Dir = project
	stdin, err := mcp.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	mcp.Stdout = io.Discard
	var mcpErr strings.Builder
	mcp.Stderr = &mcpErr
	if err := mcp.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		done := make(chan struct{})
		go func() { _ = mcp.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = mcp.Process.Kill()
			<-done
		}
	})
	for _, line := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	} {
		if _, err := io.WriteString(stdin, line+"\n"); err != nil {
			t.Fatalf("write %s: %v", line, err)
		}
	}
	channelDB := filepath.Join(hubData, "channels", "homelab.db")
	if !pollFor(15*time.Second, func() bool { return memberSessionID(t, channelDB, "proxbeam") == sessionID }) {
		t.Fatalf("the channel never listed proxbeam with session %s within 15 s of the bridge starting; bridge stderr:\n%s",
			sessionID, mcpErr.String())
	}
}
