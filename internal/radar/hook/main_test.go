// The process cases: the built radar binary runs as a child process, the way Claude Code runs
// it, so the contract under test is whatever stdin, env or state dir it meets: exit 0, silence
// on stdout and stderr, and speed. Its own failures belong in hook-errors.log, and if even
// that fails, nowhere.
package hook_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit"
)

// huddleBin is the huddle binary TestMain builds once; the cases below run its radar hook as a
// child.
var huddleBin string

// TestMain builds the binary the process cases spawn, so they exercise the exact entry the
// plugin will run.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "radar-hook-binary-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "radar hook tests: %v\n", err)
		os.Exit(1)
	}
	bin := filepath.Join(dir, "huddle")
	build := exec.Command("go", "build", "-o", bin, "./cmd/huddle")
	build.Dir = "../../.."
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building ./cmd/huddle: %v\n%s", err, out)
		os.Exit(1)
	}
	huddleBin = bin
	code := m.Run()
	// os.Exit would skip a defer, so the build dir goes here: the suite must leave no temp behind.
	os.RemoveAll(dir)
	os.Exit(code)
}

// run is one hook invocation's exit code and both output streams.
type run struct {
	code   int
	stdout string
	stderr string
}

// runHookProcess runs the built hook with the payload on stdin, the way hooks.json does.
func runHookProcess(t *testing.T, input string, env []string) run {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, huddleBin, "radar", "hook")
	cmd.Env = env
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("running %s radar hook: %v", huddleBin, err)
		}
		code = exitErr.ExitCode()
	}
	return run{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// expectSilentSuccess pins the contract: a healthy run and a failing one are both
// indistinguishable from silence.
func expectSilentSuccess(t *testing.T, r run) {
	t.Helper()
	if r.code != 0 {
		t.Errorf("exit = %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	if r.stdout != "" {
		t.Errorf("stdout = %q, want empty", r.stdout)
	}
	if r.stderr != "" {
		t.Errorf("stderr = %q, want empty", r.stderr)
	}
}

// radarHomeOf digs RADAR_HOME out of the child environment the case built.
func radarHomeOf(env []string) string {
	for _, entry := range env {
		if key, value, _ := strings.Cut(entry, "="); key == "RADAR_HOME" {
			return value
		}
	}
	return ""
}

// lastSpoolLine is the last line the spool file for today collected (the date the hook itself
// stamps).
func lastSpoolLine(t *testing.T, env []string) map[string]any {
	t.Helper()
	file := filepath.Join(radarHomeOf(env), "spool", time.Now().UTC().Format("2006-01-02")+".jsonl")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("reading the spool: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var line map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &line); err != nil {
		t.Fatalf("parsing the last spool line %q: %v", lines[len(lines)-1], err)
	}
	return line
}

func TestBuiltHookUnderHostileInput(t *testing.T) {
	t.Run("exits 0 in silence on empty stdin", func(t *testing.T) {
		e := makeEnv(t)
		expectSilentSuccess(t, runHookProcess(t, "", e.environ))
		if lastSpoolLine(t, e.environ)["event"] != "unknown" {
			t.Error("blank stdin recorded a known event, want unknown")
		}
	})

	t.Run("exits 0 in silence on invalid JSON, logging it to hook-errors.log instead", func(t *testing.T) {
		e := makeEnv(t)
		if err := os.MkdirAll(e.state, 0o700); err != nil { // the log needs a writable state dir, like any append
			t.Fatal(err)
		}
		expectSilentSuccess(t, runHookProcess(t, `{"hook_event_name": oops`, e.environ))
		data, err := os.ReadFile(filepath.Join(e.state, "hook-errors.log"))
		if err != nil {
			t.Fatalf("reading hook-errors.log: %v", err)
		}
		if !strings.Contains(string(data), "unparseable stdin") {
			t.Errorf("hook-errors.log holds %q, want an unparseable-stdin line", data)
		}
	})

	t.Run("exits 0 in silence on a 5 MiB payload", func(t *testing.T) {
		e := makeEnv(t)
		big := fmt.Sprintf(`{"hook_event_name":"UserPromptSubmit","session_id":"big","prompt":"%s"}`,
			strings.Repeat("x", 5*1024*1024))
		expectSilentSuccess(t, runHookProcess(t, big, e.environ))
		line := lastSpoolLine(t, e.environ)
		if line["event"] != "UserPromptSubmit" || line["session_id"] != "big" {
			t.Errorf("last spool line = %v, want the 5 MiB payload's event and session", line)
		}
	})

	t.Run("exits 0 in silence on an unknown event", func(t *testing.T) {
		e := makeEnv(t)
		expectSilentSuccess(t, runHookProcess(t, `{"hook_event_name":"BrandNewEvent","session_id":"s1"}`, e.environ))
		if lastSpoolLine(t, e.environ)["event"] != "BrandNewEvent" {
			t.Error("unknown event not recorded under its own name")
		}
	})

	t.Run("exits 0 in silence when the state dir cannot be created (read-only parent)", func(t *testing.T) {
		e := makeEnv(t)
		readOnly := filepath.Join(e.home, "read-only")
		if err := os.MkdirAll(readOnly, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(readOnly, 0o700) })
		if err := os.Chmod(readOnly, 0o500); err != nil {
			t.Fatal(err)
		}
		expectSilentSuccess(t, runHookProcess(t, "{}", withEnv(e, "RADAR_HOME", filepath.Join(readOnly, "state"))))
	})

	t.Run("exits 0 in silence when RADAR_HOME points at a file", func(t *testing.T) {
		e := makeEnv(t)
		file := filepath.Join(e.home, "not-a-dir")
		if err := os.WriteFile(file, []byte("in the way"), 0o600); err != nil {
			t.Fatal(err)
		}
		expectSilentSuccess(t, runHookProcess(t, "{}", withEnv(e, "RADAR_HOME", file)))
	})

	// The hook gives up on its own (an internal deadline under a second); the test only bounds
	// it by the budget hooks.json gives the event, so start-up time on a loaded box cannot fail
	// it.
	t.Run("exits 0 well inside its hooks.json timeout when stdin never closes (internal deadline)", func(t *testing.T) {
		e := makeEnv(t)
		cmd := exec.Command(huddleBin, "radar", "hook")
		cmd.Env = e.environ
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		if _, err := stdin.Write([]byte(`{"hook_event_name":"UserPromptSubmit","prompt":"still ty`)); err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		waited := make(chan error, 1)
		go func() { waited <- cmd.Wait() }()
		budget := time.Duration(hookTimeoutMs(t, "UserPromptSubmit")) * time.Millisecond
		select {
		case err := <-waited:
			if err != nil {
				t.Fatalf("hook exited with %v", err)
			}
		case <-time.After(budget):
			_ = cmd.Process.Kill()
			t.Fatalf("hook did not exit within %v", budget)
		}
		if elapsed := time.Since(started); elapsed >= budget {
			t.Errorf("hook took %v, want under %v", elapsed, budget)
		}
		if stdout.String() != "" || stderr.String() != "" {
			t.Errorf("hook spoke: stdout %q, stderr %q", stdout.String(), stderr.String())
		}
	})
}

func TestBuiltHookOnTheHealthyPath(t *testing.T) {
	t.Run("records the payload as one spool line", func(t *testing.T) {
		e := makeEnv(t)
		r := runHookProcess(t, `{
			"hook_event_name": "PostToolUse",
			"session_id": "s-healthy",
			"tool_name": "Bash",
			"tool_input": {"command": "echo hi"}
		}`, e.environ)
		expectSilentSuccess(t, r)
		line := lastSpoolLine(t, e.environ)
		if line["event"] != "PostToolUse" || line["session_id"] != "s-healthy" || line["tool_name"] != "Bash" {
			t.Errorf("last spool line = %v, want the payload's event, session and tool", line)
		}
	})

	// The requirement is "never hold Claude": every run exits 0, silent, well inside the
	// timeout hooks.json gives it. How fast is a measurement, not a pass/fail line: a loaded CI
	// box or a parallel suite moves the p95 by hundreds of ms, so the p50/p95 are reported and
	// only the hooks.json budget is asserted.
	t.Run("answers every one of 50 runs silently, each well inside its hooks.json timeout (p50/p95 reported)", func(t *testing.T) {
		e := makeEnv(t)
		payload := `{"hook_event_name":"PostToolUse","session_id":"perf","tool_name":"Bash","tool_input":{"command":"ls"}}`
		budgetMs := hookTimeoutMs(t, "UserPromptSubmit") // every event gets the same command and budget
		// the race detector slows the spawning parent 5-20x, so fewer runs under it; the
		// budget asserted stays Claude Code's own
		runs := 50
		if testkit.RaceEnabled {
			runs = 10
		}
		var samples []float64
		for i := 0; i < runs; i++ {
			started := time.Now()
			r := runHookProcess(t, payload, e.environ)
			samples = append(samples, float64(time.Since(started).Microseconds())/1000)
			if r.code != 0 {
				t.Fatalf("run %d exited %d", i, r.code)
			}
			if r.stdout != "" {
				t.Fatalf("run %d wrote %q to stdout", i, r.stdout)
			}
		}
		sort.Float64s(samples)
		at := func(fraction float64) float64 { return samples[int(math.Ceil(fraction*float64(len(samples))))-1] }
		t.Logf("hook wall time over %d runs: p50 %.1f ms, p95 %.1f ms", len(samples), at(0.5), at(0.95))
		if worst := samples[len(samples)-1]; worst >= float64(budgetMs) {
			t.Errorf("slowest run took %.1f ms, want under %d ms", worst, budgetMs)
		}
	})
}

func TestAutostart(t *testing.T) {
	// The hook starts no server of its own: with no hub a SessionStart stays
	// silent and leaves no dashboard behind, autostart on or not.
	t.Run("with no hub a SessionStart starts no dashboard and stays silent", func(t *testing.T) {
		e := makeEnv(t)
		if err := os.MkdirAll(e.state, 0o700); err != nil {
			t.Fatal(err)
		}
		auto := withEnv(e, "RADAR_AUTOSTART", "1")
		expectSilentSuccess(t, runHookProcess(t, `{"hook_event_name":"SessionStart","session_id":"a1"}`, auto))
		left, err := filepath.Glob(filepath.Join(e.state, "*"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range left {
			if filepath.Base(path) == "server.json" {
				t.Errorf("a standalone dashboard left %s behind", path)
			}
		}
	})
}
