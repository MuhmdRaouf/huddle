package ports

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
)

// A rival that probes the test's port in a tight loop, the way another test process's Free
// does (bind, close), never takes it: the port is the test's from the instant the test learns
// the number. A probe-then-bind (Free, then net.Listen) loses this race within a few hundred
// draws with "address already in use".
func TestListenHoldsItsPortAgainstARivalProbe(t *testing.T) {
	const draws = 500
	var rival struct {
		sync.Mutex
		port int
		wins int
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-done:
				return
			default:
			}
			rival.Lock()
			if rival.port != 0 {
				if listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", rival.port)); err == nil {
					_ = listener.Close()
					rival.wins++
				}
			}
			rival.Unlock()
		}
	}()
	t.Cleanup(func() { close(done); <-stopped })

	for i := 0; i < draws; i++ {
		listener := Listen(t)
		port := listener.Addr().(*net.TCPAddr).Port
		rival.Lock()
		rival.port = port
		rival.Unlock()
		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatalf("draw %d: dial %s: %v", i+1, listener.Addr(), err)
		}
		_ = conn.Close()
		// the rival stops probing before the port is let go, so a win can only be a held port
		rival.Lock()
		rival.port = 0
		wins := rival.wins
		rival.Unlock()
		_ = listener.Close()
		if wins != 0 {
			t.Fatalf("draw %d: a rival bound the held port %d, want never", i+1, port)
		}
	}
}

// Listen's cleanup closes the listener at the end of the test, and a test that closed it
// first is no failure.
func TestListenClosesAtCleanup(t *testing.T) {
	var held net.Listener
	t.Run("held", func(t *testing.T) {
		held = Listen(t)
		if host, _, _ := net.SplitHostPort(held.Addr().String()); host != "127.0.0.1" {
			t.Errorf("Listen bound %s, want 127.0.0.1", held.Addr())
		}
	})
	if _, err := held.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept after cleanup = %v, want net.ErrClosed", err)
	}
	t.Run("closed by the test", func(t *testing.T) {
		_ = Listen(t).Close()
	})
}
