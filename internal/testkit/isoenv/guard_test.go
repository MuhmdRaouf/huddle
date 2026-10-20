package isoenv

// The guard: one raw os.Environ() in a test is a child that inherits the owner's state — the
// owner's XDG_* dirs, huddle and radar homes, provider keys and proxies. Every test child
// environment is built here (Environ), and the child modes of a re-executed test binary read
// what their parent handed them through Inherited. A new raw call fails this test.

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildOnly lists the *_test.go files outside this package that may still call os.Environ(),
// each with the reason its call is not a child's environment: only a build's toolchain
// environment may keep the process's own variables, because it never runs huddle.
var buildOnly = map[string]string{
	"cmd/huddle/fresh_install_test.go":                    "freshBuildEnv pins the release go build's toolchain environment; it never runs huddle itself",
	"internal/core/testkit/fakeclaude/fakeclaude_test.go": "the broken-build subprocess only runs the fake's go build, and the toolchain keeps its own HOME; it never runs huddle",
}

func TestNoTestBuildsAChildEnvironmentOutsideIsoenv(t *testing.T) {
	seen := map[string]bool{}
	err := filepath.WalkDir(moduleRoot(t), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			// .agents holds the worktrees and scratch of other cards: their sources are this
			// module's copies, not this run's.
			case ".git", ".agents", "node_modules", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(moduleRoot(t), path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "internal/testkit/isoenv/") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(data), "os.Environ()") {
			return nil
		}
		if _, allowed := buildOnly[rel]; allowed {
			seen[rel] = true
			return nil
		}
		t.Errorf("%s calls os.Environ() directly: a child built from it inherits the owner's state; build it with isoenv.Environ, or read what the parent handed the child with isoenv.Inherited", rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk the module's tests: %v", err)
	}
	for path, reason := range buildOnly {
		if !seen[path] {
			t.Errorf("the build-only exception for %s is stale (%s): drop it from the allow list", path, reason)
		}
	}
}

// moduleRoot is the directory holding go.mod, found by walking up from the test's own.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("find the module root: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("find the module root: no go.mod above %s", dir)
		}
		dir = parent
	}
}
