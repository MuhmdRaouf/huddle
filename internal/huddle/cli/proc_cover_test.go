package cli

// The process lookups' quiet edges: nothing here needs a real socket table — a pid that names
// no process reads as nothing.

import (
	"testing"
)

// TestForceStopIsNeverNeededHere pins the platform's answer: a stop that outlives its ask is
// reported, never forced behind the user's back.
func TestForceStopIsNeverNeededHere(t *testing.T) {
	if forceStop(1) {
		t.Error("forceStop answered true")
	}
}

// TestProcLookupsReadNothingForAGoneProcess pins the real readers over a pid that names no
// process: no command line, no listening port.
func TestProcLookupsReadNothingForAGoneProcess(t *testing.T) {
	if line, ok := procCommand(-1); ok || line != "" {
		t.Errorf("procCommand(-1) = (%q, %v)", line, ok)
	}
	if port, ok := procListenOf(-1); ok || port != 0 {
		t.Errorf("procListenOf(-1) = (%d, %v)", port, ok)
	}
}
