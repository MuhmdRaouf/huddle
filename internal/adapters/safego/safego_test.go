package safego

import (
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
