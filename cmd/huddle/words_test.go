package main

// The satellite words: `huddle radar` and `huddle kanban` run radar's and kanban's command
// lines in this binary, before any huddle word — neither touches a huddle home.

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestRadarWordRunsRadar(t *testing.T) {
	t.Setenv("RADAR_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := run([]string{"radar", "--version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(radar --version) = %d, stderr %q", code, stderr.String())
	}
	if want := fmt.Sprintf("radar %s\n", version); stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestKanbanWordRunsKanban(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"kanban", "--version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(kanban --version) = %d, stderr %q", code, stderr.String())
	}
	if want := fmt.Sprintf("kanban %s\n", version); stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

// TestRadarWordAnswersWithoutAHuddleHome pins the word's reach: a radar invocation answers on
// an environment with no huddle home and no server to reach.
func TestRadarWordAnswersWithoutAHuddleHome(t *testing.T) {
	t.Setenv("HUDDLE_URL", "")
	t.Setenv("HUDDLE_CHANNEL", "")
	t.Setenv("HUDDLE_AS", "")
	t.Setenv("HUDDLE_TOKEN", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("RADAR_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := run([]string{"radar", "--version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(radar --version) = %d, stderr %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "radar "+version) {
		t.Errorf("stdout = %q, want the radar version line", stdout.String())
	}
}
