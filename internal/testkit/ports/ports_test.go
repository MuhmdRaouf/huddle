package ports

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Free answers 200 calls 200 distinct ports in its range, each one a listener can take.
func TestFreeAnswersDistinctBindablePorts(t *testing.T) {
	const draws = 200
	seen := make(map[int]bool, draws)
	for i := 0; i < draws; i++ {
		port := Free(t)
		if port < portFirst || port > portLast {
			t.Fatalf("Free() = %d, want a port in %d-%d", port, portFirst, portLast)
		}
		if seen[port] {
			t.Fatalf("Free() answered %d twice in %d draws", port, i+1)
		}
		seen[port] = true
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Fatalf("bind %d: %v", port, err)
		}
		_ = listener.Close()
	}
}

// threeFreePorts answers three neighbouring free ports of the range, the three-port range the
// lease tests search over: each port is proved bindable with a listener that closes at once,
// the probe the search itself uses.
func threeFreePorts(t testing.TB) (int, int, int) {
	t.Helper()
	free := func(port int) bool {
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return false
		}
		_ = listener.Close()
		return true
	}
	span := portLast - portFirst + 1
	start := rand.IntN(span)
	for i := 0; i < span; i++ {
		port := portFirst + (start+i)%span
		if port+2 <= portLast && free(port) && free(port+1) && free(port+2) {
			return port, port + 1, port + 2
		}
	}
	t.Fatalf("no three neighbouring free ports in %d-%d", portFirst, portLast)
	return 0, 0, 0
}

// leaseAt writes dir's lease file for a port dated at, the way an answering search writes one
// dated now and a crashed run's leftover carries one dated long ago.
func leaseAt(t testing.TB, dir string, port int, at time.Time) {
	t.Helper()
	path := filepath.Join(dir, strconv.Itoa(port))
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatalf("write the lease file %s: %v", path, err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatalf("date the lease file %s: %v", path, err)
	}
}

// A port another binary leased is skipped: two of the three ports carry lease files, and the
// search answers the third, whose lease file answering then creates.
func TestFreeInSkipsThePortsOtherBinariesLeased(t *testing.T) {
	first, middle, last := threeFreePorts(t)
	dir := t.TempDir()
	leaseAt(t, dir, first, time.Now())
	leaseAt(t, dir, last, time.Now())
	got := freeIn(t, map[int]bool{}, dir, first, last)
	if got != middle {
		t.Fatalf("freeIn() = %d, want %d, the one port without a lease file", got, middle)
	}
	if _, err := os.Stat(filepath.Join(dir, strconv.Itoa(middle))); err != nil {
		t.Fatalf("stat the answer's lease file: %v", err)
	}
}

// A lease older than an hour is a crashed run's leftover: the search removes it, answers the
// port, and the lease file it answers with is fresh.
func TestAStaleLeaseFreesItsPortAgain(t *testing.T) {
	first, middle, last := threeFreePorts(t)
	dir := t.TempDir()
	leaseAt(t, dir, middle, time.Now().Add(-2*time.Hour))
	leaseAt(t, dir, first, time.Now())
	leaseAt(t, dir, last, time.Now())
	got := freeIn(t, map[int]bool{}, dir, first, last)
	if got != middle {
		t.Fatalf("freeIn() = %d, want %d, the port whose lease was two hours old", got, middle)
	}
	info, err := os.Stat(filepath.Join(dir, strconv.Itoa(middle)))
	if err != nil {
		t.Fatalf("stat the fresh lease: %v", err)
	}
	if age := time.Since(info.ModTime()); age > time.Minute {
		t.Fatalf("the leftover's lease is %v old, want fresh", age)
	}
}

// A test's lease dies with the test: the subtest answers a port, and when it ends its lease
// file is gone and the lease directory with it, so a tmpguard run's TMPDIR ends empty.
func TestTheLeaseDiesWithTheTest(t *testing.T) {
	first, _, last := threeFreePorts(t)
	dir := t.TempDir()
	var answered int
	t.Run("leased", func(t *testing.T) {
		answered = freeIn(t, map[int]bool{}, dir, first, last)
		if answered < first || answered > last {
			t.Fatalf("freeIn() = %d, want a port in %d-%d", answered, first, last)
		}
		if _, err := os.Stat(filepath.Join(dir, strconv.Itoa(answered))); err != nil {
			t.Fatalf("stat the lease file of %d: %v", answered, err)
		}
	})
	if _, err := os.Stat(filepath.Join(dir, strconv.Itoa(answered))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the lease file for %d outlived its test: %v", answered, err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the lease directory outlived the test: %v", err)
	}
}

// refusedTB is a testing.TB whose Fatalf records the refusal instead of failing the test: the
// fresh search of the guard below must refuse every port, and a refusal is freeIn's Fatalf.
type refusedTB struct {
	testing.TB
	refusal string
}

func (tb *refusedTB) Helper() {}

func (tb *refusedTB) Fatalf(format string, args ...any) {
	tb.refusal = fmt.Sprintf(format, args...)
}

// Two test binaries hold no answered map in common; the lease files are what they share. The
// first search answers a port, leases cover every port it did not answer, and a fresh search —
// an empty answered map, the way a second binary's is — must then refuse the first call's port
// along with the rest: that port is its only possible answer, so refusing every port refuses
// it.
func TestAFreshSearchSkipsThePortAnotherBinaryAnswered(t *testing.T) {
	first, middle, last := threeFreePorts(t)
	dir := t.TempDir()
	taken := freeIn(t, map[int]bool{}, dir, first, last)
	if taken < first || taken > last {
		t.Fatalf("freeIn() = %d, want a port in %d-%d", taken, first, last)
	}
	for _, port := range []int{first, middle, last} {
		if port != taken {
			leaseAt(t, dir, port, time.Now())
		}
	}
	search := &refusedTB{TB: t}
	if got := freeIn(search, map[int]bool{}, dir, first, last); got != 0 {
		t.Fatalf("a fresh search answered %d again after every port was leased", got)
	}
	if want := fmt.Sprintf("no free port in %d-%d", first, last); search.refusal != want {
		t.Fatalf("the fresh search refused %q, want %q", search.refusal, want)
	}
}
