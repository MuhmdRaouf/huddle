package server

// The idle guard's reference: a second process of this test binary that sits idle in the Go
// runtime beside the hub through the same windows. Whatever the runner charges any idle Go
// process (scheduler wakeups, timer slack, background load) lands in the reference's share as
// well as the hub's, so the hub is judged on the share it adds beyond the reference.

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
)

// idleReferenceEnv marks the re-executed test binary as the reference child.
const idleReferenceEnv = "HUDDLE_IDLE_REFERENCE"

// idleReference is the running reference child, asked for its CPU time over its pipes.
type idleReference struct {
	pid    int
	stdin  io.Writer
	answer *bufio.Reader
}

// TestIdleReferenceHelper is the reference child: an open loopback listener nobody dials and a
// stdin read, nothing else. Each line on stdin asks for the process's CPU time so far in
// nanoseconds; the child answers with one line. It exits when stdin closes.
func TestIdleReferenceHelper(t *testing.T) {
	if os.Getenv(idleReferenceEnv) == "" {
		t.Skip("the idle guard's helper process, not a test")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	fmt.Println("ready", os.Getpid())
	asks := bufio.NewScanner(os.Stdin)
	for asks.Scan() {
		fmt.Println(cpuTime(t).Nanoseconds())
	}
}

// startIdleReference starts the reference child, waits for its ready line and stops it when
// the test ends.
func startIdleReference(t *testing.T, root string) *idleReference {
	t.Helper()
	child := exec.Command(os.Args[0], "-test.run=^TestIdleReferenceHelper$")
	child.Env = isoenv.Environ(t, filepath.Join(root, "reference"), idleReferenceEnv+"=1")
	child.Stderr = os.Stderr
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		exited := make(chan struct{})
		go func() { _ = child.Wait(); close(exited) }()
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			_ = child.Process.Kill()
			<-exited
		}
	})
	reference := &idleReference{stdin: stdin, answer: bufio.NewReader(stdout)}
	// the child's TestMain may print before the test runs: the ready line is the one that parses
	for reference.pid == 0 {
		line, err := reference.answer.ReadString('\n')
		if err != nil {
			t.Fatalf("the idle reference never said it was ready: %v", err)
		}
		if fields := strings.Fields(line); len(fields) == 2 && fields[0] == "ready" {
			reference.pid, _ = strconv.Atoi(fields[1])
		}
	}
	return reference
}

// cpuTime is the reference's user plus system time so far, as the child itself reports it.
func (r *idleReference) cpuTime(t *testing.T) time.Duration {
	t.Helper()
	if _, err := fmt.Fprintln(r.stdin); err != nil {
		t.Fatalf("asking the idle reference for its CPU time: %v", err)
	}
	line, err := r.answer.ReadString('\n')
	if err != nil {
		t.Fatalf("reading the idle reference's CPU time: %v", err)
	}
	nanos, err := strconv.ParseInt(strings.TrimSpace(line), 10, 64)
	if err != nil {
		t.Fatalf("the idle reference's CPU time %q: %v", line, err)
	}
	return time.Duration(nanos)
}

// idleWindowVerdict is idleVerdict over the share the hub used beyond the reference in each
// window: the hub's share less the reference's in the same window, read as 0 when the
// reference's is larger.
func idleWindowVerdict(hub, reference []float64, budget float64) (held, done bool) {
	excess := make([]float64, len(hub))
	for i := range hub {
		excess[i] = max(hub[i]-reference[i], 0)
	}
	return idleVerdict(excess, budget)
}

func TestIdleWindowVerdict(t *testing.T) {
	for _, tt := range []struct {
		name       string
		hub, ref   []float64
		held, done bool
	}{
		{name: "the hub at 0.7% over a reference at 0.3% holds", hub: []float64{0.007}, ref: []float64{0.003}, held: true, done: true},
		{name: "the hub at 0.9% over a reference at 0.3% is over", hub: []float64{0.009}, ref: []float64{0.003}, held: false, done: false},
		{name: "a reference above the hub reads as no difference", hub: []float64{0.002}, ref: []float64{0.004}, held: true, done: true},
		{name: "a second window under budget holds", hub: []float64{0.009, 0.006}, ref: []float64{0.003, 0.003}, held: true, done: true},
		{name: "every window over fails once all are sampled", hub: []float64{0.009, 0.009, 0.009}, ref: []float64{0.003, 0.003, 0.003}, held: false, done: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			held, done := idleWindowVerdict(tt.hub, tt.ref, 0.005)
			if held != tt.held || done != tt.done {
				t.Errorf("idleWindowVerdict(%v, %v, 0.005) = %t, %t; want %t, %t",
					tt.hub, tt.ref, held, done, tt.held, tt.done)
			}
		})
	}
}

func TestIdleReferenceRunsInAnotherProcess(t *testing.T) {
	reference := startIdleReference(t, t.TempDir())
	if reference.pid <= 0 || reference.pid == os.Getpid() {
		t.Fatalf("the reference runs as pid %d, the test as %d: want another process", reference.pid, os.Getpid())
	}
	first := reference.cpuTime(t)
	if first <= 0 {
		t.Errorf("the reference reports %v of CPU time after starting: want a process that has run", first)
	}
	if second := reference.cpuTime(t); second < first {
		t.Errorf("the reference's CPU time went from %v to %v: want it to never decrease", first, second)
	}
}
