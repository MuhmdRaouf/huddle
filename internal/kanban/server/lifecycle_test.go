package server

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The lifecycle: nothing starts a server any more — the hub serves every
// board — so what is tested is refresh through a running server and serve's
// four exits, one log line each.

// serveRun is one in-process server Serve started; reason answers why it ended.
type serveRun struct {
	reason chan string
	cancel context.CancelFunc
	done   chan struct{}
}

// serveInProcess runs Serve the way a detached `kanban serve` would, inside
// this process: its stop is registered after the temp dirs, so the server is
// gone before they are removed.
func serveInProcess(t *testing.T, opts Options) *serveRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	run := &serveRun{cancel: cancel, reason: make(chan string, 1), done: make(chan struct{})}
	go func() {
		defer close(run.done)
		reason, err := Serve(ctx, opts)
		if err != nil {
			t.Logf("serve ended with %v", err)
		}
		run.reason <- reason
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-run.done:
		case <-time.After(5 * time.Second):
			t.Error("the server did not exit with its context")
		}
	})
	return run
}

// waitReason answers the reason one run ended within the budget.
func (r *serveRun) waitReason(t *testing.T, budget time.Duration) string {
	t.Helper()
	select {
	case reason := <-r.reason:
		return reason
	case <-time.After(budget):
		t.Fatalf("the server did not exit within %s", budget)
		return ""
	}
}

// closedPort answers a port nothing listens on: one that just closed.
func closedPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func TestRefreshAnswersTheRev(t *testing.T) {
	b := newBoard(t)
	home := t.TempDir()
	ctx := context.Background()
	writeTicket(t, b, "backlog", "REF-1")
	serveInProcess(t, Options{Board: b, Home: home, Version: "test", Port: -1, EveryMs: 25})
	waitState(t, home, b.Root)

	rev, err := Refresh(ctx, b.Root, home, DefaultEffects())
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if rev < 1 {
		t.Errorf("rev = %d, want at least 1", rev)
	}

	empty := t.TempDir()
	_, err = Refresh(ctx, b.Root, empty, DefaultEffects())
	var noServer *ErrNoServer
	if !errors.As(err, &noServer) {
		t.Fatalf("refresh with no server = %v, want ErrNoServer", err)
	}
}

