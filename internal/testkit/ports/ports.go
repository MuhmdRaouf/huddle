// Package ports answers tests their loopback ports.
package ports

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Listen answers a listener bound to 127.0.0.1 on a port the kernel picks, for a test that
// serves in its own process: the port is held from the moment the test learns it, so no other
// test can take it in between. The test's cleanup closes it; closing it earlier, or handing it
// over, is fine.
func Listen(t testing.TB) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on 127.0.0.1:0: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

// Free answers a port nothing listens on, for a child process to bind; a test that serves in
// its own process takes Listen. The port comes from 20000-29999: outside the kernel's ephemeral
// ranges (macOS 49152-65535, Linux 32768-60999), so no :0 listener or outgoing connection of a
// parallel test takes it after it is answered. A random start and a bind check skip the ports
// already in use.
//
// The answer is also a lease file under TMPDIR, and the lease is shared across the test
// binaries of one run — go test runs package binaries in parallel, and tmpguard runs them under
// one fresh TMPDIR — so a port one binary answered, another binary's search skips. A TMPDIR
// that takes no lease directory — planted, read-only — leaves a run its bind check and its own
// answers alone. A lease older than an hour is a crashed run's leftover, and its port is free
// again.
func Free(t testing.TB) int {
	t.Helper()
	answered.Lock()
	defer answered.Unlock()
	return freeIn(t, answered.ports, leaseDir(), portFirst, portLast)
}

// freeIn answers the first port in first-last that no answered mark and no live lease file
// holds and that binds. An answered port gets a lease file in dir and a cleanup that removes
// the file and then the directory, so a run whose every test ended leaves no TMPDIR entry.
func freeIn(t testing.TB, answered map[int]bool, dir string, first, last int) int {
	t.Helper()
	span := last - first + 1
	start := rand.IntN(span)
	for i := 0; i < span; i++ {
		port := first + (start+i)%span
		if answered[port] {
			continue
		}
		if !leaseFree(dir, port) {
			continue
		}
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		_ = listener.Close()
		if !lease(t, dir, port) {
			continue
		}
		answered[port] = true
		path := filepath.Join(dir, strconv.Itoa(port))
		t.Cleanup(func() {
			_ = os.Remove(path)
			_ = os.Remove(dir) // another binary may still hold a lease in it
		})
		return port
	}
	t.Fatalf("no free port in %d-%d", first, last)
	return 0
}

// leaseDir is where the lease files live: under TMPDIR, which the test binaries of one run
// share, so a lease one binary writes is a port every binary skips.
func leaseDir() string {
	return filepath.Join(os.TempDir(), "huddle-port-leases")
}

// leaseAge is how old a lease file must be to count as a crashed run's leftover.
const leaseAge = time.Hour

// leaseFree answers whether a port is leasable: its lease file is absent, or it is older than
// leaseAge and the search removes it, a crashed run's leftover freeing its port again.
func leaseFree(dir string, port int) bool {
	path := filepath.Join(dir, strconv.Itoa(port))
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	if time.Since(info.ModTime()) < leaseAge {
		return false
	}
	_ = os.Remove(path)
	return true
}

// lease creates dir's lease file for a port and answers whether the search may take it: false
// means another binary holds the lease now and the search moves on. A directory another binary
// removed between the make and the create is made again and the create retried once.
func lease(t testing.TB, dir string, port int) bool {
	t.Helper()
	path := filepath.Join(dir, strconv.Itoa(port))
	if !leaseRoot(dir) {
		// a TMPDIR that takes no lease directory — planted, read-only — shares no lease, and
		// the port stays the caller's on the bind check alone
		return true
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrNotExist) {
		if !leaseRoot(dir) {
			return true
		}
		file, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL, 0600)
	}
	switch {
	case err == nil:
		_ = file.Close()
		return true
	case errors.Is(err, os.ErrExist):
		return false
	default:
		t.Fatalf("lease %s: %v", path, err)
		return false
	}
}

// leaseRoot makes dir, the lease files' parent, and answers whether it exists: a TMPDIR that
// takes no such directory leaves the run sharing no lease.
func leaseRoot(dir string) bool {
	return os.MkdirAll(dir, 0700) == nil
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
