package main

import (
	"bytes"
	"strings"
	"testing"
)

// spawner is the fixture package: a test whose child starts a notifier, and one that does not.
const spawner = "github.com/MuhmdRaouf/huddle/scripts/tmpguard/testdata/spawner"

func TestARunWhoseChildStartsANotifierFailsNamingTheSpawn(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-count=1", "-run", "^TestStartsANotifier$", spawner}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit code = %d, want 1\nstdout: %s\nstderr: %s", code, &stdout, &stderr)
	}
	if !strings.Contains(stderr.String(), "notify-send Huddle blocked") {
		t.Errorf("stderr = %q, want it to name the recorded spawn", &stderr)
	}
}

func TestARunThatStartsNoNotifierPasses(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-count=1", "-run", "^TestStartsNothing$", spawner}, &stdout, &stderr)
	if code != 0 {
		t.Errorf("exit code = %d, want 0\nstdout: %s\nstderr: %s", code, &stdout, &stderr)
	}
}
