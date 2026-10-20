package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
)

// TestRunningPidMatchesTheServeEntry pins the pid check against the command lines the lookup
// reports: every spelling of this binary running serve counts, and nothing else does.
func TestRunningPidMatchesTheServeEntry(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		read bool
		want int
	}{
		{"a separated path", "/usr/local/bin/huddle serve", true, 7},
		{"bare, as a shell typed it", "huddle serve", true, 7},
		{"this binary, another entry", "/usr/local/bin/huddle feed", true, 0},
		{"this binary alone", "/opt/huddle", true, 0},
		{"a command that only mentions it", "grep huddle serve", true, 0},
		{"an unreadable process", "", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.MkdirAll(filepath.Join(home, "run"), 0o700); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(home, "run", "huddle.pid"), "7")
			w := &World{Proc: Proc{Command: func(pid int) (string, bool) {
				return tc.line, tc.read
			}}, Dir: home}
			if pid := RunningPid(context.Background(), w, home); pid != tc.want {
				t.Errorf("RunningPid over %q = %d, want %d", tc.line, pid, tc.want)
			}
		})
	}
}

// TestDownStopsTheServerThePidFileNames pins down's whole stop on a server the test started: the
// pid file names a live huddle serve, the process lookups answer from fakes (the same line every
// platform's reader reports), and down ends the process — the ask first, then whatever the
// platform's force is — and takes the pid file with it.
func TestDownStopsTheServerThePidFileNames(t *testing.T) {
	home := t.TempDir()
	w := cliWorld(t, home, filepath.Join(t.TempDir(), "state"), nil)
	port := freePort(t)
	srv := exec.Command(huddleBin, "serve")
	srv.Env = isoenv.Environ(t, t.TempDir(), fmt.Sprintf("PORT=%d", port), "HOST=127.0.0.1",
		"HUDDLE_TOKEN=test-root-credential", "HUDDLE_DATA="+filepath.Join(home, "data"),
		"HUDDLE_HOME="+home)
	log, err := os.Create(filepath.Join(t.TempDir(), "child.log"))
	if err != nil {
		t.Fatal(err)
	}
	srv.Stdout, srv.Stderr = log, log
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- srv.Wait() }()
	t.Cleanup(func() { stopProcess(srv.Process.Pid) })
	healthyAt := func() bool {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}
	for i := 0; i < 300 && !healthyAt(); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if !healthyAt() {
		t.Fatal("the server did not answer /health")
	}
	pid := srv.Process.Pid
	writeFile(t, filepath.Join(home, "run", "huddle.pid"), strconv.Itoa(pid))
	writeFile(t, filepath.Join(home, "run", "huddle.port"), strconv.Itoa(port))
	w.Proc = Proc{Command: func(at int) (string, bool) {
		if at == pid {
			return "huddle serve", true
		}
		return "", false
	}}
	result := Down(context.Background(), w, "")
	if !result.OK || result.Msg != fmt.Sprintf("Huddle stopped (pid %d)", pid) {
		t.Fatalf("down = %+v, want it stopped", result)
	}
	select {
	case <-waited: // gone, whatever exit its platform's stop gave it
	case <-time.After(10 * time.Second):
		t.Fatal("the server outlived down")
	}
	_ = log.Close() // before this test's TempDirs go
	if _, err := os.Stat(filepath.Join(home, "run", "huddle.pid")); !os.IsNotExist(err) {
		t.Error("the pid file stayed after the stop")
	}
}

// TestDownLeavesAPidFileForAnotherProcessAlone pins the safety around a stale pid file: a pid
// whose command line is not this binary running serve is never signalled, on any platform, and
// down just says nothing runs. This test process is that pid.
func TestDownLeavesAPidFileForAnotherProcessAlone(t *testing.T) {
	home := t.TempDir()
	w := cliWorld(t, home, filepath.Join(t.TempDir(), "state"), nil)
	if err := os.MkdirAll(filepath.Join(home, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, "run", "huddle.pid"), strconv.Itoa(os.Getpid()))
	t.Cleanup(func() { _ = os.Remove(filepath.Join(home, "run", "huddle.pid")) })
	result := Down(context.Background(), w, "")
	if !result.OK || !strings.Contains(result.Msg, "not running") {
		t.Fatalf("down over a foreign pid = %+v", result)
	}
	if !processAlive(os.Getpid()) {
		t.Fatal("this process was signalled")
	}
}
