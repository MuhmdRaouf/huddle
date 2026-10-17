package hooks

// The report hook's own pieces, unit-proven: the ps readers the session walk uses, the machine
// name the config holds, the environment a child inherits, the outbox writers' quiet failures
// and the one JSON answer a hook prints.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
)

// coverWorld answers a world whose variables are exactly vars, the way the suite's hook runs
// build theirs.
func coverWorld(vars map[string]string) *cli.World {
	return &cli.World{
		Getenv: func(key string) string { return vars[key] },
		Now:    func() time.Time { return time.Now() },
	}
}

// sameEnv answers whether two variable lists name the same entries.
func sameEnv(a, b []string) bool {
	left := append([]string{}, a...)
	right := append([]string{}, b...)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}

// TestPsParentOfAnswersThisProcesssParent: the ps reader answers this process's own parent.
func TestPsParentOfAnswersThisProcesssParent(t *testing.T) {
	parent, ok := psParentOf(context.Background())(os.Getpid())
	if !ok || parent != os.Getppid() {
		t.Fatalf("psParentOf(%d) = (%d, %v), want (%d, true)", os.Getpid(), parent, ok, os.Getppid())
	}
}

// TestPsParentOfAnswersNoneForAPidPsRejects: a pid ps cannot answer, and a walk whose budget is
// already spent, both read as no parent — never a guess.
func TestPsParentOfAnswersNoneForAPidPsRejects(t *testing.T) {
	if parent, ok := psParentOf(context.Background())(-1); ok || parent != 0 {
		t.Errorf("psParentOf(-1) = (%d, %v), want (0, false)", parent, ok)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if parent, ok := psParentOf(ctx)(os.Getpid()); ok || parent != 0 {
		t.Errorf("psParentOf on a cancelled context = (%d, %v), want (0, false)", parent, ok)
	}
}

// TestMachineOfAnswersEmptyWithoutAHomeOrWithABrokenConfig: with no home to read, and with a
// config the loader cannot read, the report carries no machine name; a readable config answers
// the name it sets.
func TestMachineOfAnswersEmptyWithoutAHomeOrWithABrokenConfig(t *testing.T) {
	ctx := context.Background()
	if got := machineOf(ctx, coverWorld(map[string]string{})); got != "" {
		t.Errorf("machineOf without a home = %q, want empty", got)
	}
	broken := t.TempDir()
	if err := os.Mkdir(filepath.Join(broken, "config.toml"), 0o700); err != nil {
		t.Fatalf("break the config: %v", err)
	}
	if got := machineOf(ctx, coverWorld(map[string]string{"HUDDLE_HOME": broken})); got != "" {
		t.Errorf("machineOf with an unreadable config = %q, want empty", got)
	}
	named := t.TempDir()
	if err := os.WriteFile(filepath.Join(named, "config.toml"), []byte("machine = \"mbp\"\n"), 0o600); err != nil {
		t.Fatalf("write the config: %v", err)
	}
	if got := machineOf(ctx, coverWorld(map[string]string{"HUDDLE_HOME": named})); got != "mbp" {
		t.Errorf("machineOf = %q, want the config's mbp", got)
	}
}

// TestEnvironOfFallsBackToTheProcessEnvironment: a world without its own list — or no world —
// hands the child the process's variables; a world with one hands the child its own.
func TestEnvironOfFallsBackToTheProcessEnvironment(t *testing.T) {
	if got := environOf(nil); !sameEnv(got, os.Environ()) {
		t.Errorf("environOf(nil) = %v, want the process's environment", got)
	}
	if got := environOf(&cli.World{}); !sameEnv(got, os.Environ()) {
		t.Errorf("environOf of an empty world = %v, want the process's environment", got)
	}
	w := &cli.World{Environ: func() []string { return []string{"HOOKS_TEST=1"} }}
	if got := environOf(w); !slices.Equal(got, []string{"HOOKS_TEST=1"}) {
		t.Errorf("environOf of a world with a list = %v, want its own list", got)
	}
}

// TestTheOutboxWritersGiveUpQuietlyOnAnUnwritablePath: a path the writers cannot use — none at
// all, one inside a plain file, one whose temp name a directory took — leaves the disk alone and
// raises nothing.
func TestTheOutboxWritersGiveUpQuietlyOnAnUnwritablePath(t *testing.T) {
	ctx := context.Background()
	appendOutbox(ctx, nil, "", "x")
	writeOutbox("", nil) // neither writes anything anywhere

	root := t.TempDir()
	plain := filepath.Join(root, "file")
	if err := os.WriteFile(plain, []byte("plain"), 0o600); err != nil {
		t.Fatalf("write the plain file: %v", err)
	}
	blocked := filepath.Join(plain, "outbox.jsonl")
	appendOutbox(ctx, nil, blocked, "x")
	writeOutbox(blocked, []string{"x"})
	if raw, err := os.ReadFile(plain); err != nil || string(raw) != "plain" {
		t.Errorf("the plain file changed: %v (%q), want it untouched with nothing created inside", err, raw)
	}

	taken := filepath.Join(root, "outbox.jsonl")
	if err := os.WriteFile(taken, []byte("old"), 0o600); err != nil {
		t.Fatalf("write the outbox: %v", err)
	}
	if err := os.Mkdir(taken+".tmp", 0o700); err != nil {
		t.Fatalf("take the temp name: %v", err)
	}
	writeOutbox(taken, []string{"new"})
	if raw, err := os.ReadFile(taken); err != nil || string(raw) != "old" {
		t.Errorf("the outbox changed: %v (%q), want it unchanged", err, raw)
	}
}

// TestWriteJSONWritesNothingForAValueItCannotEncode: a value JSON cannot hold leaves the answer
// empty — a hook prints nothing rather than a broken line.
func TestWriteJSONWritesNothingForAValueItCannotEncode(t *testing.T) {
	buf := &bytes.Buffer{}
	writeJSON(buf, make(chan int))
	if buf.Len() != 0 {
		t.Errorf("writeJSON wrote %q, want nothing", buf.String())
	}
}
