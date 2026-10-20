package main

// The leftover-process matcher over ps lines: only a *.test executable under the run's own temp
// build dirs, started after the run began, is the run's to answer — never another run's, never
// one that was already running, never a process ps cannot name.

import (
	"strings"
	"testing"
	"time"
)

func TestMatchLeftovers(t *testing.T) {
	const buildRoot = "/var/folders/qm/T/tmpguard-1/go-build"
	const maxElapsed = 5 * time.Minute
	rows := []struct {
		name string
		line string
		want bool
	}{
		{"a test binary of this run", "  123    00:04 " + buildRoot + "123/b001/huddle.test serve", true},
		{"one started after the run began, late in it", "  130    04:59 " + buildRoot + "7/b001/cli.test -test.timeout=10m -test.count=1", true},
		{"one started before the run began", "  124    05:01 " + buildRoot + "123/b001/huddle.test serve", false},
		{"another run's test binary", "  125    00:01 /var/folders/qm/T/go-build999/b001/other.test -test.timeout=10m", false},
		{"a built binary that is no test", "  126    00:01 " + buildRoot + "123/b001/huddle serve", false},
		{"a reaped process ps cannot name", "  127    00:01 (adapters.test)", false},
		{"a day-old daemon under the build root", "  128 2-03:04:05 " + buildRoot + "7/b001/plugins.test", false},
		{"a test binary somewhere else entirely", "  131    00:30 /usr/local/bin/x.test", false},
		{"a line without a command", "  132    00:30", false},
		{"an empty line", "", false},
	}
	for _, row := range rows {
		got := matchLeftovers([]string{row.line}, buildRoot, maxElapsed)
		if !row.want {
			if len(got) != 0 {
				t.Errorf("%s: matched %v, want nothing", row.name, got)
			}
			continue
		}
		if len(got) != 1 {
			t.Errorf("%s: matched %v, want the one pid", row.name, got)
			continue
		}
		if want := firstField(row.line); got[0].pid != want || got[0].command != afterPid(row.line) {
			t.Errorf("%s: matched (%s, %q), want (%s, %q)", row.name, got[0].pid, got[0].command, want, afterPid(row.line))
		}
	}
}

// firstField is a line's first whitespace-separated word, its pid.
func firstField(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// afterPid is the line past its pid and etime: the command line as ps printed it.
func afterPid(line string) string {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return ""
	}
	return strings.Join(fields[2:], " ")
}

func TestParseElapsed(t *testing.T) {
	rows := []struct {
		text string
		want time.Duration
		ok   bool
	}{
		{"04:59", 299 * time.Second, true},
		{"01:00:00", time.Hour, true},
		{"1-02:03:04", 26*time.Hour + 3*time.Minute + 4*time.Second, true},
		{"2-00:00:00", 48 * time.Hour, true},
		{"04", 0, false},
		{"01:02:03:04", 0, false},
		{"bogus", 0, false},
		{"", 0, false},
		{"-1:00", 0, false},
	}
	for _, row := range rows {
		got, ok := parseElapsed(row.text)
		if ok != row.ok || (ok && got != row.want) {
			t.Errorf("parseElapsed(%q) = (%v, %v), want (%v, %v)", row.text, got, ok, row.want, row.ok)
		}
	}
}
