package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The whole server flow through the built huddle binary's kanban word: init,
// start naming the hub (and starting nothing), stop pointing at the hub, then
// the hand-run foreground server carrying the live view — a ticket seen on
// /api/events, refresh through it, its exit, and nothing left behind. Every
// temp home and project is the test's own; the port is the OS's pick, never a
// fixed one.

// buildHuddle builds the binary once for the e2e.
func buildHuddle(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "huddle")
	build := exec.Command("go", "build", "-o", bin, "./cmd/huddle")
	build.Dir = "../../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// e2e is one throwaway project with its own kanban home and its own huddle
// home, where no hub answers and none can be started: the home was never set
// up.
type e2e struct {
	t         *testing.T
	bin       string
	project   string
	home      string
	hubHome   string
	boardDir  string
	statePath string
}

func newE2E(t *testing.T) *e2e {
	t.Helper()
	project := t.TempDir()
	e := &e2e{
		t: t, bin: buildHuddle(t), project: project, home: t.TempDir(), hubHome: filepath.Join(t.TempDir(), "huddle"),
		boardDir: filepath.Join(project, ".agents", "kanban"),
	}
	return e
}

// run runs one kanban word of the binary in the project and answers its exit
// code and combined output.
func (e *e2e) run(args ...string) (int, string) {
	e.t.Helper()
	cmd := exec.Command(e.bin, append([]string{"kanban"}, args...)...)
	cmd.Dir = e.project
	cmd.Env = append(os.Environ(), "KANBAN_HOME="+e.home, "HUDDLE_HOME="+e.hubHome, "KANBAN_NO_OPEN=1")
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	code := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			e.t.Fatalf("run %v: %v", args, err)
		}
		code = exit.ExitCode()
	}
	return code, out.String()
}

// stateFile answers the one state file the home holds.
func (e *e2e) stateFile(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		matches, err := filepath.Glob(filepath.Join(e.home, "*.json"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) == 1 {
			return matches[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the home holds no state file")
	return ""
}

// recordedState remembers the state file and answers the recorded server.
func (e *e2e) recordedState(t *testing.T) (pid int, port int) {
	t.Helper()
	e.statePath = e.stateFile(t)
	data, err := os.ReadFile(e.statePath)
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Pid  int `json:"pid"`
		Port int `json:"port"`
	}
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	return st.Pid, st.Port
}

// eventsLine answers the next stream line within the budget.
func eventsLine(t *testing.T, lines chan string, budget time.Duration) string {
	t.Helper()
	select {
	case line, ok := <-lines:
		if !ok {
			t.Fatal("the event stream ended")
		}
		return line
	case <-time.After(budget):
		t.Fatalf("no event line within %s", budget)
		return ""
	}
}

func TestServerEndToEnd(t *testing.T) {
	e := newE2E(t)

	if code, out := e.run("init"); code != 0 {
		t.Fatalf("init = %d, %q", code, out)
	}

	// with no hub and no set-up home, start starts nothing and says so
	if code, out := e.run("start", "--no-open"); code != 6 ||
		!strings.Contains(out, "kanban: the huddle hub is not set up; run /huddle:setup") {
		t.Fatalf("start = %d, %q, want 6 with the setup line", code, out)
	}
	if entries, err := filepath.Glob(filepath.Join(e.home, "*")); err != nil || len(entries) != 0 {
		t.Fatalf("start left %v in the home, want it empty", entries)
	}

	// stop is words: the hub is what serves the boards, and /huddle:stop ends it
	if code, out := e.run("stop"); code != 0 ||
		!strings.Contains(out, "kanban: boards are served by the huddle hub; /huddle:stop stops it") {
		t.Fatalf("stop = %d, %q, want 0 and the hub line", code, out)
	}

	// the hand-run foreground server carries the live view
	serve := exec.Command(e.bin, "kanban", "serve", "--port", "0")
	serve.Dir = e.project
	serve.Env = append(os.Environ(), "KANBAN_HOME="+e.home, "KANBAN_NO_OPEN=1")
	var serveOut strings.Builder
	serve.Stdout = &serveOut
	serve.Stderr = &serveOut
	if err := serve.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	t.Cleanup(func() {
		if serve.Process != nil && serve.ProcessState == nil {
			_ = serve.Process.Signal(syscall.SIGTERM)
			done := make(chan struct{})
			go func() { _ = serve.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = serve.Process.Kill()
				<-done
			}
		}
	})
	pid, port := e.recordedState(t)
	address := fmt.Sprintf("http://127.0.0.1:%d", port)

	// the live view: the snapshot first, then the ticket this test writes
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("open /api/events: %v", err)
	}
	if response.StatusCode != 200 {
		t.Fatalf("/api/events = %d", response.StatusCode)
	}
	lines := make(chan string, 256)
	go func() {
		defer func() { _ = response.Body.Close(); close(lines) }()
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()

	snapshot := false
	deadline := time.Now().Add(2 * time.Second)
	for !snapshot && time.Now().Before(deadline) {
		if line := eventsLine(t, lines, time.Until(deadline)); strings.HasPrefix(line, "event: snapshot") {
			snapshot = true
		}
	}
	if !snapshot {
		t.Fatal("no snapshot arrived on /api/events")
	}

	ticket := filepath.Join(e.boardDir, "backlog", "E2E-1.md")
	if err := os.WriteFile(ticket, []byte("---\nid: E2E-1\ntitle: \"live\"\ntype: docs\n---\n\n# E2E-1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seen, delta := false, ""
	deadline = time.Now().Add(2 * time.Second)
	for !seen && time.Now().Before(deadline) {
		line := eventsLine(t, lines, time.Until(deadline))
		if strings.HasPrefix(line, "event: delta") {
			delta = line
			continue
		}
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, "E2E-1") {
			seen = delta != ""
		}
	}
	if !seen {
		t.Fatal("the written ticket never arrived on /api/events")
	}

	if code, out := e.run("refresh"); code != 0 || !strings.Contains(out, "kanban: refreshed (rev ") {
		t.Errorf("refresh = %d, %q", code, out)
	}

	// the server ends with its signal and takes its state file with it. Wait
	// first: a child not yet reaped is a zombie that answers every liveness
	// probe, so the wait is the proof the signal ended it.
	if err := serve.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal serve: %v", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- serve.Wait() }()
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("serve ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = serve.Process.Kill()
		t.Fatal("the server process survived its signal")
	}
	if _, err := os.Stat(e.statePath); err == nil {
		t.Error("the state file survived the stop")
	}
	_ = pid
	logData, err := os.ReadFile(strings.TrimSuffix(e.statePath, ".json") + ".log")
	if err != nil {
		t.Fatalf("the server log is missing: %v", err)
	}
	log := string(logData)
	if !strings.Contains(log, "kanban: listening on ") || !strings.Contains(log, "kanban: stopped (reason: signal)") {
		t.Errorf("log = %q, want a listening line and the signal line", log)
	}
}
