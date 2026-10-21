package process

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

func TestPickLive(t *testing.T) {
	live := func(path string, ownName bool) liveCandidate {
		return liveCandidate{path: path, regular: true, executable: true, ownName: ownName}
	}
	cases := []struct {
		name  string
		cands []liveCandidate
		want  string
	}{
		{
			name: "the launcher home's current wins over the rest",
			cands: []liveCandidate{
				live("/h/src/bb/huddle", false),
				live("/h/src/aa/huddle", true),
				live("/old/huddle", false),
			},
			want: "/h/src/bb/huddle",
		},
		{
			name: "a missing current falls to the next spot",
			cands: []liveCandidate{
				{path: "/h/current", err: errors.New("no such file or directory")},
				live("/h/src/bb/huddle", true),
				live("/old/huddle", false),
			},
			want: "/h/src/bb/huddle",
		},
		{
			name: "a current to a directory is skipped",
			cands: []liveCandidate{
				{path: "/h/src/bb", executable: true},
				live("/old/huddle", false),
			},
			want: "/old/huddle",
		},
		{
			name: "a current to a plain file is skipped",
			cands: []liveCandidate{
				{path: "/h/notes.txt", regular: true},
				live("/old/huddle", false),
			},
			want: "/old/huddle",
		},
		{
			name: "a current beside the build naming another tool is skipped",
			cands: []liveCandidate{
				live("/h/bin/other", true),
				live("/h/src/cc/huddle", true),
				live("/old/huddle", false),
			},
			want: "/h/src/cc/huddle",
		},
		{
			name:  "the own build answers with no current anywhere",
			cands: []liveCandidate{live("/h/src/aa/huddle", true), live("/h/src/aa/huddle", false)},
			want:  "/h/src/aa/huddle",
		},
		{
			name: "nothing on disk answers nothing",
			cands: []liveCandidate{
				{path: "/h/current", err: errors.New("no such file or directory")},
				{path: "/h/src/aa/huddle", err: errors.New("no such file or directory"), ownName: true},
				{path: "/gone/huddle", err: errors.New("no such file or directory")},
			},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickLive(tc.cands); got != tc.want {
				t.Fatalf("pickLive = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLiveExecutable(t *testing.T) {
	// A launcher home with one build deleted and one alive, `current` naming the alive one:
	// the shape of a session that outlived its own build.
	home := t.TempDir()
	alive := filepath.Join(home, "src", "bbbb", "huddle")
	if err := os.MkdirAll(filepath.Dir(alive), 0o700); err != nil {
		t.Fatal(err)
	}
	test, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(test)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(alive, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("src/bbbb/huddle", filepath.Join(home, "current")); err != nil {
		t.Fatal(err)
	}
	// macOS keeps t.TempDir() behind /var → /private/var, and EvalSymlinks resolves the whole path.
	alive, err = filepath.EvalSymlinks(alive)
	if err != nil {
		t.Fatal(err)
	}
	deleted := filepath.Join(home, "src", "aaaa", "huddle")
	selfAt := func(path string) func() (string, error) {
		return func() (string, error) { return path, nil }
	}

	t.Run("the env home's current answers with the alive build", func(t *testing.T) {
		got := liveExecutable(domain.EnvLookup{"AGENTS_HUDDLE_HOME": home}, selfAt(deleted))
		if got != alive {
			t.Fatalf("liveExecutable = %q, want %q", got, alive)
		}
	})
	t.Run("the current beside the build answers without the env", func(t *testing.T) {
		got := liveExecutable(domain.EnvLookup{}, selfAt(deleted))
		if got != alive {
			t.Fatalf("liveExecutable = %q, want %q", got, alive)
		}
	})
	t.Run("the current at the build's parent answers", func(t *testing.T) {
		shallow := t.TempDir()
		bump := filepath.Join(shallow, "0.0.2", "huddle")
		if err := os.MkdirAll(filepath.Dir(bump), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bump, raw, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("0.0.2/huddle", filepath.Join(shallow, "current")); err != nil {
			t.Fatal(err)
		}
		want, err := filepath.EvalSymlinks(bump)
		if err != nil {
			t.Fatal(err)
		}
		got := liveExecutable(domain.EnvLookup{}, selfAt(filepath.Join(shallow, "0.0.1", "huddle")))
		if got != want {
			t.Fatalf("liveExecutable = %q, want %q", got, want)
		}
	})
	t.Run("the current wins over the surviving own build", func(t *testing.T) {
		if err := os.MkdirAll(filepath.Dir(deleted), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(deleted, raw, 0o755); err != nil {
			t.Fatal(err)
		}
		got := liveExecutable(domain.EnvLookup{"AGENTS_HUDDLE_HOME": home}, selfAt(deleted))
		if got != alive {
			t.Fatalf("liveExecutable = %q, want %q", got, alive)
		}
	})
	t.Run("the own build answers when no current exists", func(t *testing.T) {
		bare := t.TempDir()
		own := filepath.Join(bare, "src", "aaaa", "huddle")
		if err := os.MkdirAll(filepath.Dir(own), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(own, raw, 0o755); err != nil {
			t.Fatal(err)
		}
		got := liveExecutable(domain.EnvLookup{}, selfAt(own))
		if got != own {
			t.Fatalf("liveExecutable = %q, want %q", got, own)
		}
	})
	t.Run("a deleted executable with no current falls to os.Args[0]", func(t *testing.T) {
		bare := t.TempDir()
		got := liveExecutable(domain.EnvLookup{}, selfAt(filepath.Join(bare, "src", "aaaa", "huddle")))
		if got != os.Args[0] {
			t.Fatalf("liveExecutable = %q, want %q", got, os.Args[0])
		}
	})
	t.Run("an executable that cannot be found falls to os.Args[0]", func(t *testing.T) {
		got := liveExecutable(domain.EnvLookup{}, func() (string, error) {
			return "", errors.New("not found")
		})
		if got != os.Args[0] {
			t.Fatalf("liveExecutable = %q, want %q", got, os.Args[0])
		}
	})
}

// TestLiveExecutableOnThisTestBinary pins the production entry point: the test's own binary
// exists, so with no launcher home around it is the answer.
func TestLiveExecutableOnThisTestBinary(t *testing.T) {
	got := LiveExecutable(domain.EnvLookup{"AGENTS_HUDDLE_HOME": ""})
	want, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("LiveExecutable = %q, want %q", got, want)
	}
}
