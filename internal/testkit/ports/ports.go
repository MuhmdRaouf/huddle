// Package ports answers tests their loopback ports.
package ports

import (
	"fmt"
	"math/rand/v2"
	"net"
	"sync"
	"testing"
)

// Free answers a port nothing listens on, from 20000-29999: outside the kernel's ephemeral
// ranges (macOS 49152-65535, Linux 32768-60999), so no :0 listener or outgoing connection of a
// parallel test takes it after it is answered. A random start and a bind check skip the ports
// already in use.
func Free(t testing.TB) int {
	t.Helper()
	answered.Lock()
	defer answered.Unlock()
	span := portLast - portFirst + 1
	start := rand.IntN(span)
	for i := 0; i < span; i++ {
		port := portFirst + (start+i)%span
		if answered.ports[port] {
			continue
		}
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		_ = listener.Close()
		answered.ports[port] = true
		return port
	}
	t.Fatalf("no free port in %d-%d", portFirst, portLast)
	return 0
}

// answered holds every port Free gave out in this process, so no two callers get the same one.
// Test-only mutable state in a testkit package: the no-package-level-state rule is about
// production code.
var answered = struct {
	sync.Mutex
	ports map[int]bool
}{ports: make(map[int]bool)}

const (
	portFirst = 20000
	portLast  = 29999
)
