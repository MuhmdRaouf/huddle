package server

// A withdraw whose context is cancelled or expired is its hook's request going away, not a
// failure: the sweep withdraws the open items it left open, so it logs nothing. Every other
// failure still logs its one line.

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
)

var withdrawClock = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

// withdrawServerForTest builds the server face over a real registry in a temp home with its
// log wired to a buffer, so a test reads exactly what a withdraw printed.
func withdrawServerForTest(t *testing.T) (*server, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	db, err := hub.Open(context.Background(), home.Home{Root: dir}.HubDB())
	if err != nil {
		t.Fatalf("open the registry: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var logged bytes.Buffer
	return &server{
		o:        Options{Home: dir, Data: dir, Stdout: io.Discard, Stderr: &logged},
		registry: db,
		now:      func() time.Time { return withdrawClock },
		permits:  newPermitWaiters(),
	}, &logged
}

func TestACancelledWithdrawLogsNothing(t *testing.T) {
	s, logged := withdrawServerForTest(t)
	if _, err := s.registry.CreatePermissionItem(context.Background(), "s-1", "Bash",
		[]byte(`{"command":"npm test"}`), withdrawClock); err != nil {
		t.Fatalf("create the item: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.withdrawPermissions(ctx, "s-1")
	if logged.Len() != 0 {
		t.Fatalf("the cancelled withdraw logged %q, want nothing", logged.String())
	}
}

func TestAWithdrawThatFailsOtherwiseStillLogs(t *testing.T) {
	s, logged := withdrawServerForTest(t)
	if err := s.registry.Close(); err != nil {
		t.Fatalf("close the registry: %v", err)
	}
	s.withdrawPermissions(context.Background(), "s-1")
	if n := strings.Count(logged.String(), "permit: withdraw:"); n != 1 {
		t.Fatalf("the failed withdraw logged %d lines, want one: %q", n, logged.String())
	}
}

func TestALiveWithdrawWithdrawsTheItem(t *testing.T) {
	s, logged := withdrawServerForTest(t)
	if _, err := s.registry.CreatePermissionItem(context.Background(), "s-1", "Bash",
		[]byte(`{"command":"npm test"}`), withdrawClock); err != nil {
		t.Fatalf("create the item: %v", err)
	}
	s.withdrawPermissions(context.Background(), "s-1")
	item, err := s.registry.PermissionItemByID(context.Background(), 1, withdrawClock)
	if err != nil {
		t.Fatalf("read the item: %v", err)
	}
	if item == nil || item.State != hub.StateWithdrawn {
		t.Fatalf("the item after the withdraw = %+v, want it withdrawn", item)
	}
	if logged.Len() != 0 {
		t.Fatalf("the live withdraw logged %q, want nothing", logged.String())
	}
}
