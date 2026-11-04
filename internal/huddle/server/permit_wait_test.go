package server

// The hook's long poll never loses a change that lands while the route is still setting up its
// wait: the terminal answering its own prompt in that gap withdraws the item and wakes the
// waiters, and the poll must come back with the withdrawn item instead of holding to its timeout.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
)

func TestAWithdrawBetweenTheWaitsReadAndItsHoldStillWakesIt(t *testing.T) {
	s, _ := withdrawServerForTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := s.registry.CreatePermissionItem(ctx, "s-1", "Bash",
		[]byte(`{"command":"npm test"}`), withdrawClock); err != nil {
		t.Fatalf("create the item: %v", err)
	}
	// The wait route reads the clock once for its read of the item and again for the poll stamp
	// it writes after reading the item open: the second reading is the gap between the read and
	// the hold, where the terminal's PostToolUse report withdraws the item.
	var reads int
	var withdrawn bool
	s.now = func() time.Time {
		if withdrawn {
			return withdrawClock
		}
		if reads++; reads == 2 {
			withdrawn = true
			s.withdrawPermissions(ctx, "s-1")
		}
		return withdrawClock
	}

	request := httptest.NewRequest(http.MethodGet, "/api/needs/1/wait?timeout=30", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()
	done := make(chan error, 1)
	go func() { done <- s.permitWaitRoute(recorder, request, 1) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the wait failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatal("the wait held to its timeout: the withdraw that landed in its setup woke nobody")
	}
	var item hub.PermissionItem
	if err := json.Unmarshal(recorder.Body.Bytes(), &item); err != nil {
		t.Fatalf("decode the wait's answer %q: %v", recorder.Body.String(), err)
	}
	if item.State != hub.StateWithdrawn {
		t.Fatalf("the wait answered the item %s, want it withdrawn", item.State)
	}
}
