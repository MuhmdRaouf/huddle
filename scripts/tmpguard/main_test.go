package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/core/testkit"
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

// TestARunWhoseTmpdirIsInsideAGitWorkTreeIsRefused: the suite's "outside a repository" cases and any
// git write a test aims at its own folder would reach the repository around a TMPDIR that points
// into a checkout.
func TestARunWhoseTmpdirIsInsideAGitWorkTreeIsRefused(t *testing.T) {
	testkit.IsolateGitConfig(t)
	repo := testkit.MakeRepo(t)
	inside := filepath.Join(repo.Dir, "scratch", "tmp")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", inside)

	var stdout, stderr bytes.Buffer
	code := run([]string{"-count=1", "-run", "^TestStartsNothing$", spawner}, &stdout, &stderr)

	if code != 1 {
		t.Errorf("exit code = %d, want 1\nstdout: %s\nstderr: %s", code, &stdout, &stderr)
	}
	for _, want := range []string{"TMPDIR", repo.Dir, "git work tree"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr = %q, want it to name %q", &stderr, want)
		}
	}
	if stdout.Len() != 0 {
		t.Errorf("the suite ran (stdout %q), want it refused before any test", &stdout)
	}
}

func TestARunThatStartsNoNotifierPasses(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-count=1", "-run", "^TestStartsNothing$", spawner}, &stdout, &stderr)
	if code != 0 {
		t.Errorf("exit code = %d, want 0\nstdout: %s\nstderr: %s", code, &stdout, &stderr)
	}
}

// childPID reads the pid the leaker fixture logs.
var childPID = regexp.MustCompile(`leaker child pid (\d+)`)

// TestTheGuardNamesAChildThatOutlivesASandboxedSuite runs a sandboxed suite whose test leaves a
// child running from a binary under a temp dir of its own: the guard fails the run and names the child,
// because the suite's temp dirs sit under the guard's own TMPDIR.
func TestTheGuardNamesAChildThatOutlivesASandboxedSuite(t *testing.T) {
	var stdout, stderr bytes.Buffer
	t.Cleanup(func() { // the child is this test's own: the fixture logged its pid
		if found := childPID.FindStringSubmatch(stdout.String()); found != nil {
			if pid, err := strconv.Atoi(found[1]); err == nil && pid > 1 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	code := run([]string{"-count=1", "-v", "./testdata/leaker"}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("tmpguard exited %d, want 1 for a suite that left a child running\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "a process of this run outlived it") {
		t.Errorf("tmpguard's stderr does not name the child:\n%s", stderr.String())
	}
}
