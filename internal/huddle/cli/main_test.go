package cli

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/server"
)

// huddleBin is the huddle binary TestMain builds once: the servers the tests start run it with
// the serve entry, as up does.
var huddleBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "huddle-cli-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)
	huddleBin = filepath.Join(dir, "huddle")
	build := exec.Command("go", "build", "-o", huddleBin, "github.com/MuhmdRaouf/huddle/cmd/huddle")
	build.Dir = "../../.." // the module root, from this package's directory
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building the huddle binary:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// world is one command's environment: a throwaway home and state, none of the real session's
// Huddle variables, the given overrides on top.
func world(t *testing.T, dir string, env map[string]string) *World {
	t.Helper()
	vars := map[string]string{
		"HOME":                   t.TempDir(),
		"HUDDLE_NO_PROJECT_CRED": "1",
	}
	for key, value := range env {
		if value == "" {
			delete(vars, key)
			continue
		}
		vars[key] = value
	}
	entries := make([]string, 0, len(vars))
	for key, value := range vars {
		entries = append(entries, key+"="+value)
	}
	return &World{
		Getenv:  func(key string) string { return vars[key] },
		Environ: func() []string { return append([]string{}, entries...) },
		Dir:     dir,
		Now:     func() time.Time { return time.Now() },
		Bin:     huddleBin,
	}
}

// realDir is dir with its symlinks resolved: the path a process's working directory reports on
// this machine.
func realDir(t *testing.T, dir string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve %s: %v", dir, err)
	}
	return real
}

// writeFile is the tests' one-line file writer.
func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// readFile is the tests' one-line file reader.
func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// freePort picks an unused port below every OS's ephemeral range, so nothing is handed it by a
// port-0 bind between the check and the use, and never gives one out twice.
func freePort(t *testing.T) int {
	t.Helper()
	seen := map[int]bool{}
	for i := 0; i < 500; i++ {
		p := 20000 + rand.Intn(12000)
		if seen[p] {
			continue
		}
		seen[p] = true
		if server.PortFree(p) {
			return p
		}
	}
	t.Fatal("no free port in 20000-31999")
	return 0
}

// stopHome stops the server a test started from home and waits for it: no test leaves a server
// behind.
func stopHome(t *testing.T, w *World, home string) {
	t.Helper()
	pid := RunningPid(context.Background(), w, home)
	if pid <= 0 {
		return
	}
	stopProcess(pid)
}

// stopProcess signals pid to stop and waits for it to go; a stubborn one is killed.
func stopProcess(pid int) {
	if err := signalTerm(pid); err != nil && processAlive(pid) {
		// the ask was never delivered: no wait can end in the process stopping by itself
		_ = signalKill(pid)
	}
	for i := 0; i < 50; i++ {
		if !processAlive(pid) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = signalKill(pid)
}

// runServerCommand runs one up, down or server command in w's working directory and returns its
// exit code and output.
func runServerCommand(t *testing.T, w *World, command string, args ...string) (int, string) {
	t.Helper()
	var out strings.Builder
	url := Identify(context.Background(), w, "").URL
	code := ServerCommand(context.Background(), w, command, url, args, &out, &out)
	return code, out.String()
}
