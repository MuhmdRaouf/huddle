package server

import "testing"

// A race-built child server sleeps one second at exit unless told not to; the suite starts a
// server per test, so the sleep is paid once per test.
func TestAChildServerSkipsTheRaceDetectorsExitSleep(t *testing.T) {
	if got := childDefaults()["GORACE"]; got != "atexit_sleep_ms=0" {
		t.Errorf("GORACE = %q, want atexit_sleep_ms=0", got)
	}
}
