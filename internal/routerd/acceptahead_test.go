package routerd

// The accept-ahead listener: a takeover's port is live while its daemon still sets up, so the
// wrapper accepts during the setup and answers the queue afterwards. The dial counts here lean
// on the loopback backlog of 128: 300 dials before the first Accept overflow a raw listener
// and pass through this one.

import (
	"errors"
	"net"
	"testing"
	"time"
)

func TestAcceptAhead(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	ahead := acceptAhead(raw, func(string) {})
	if ahead.Addr().String() != raw.Addr().String() {
		t.Fatalf("addr = %s, want %s", ahead.Addr(), raw.Addr())
	}

	// 300 dials before anyone accepts: every one connects, because the loop carries each off
	// the kernel backlog the moment it lands.
	const dials = 300
	addr := raw.Addr().String()
	dialed := make(map[string]bool, dials)
	clients := make([]net.Conn, 0, dials)
	defer func() {
		for _, conn := range clients {
			_ = conn.Close()
		}
	}()
	for i := 0; i < dials; i++ {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err != nil {
			t.Fatalf("dial %d before any accept: %v", i, err)
		}
		dialed[conn.LocalAddr().String()] = true
		clients = append(clients, conn)
	}
	for i := 0; i < dials; i++ {
		conn, err := ahead.Accept()
		if err != nil {
			t.Fatalf("accept %d of %d: %v", i, dials, err)
		}
		if !dialed[conn.RemoteAddr().String()] {
			t.Fatalf("accept %d answered %v, want a connection the test dialed", i, conn.RemoteAddr())
		}
		_ = conn.Close()
	}

	// A connection queued before the close is still answered after it — the serving loop of an
	// http.Server takes it from the shutdown listener — and past the queue the close error
	// ends the Accept.
	last, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer last.Close()
	time.Sleep(250 * time.Millisecond) // the loop carries the connection into the queue
	if err := ahead.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	conn, err := ahead.Accept()
	if err != nil {
		t.Fatalf("the connection queued before the close: %v", err)
	}
	if conn.RemoteAddr().String() != last.LocalAddr().String() {
		t.Errorf("the queued connection is %v, want the last dial's %v", conn.RemoteAddr(), last.LocalAddr())
	}
	_ = conn.Close()
	if _, err := ahead.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("accept past the queue = %v, want %v", err, net.ErrClosed)
	}
	if err := ahead.Close(); err != nil {
		t.Fatalf("close again: %v", err)
	}
}
