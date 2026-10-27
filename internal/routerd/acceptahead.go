package routerd

// acceptAhead keeps a live port answering while its daemon still sets up: the router's setup
// runs after its port is live (a takeover inherits a socket dials already reach, and the old
// daemon has stopped accepting), so the listener accepts first and serves when ready — no dial
// is refused while it sets up. Accept answers the accepted connections in order; when the
// queue is full the loop blocks and the kernel's backlog takes over, as on a quiet start.

import (
	"net"
	"sync"

	"github.com/MuhmdRaouf/huddle/internal/adapters/safego"
)

// aheadDepth is how many connections the queue holds while nobody is serving yet.
const aheadDepth = 1024

// acceptAhead wraps l in a listener that accepts from it at once, in its own goroutine, so the
// port never sits with a full backlog while the daemon sets up.
func acceptAhead(l net.Listener, log func(string)) net.Listener {
	a := &aheadListener{l: l, queued: make(chan net.Conn, aheadDepth)}
	safego.Go(log, a.acceptLoop)
	return a
}

// aheadListener accepts ahead of its server: only acceptLoop sends on queued and closes it,
// when the raw listener stops accepting.
type aheadListener struct {
	l      net.Listener
	queued chan net.Conn
	// closeOnce keeps Close idempotent and its first error the answer for every call.
	closeOnce sync.Once
	closeErr  error
}

// acceptLoop accepts until l stops; closing queued is what ends the queue for Accept.
func (a *aheadListener) acceptLoop() {
	for {
		conn, err := a.l.Accept()
		if err != nil {
			close(a.queued)
			return
		}
		a.queued <- conn
	}
}

// Accept answers the next connection the loop took, in order; after Close it answers every
// connection already queued, then net.ErrClosed.
func (a *aheadListener) Accept() (net.Conn, error) {
	conn, ok := <-a.queued
	if !ok {
		return nil, net.ErrClosed
	}
	return conn, nil
}

// Close closes the raw listener, which ends the loop; connections already queued outlive it.
func (a *aheadListener) Close() error {
	a.closeOnce.Do(func() { a.closeErr = a.l.Close() })
	return a.closeErr
}

// Addr is the raw listener's address.
func (a *aheadListener) Addr() net.Addr { return a.l.Addr() }
