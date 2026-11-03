package safego

import (
	"bufio"
	"os"
	"strings"
	"testing"
	"time"
)

func TestGoRunsTheFunction(t *testing.T) {
	done := make(chan struct{})
	Go(nil, func() { close(done) })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("fn did not run")
	}
}

func TestGoRecoversAPanicAndWarnsOnce(t *testing.T) {
	lines := make(chan string, 4)
	Go(func(line string) { lines <- line }, func() { panic("boom") })
	select {
	case line := <-lines:
		if !strings.Contains(line, "panic: boom") {
			t.Errorf("warn received %q, want it to contain %q", line, "panic: boom")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("warn was not called")
	}
	select {
	case line := <-lines:
		t.Errorf("warn was called twice: second line %q", line)
	case <-time.After(100 * time.Millisecond):
	}
	// the panic ended only its own goroutine: this one still runs
	done := make(chan struct{})
	Go(nil, func() { close(done) })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Go stopped starting goroutines after a recovered panic")
	}
}

func TestAPanicWithNoWarnFunctionIsWrittenToStderr(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	// The goroutine reads os.Stderr before it writes the line, and the read of that line below
	// comes after the write, so the restore cannot overlap the goroutine's use of the variable.
	t.Cleanup(func() {
		os.Stderr = orig
		w.Close()
		r.Close()
	})

	Go(nil, func() { panic("boom") })

	first, err := bufio.NewReader(r).ReadString('\n')
	if err != nil {
		t.Fatalf("read the captured stderr: %v", err)
	}
	if first != "panic: boom\n" {
		t.Errorf("the first stderr line = %q, want %q", first, "panic: boom\n")
	}
}

func TestAWarnLineKeepsOnlyTheStacksFirstLines(t *testing.T) {
	var stack []string
	for i := 1; i <= warnStackLines+8; i++ {
		stack = append(stack, "frame "+strings.Repeat("x", i))
	}

	got := strings.Split(warnStack([]byte(strings.Join(stack, "\n")+"\n")), "\n")
	if len(got) != warnStackLines {
		t.Fatalf("warnStack kept %d lines, want %d", len(got), warnStackLines)
	}
	if got[0] != stack[0] || got[len(got)-1] != stack[warnStackLines-1] {
		t.Errorf("warnStack kept %q ... %q, want the first %d lines", got[0], got[len(got)-1], warnStackLines)
	}
}

func TestAShortStackIsKeptWhole(t *testing.T) {
	in := "goroutine 7 [running]:\nmain.f()\n"
	if got, want := warnStack([]byte(in)), "goroutine 7 [running]:\nmain.f()"; got != want {
		t.Errorf("warnStack = %q, want %q", got, want)
	}
}
