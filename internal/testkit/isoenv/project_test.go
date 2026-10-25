package isoenv

// The project guard: huddle's hooks resolve their identity from CLAUDE_PROJECT_DIR, else from
// the cwd upward to the nearest .agents/huddle/huddle.json. A test's cwd is its package
// directory inside this repository, and the repository keeps the owner's own huddle.json, so a
// child that walks from its cwd joins the owner's channel and, with autostart, brings up a hub
// in the reserved port walk. A child built by Environ has a project dir of its own.

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestAHookRunFromInsideARepositoryWithAHuddleJsonReadsNoIdentity(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "huddle")
	build := exec.Command("go", "build", "-o", bin, "./cmd/huddle")
	build.Dir = moduleRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the huddle binary: %v\n%s", err, out)
	}

	repo := t.TempDir()
	settings := filepath.Join(repo, ".agents", "huddle", "huddle.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatalf("make the repository's huddle dir: %v", err)
	}
	if err := os.WriteFile(settings, []byte(`{"channel":"x","as":"y","autostart":true}`), 0o600); err != nil {
		t.Fatalf("write the repository's huddle.json: %v", err)
	}
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatalf("make the package dir: %v", err)
	}

	home := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "hook", "session-start")
	cmd.Dir = sub
	// Autostart off: a hook with no identity still starts a hub by design, and this guard is about
	// the identity alone; it leaves no process behind and no port of the 18860-18869 walk open.
	cmd.Env = Environ(t, home, "HUDDLE_AUTOSTART=0")
	cmd.Stdin = bytes.NewReader([]byte(`{}`))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("hook session-start: %v (stdout %q, stderr %q)", err, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("hook session-start printed %q: it read the identity of the repository around its cwd", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "huddle", "run", "huddle.pid")); err == nil {
		t.Error("the hook started a hub under the test's home: it holds a port of the 18860-18869 walk")
	}
}