func TestServeWalksTheDefaultPorts(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:18800")
	if err != nil {
		t.Skipf("18800 is held by another program: %v", err)
	}
	defer func() { _ = held.Close() }()
	probe, err := net.Listen("tcp", "127.0.0.1:18801")
	if err != nil {
		t.Skipf("18801 is held by another program: %v", err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}

	b := newBoard(t)
	home := t.TempDir()
	serveInProcess(t, Options{Board: b, Home: home, Version: "test", EveryMs: 25})
	st := waitState(t, home, b.Root)
	if st.Port != 18801 {
		t.Errorf("port = %d, want 18801 (18800 held)", st.Port)
	}
}

func TestServeExitsWhenTheBoardDisappears(t *testing.T) {
	b := newBoard(t)
	home := t.TempDir()
	run := serveInProcess(t, Options{
		Board: b, Home: home, Version: "test", Port: -1, EveryMs: 25, BoardGoneMs: 300,
	})
	waitState(t, home, b.Root)
	if err := os.RemoveAll(b.Root); err != nil {
		t.Fatal(err)
	}
	if reason := run.waitReason(t, 5*time.Second); reason != ReasonBoardGone {
		t.Errorf("reason = %q, want %q", reason, ReasonBoardGone)
	}
	if ReadState(home, KeyOf(b.Root)) != nil {
		t.Error("the state file survived the exit")
	}
	log := readServerLog(t, home, b.Root)
	if !strings.Contains(log, "kanban: stopped (reason: board gone)") {
		t.Errorf("log = %q, want the board-gone line", log)
	}
}

func TestServeBowsOutToAnotherServer(t *testing.T) {
	b := newBoard(t)
	home := t.TempDir()
	first := serveInProcess(t, Options{Board: b, Home: home, Version: "test", Port: -1, EveryMs: 25})
	st := waitState(t, home, b.Root)

	second := serveInProcess(t, Options{Board: b, Home: home, Version: "test", Port: -1, EveryMs: 25})
	if reason := second.waitReason(t, 5*time.Second); reason != ReasonDuplicate {
		t.Errorf("reason = %q, want %q", reason, ReasonDuplicate)
	}
	select {
	case reason := <-first.reason:
		t.Fatalf("the first server left too (%q)", reason)
	default:
	}
	if kept := ReadState(home, KeyOf(b.Root)); kept == nil || kept.Pid != st.Pid {
		t.Errorf("the duplicate removed the running server's state: %+v", kept)
	}
	log := readServerLog(t, home, b.Root)
	if !strings.Contains(log, "kanban: stopped (reason: duplicate)") {
		t.Errorf("log = %q, want the duplicate line", log)
	}
}

func TestServeBowsOutToASuccessor(t *testing.T) {
	// two starts that raced both bound and both wrote the state: the one the
	// state no longer names bows out on its next check
	b := newBoard(t)
	home := t.TempDir()
	run := serveInProcess(t, Options{
		Board: b, Home: home, Version: "test", Port: -1, EveryMs: 25, DupMs: 200,
	})
	st := waitState(t, home, b.Root)

	successor := NewApp(Options{Board: b, Home: home, Version: "test", EveryMs: 25})
	listenApp(t, successor)
	abs := absPath(b.Root)
	if err := WriteState(context.Background(), home, KeyOf(abs), State{
		Pid: st.Pid + 1, Port: successor.Port, Board: abs, Token: "theirs",
	}); err != nil {
		t.Fatal(err)
	}

	if reason := run.waitReason(t, 3*time.Second); reason != ReasonDuplicate {
		t.Errorf("reason = %q, want %q", reason, ReasonDuplicate)
	}
	if kept := ReadState(home, KeyOf(abs)); kept == nil || kept.Token != "theirs" {
		t.Errorf("the loser removed its successor's state: %+v", kept)
	}
}

func TestServeStopsWithItsContext(t *testing.T) {
	b := newBoard(t)
	home := t.TempDir()
	run := serveInProcess(t, Options{Board: b, Home: home, Version: "test", Port: -1, EveryMs: 25})
	waitState(t, home, b.Root)

	run.cancel()
	if reason := run.waitReason(t, 3*time.Second); reason != ReasonSignal {
		t.Errorf("reason = %q, want %q", reason, ReasonSignal)
	}
	if ReadState(home, KeyOf(b.Root)) != nil {
		t.Error("the state file survived the exit")
	}
	log := readServerLog(t, home, b.Root)
	if !strings.Contains(log, "kanban: listening on ") || !strings.Contains(log, "kanban: stopped (reason: signal)") {
		t.Errorf("log = %q, want a listening line and the signal line", log)
	}
}

func TestStateFileIsPrivate(t *testing.T) {
	b := newBoard(t)
	home := t.TempDir()
	serveInProcess(t, Options{Board: b, Home: home, Version: "test", Port: -1, EveryMs: 25})
	st := waitState(t, home, b.Root)
	info, err := os.Stat(filepath.Join(home, KeyOf(b.Root)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("state mode = %o, want 600", info.Mode().Perm())
	}
	_ = st
}

func TestStateDirAndKey(t *testing.T) {
	t.Run("the home wins over the default", func(t *testing.T) {
		env := map[string]string{"KANBAN_HOME": "/tmp/kanban-home"}
		if got := StateDir(func(key string) (string, bool) { v, ok := env[key]; return v, ok }); got != "/tmp/kanban-home" {
			t.Errorf("StateDir = %q", got)
		}
	})

	t.Run("without a home the user's agents dir answers", func(t *testing.T) {
		env := map[string]string{"HOME": "/home/tester"}
		key := "HOME"
		got := StateDir(func(name string) (string, bool) { v, ok := env[name]; return v, ok })
		want := filepath.Join(env[key], ".agents", "kanban-server")
		if got != want {
			t.Errorf("StateDir = %q, want %q", got, want)
		}
	})

	t.Run("the key is the board path's first 12 sha256 hex", func(t *testing.T) {
		if got := KeyOf("/a/board"); len(got) != 12 {
			t.Errorf("KeyOf = %q, want 12 hex characters", got)
		}
		if KeyOf("/a/board") != KeyOf(filepath.Join("/a", "board")) {
			t.Error("two spellings of one path answer different keys")
		}
		if KeyOf("/a/board") == KeyOf("/a/other") {
			t.Error("two boards share one key")
		}
	})
}

// readServerLog answers the server's log file for one board.
func readServerLog(t *testing.T, home string, boardPath string) string {
	t.Helper()
	data, err := os.ReadFile(LogPath(home, KeyOf(boardPath)))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(data)
}
