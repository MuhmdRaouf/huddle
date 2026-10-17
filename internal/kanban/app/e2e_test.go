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
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/process"
)

// The whole server flow through the built huddle binary's kanban word: init,
// start --no-open, a ticket seen live on /api/events, refresh, stop, and
// nothing left behind — no child process, no state file. Every temp home and
// project is the test's own; the port is the OS's pick, never a fixed one.

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

// e2e is one throwaway project with its own kanban home.
type e2e struct {
	t         *testing.T
	bin       string
	project   string
	home      string
	boardDir  string
	statePath string
}

func newE2E(t *testing.T) *e2e {
	t.Helper()
	project := t.TempDir()
	e := &e2e{
		t: t, bin: buildHuddle(t), project: project, home: t.TempDir(),
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
	cmd.Env = append(os.Environ(), "KANBAN_HOME="+e.home, "KANBAN_NO_OPEN=1")
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

// reap stops the server the test started and waits for its exit, so no process
// outlives the test and no temp dir is removed under an open log file. The
// kill is the last resort for a server that ignored its stop.
func (e *e2e) reap(pid int) {
	e.t.Helper()
	e.t.Cleanup(func() {
		_, _ = e.run("stop")
		deadline := time.Now().Add(5 * time.Second)
		for process.IsAlive(int64(pid)) && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if process.IsAlive(int64(pid)) {
			if proc, err := os.FindProcess(pid); err == nil {
				_ = proc.Kill()
			}
		}
	})
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

	code, out := e.run("start", "--no-open", "--port", "0")
	if code != 0 {
		t.Fatalf("start = %d, %q", code, out)
	}
	address := regexp.MustCompile(`http://127\.0\.0\.1:\d+`).FindString(out)
	if address == "" {
		t.Fatalf("start printed no address: %q", out)
	}
	pid, port := e.recordedState(t)
	if want := fmt.Sprintf(":%d", port); !strings.HasSuffix(address, want) {
		t.Errorf("start printed %q, the server listens on %s", address, want)
	}
	e.reap(pid)

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

	if code, out := e.run("stop"); code != 0 || !strings.Contains(out, "kanban: stopped") {
		t.Fatalf("stop = %d, %q", code, out)
	}
	gone := time.Now().Add(3 * time.Second)
	for process.IsAlive(int64(pid)) && time.Now().Before(gone) {
		time.Sleep(50 * time.Millisecond)
	}
	if process.IsAlive(int64(pid)) {
		t.Fatal("the server process survived its stop")
	}
	if _, err := os.Stat(e.statePath); err == nil {
		t.Error("the state file survived the stop")
	}
	logData, err := os.ReadFile(strings.TrimSuffix(e.statePath, ".json") + ".log")
	if err != nil {
		t.Fatalf("the server log is missing: %v", err)
	}
	log := string(logData)
	if !strings.Contains(log, "kanban: listening on ") || !strings.Contains(log, "kanban: stopped (reason: stop)") {
		t.Errorf("log = %q, want a listening line and the stop line", log)
	}

	code, out = e.run("stop")
	if code != 6 || !strings.Contains(out, "kanban: no server for ") {
		t.Errorf("second stop = %d, %q, want 6 naming the board", code, out)
	}
}
