package isoenv

// The incidents doc: every incident the repo has been through is a written case in
// docs/INCIDENTS.md — what happened, the cause, the guard test that now stops it and the rule
// that follows. A guard named there can be renamed or deleted like any other test, and nothing
// else notices. This test reads the doc and requires every backticked `Test…` name on a
// **Guard:** bullet to exist in a *_test.go file of the module; a renamed or deleted guard
// fails here naming the incident it stood for.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// guard is one test name a Guard bullet names, with the incident heading above it.
type guard struct {
	name    string
	heading string
}

// guardNamePattern matches a backticked test name on a Guard bullet; the paths after the names
// are plain text and match nothing.
var guardNamePattern = regexp.MustCompile("`Test[A-Za-z0-9_]+`")

// guardNames answers every backticked `Test…` name on a **Guard:** bullet of the doc, in
// document order, each with the nearest `## ` heading above it. A test name in any other line
// — the intro, a What happened, a Rule — names no guard.
func guardNames(doc string) []guard {
	var guards []guard
	heading := ""
	for line := range strings.SplitSeq(doc, "\n") {
		if h, ok := strings.CutPrefix(line, "## "); ok {
			heading = h
			continue
		}
		if !strings.Contains(line, "**Guard:**") {
			continue
		}
		for _, m := range guardNamePattern.FindAllString(line, -1) {
			guards = append(guards, guard{name: m[1 : len(m)-1], heading: heading})
		}
	}
	return guards
}

func TestGuardNames(t *testing.T) {
	doc := "# Incidents\n" +
		"\n" +
		"An intro naming `TestNotOnAGuardLine`.\n" +
		"\n" +
		"## 2026-10-20, the first incident\n" +
		"\n" +
		"- **What happened:** something broke.\n" +
		"- **Guard:** `TestOne`, `TestTwo` (cmd/one_test.go)\n" +
		"- **Rule:** one rule.\n" +
		"\n" +
		"## 2026-10-21, the second incident\n" +
		"\n" +
		"- **Guard:** `TestThree` (internal/three_test.go)\n"
	want := []guard{
		{"TestOne", "2026-10-20, the first incident"},
		{"TestTwo", "2026-10-20, the first incident"},
		{"TestThree", "2026-10-21, the second incident"},
	}
	got := guardNames(doc)
	if len(got) != len(want) {
		t.Fatalf("guardNames answered %d names, want %d: %+v", len(got), len(want), got)
	}
	for i, g := range want {
		if got[i] != g {
			t.Errorf("guardNames[%d] = %+v, want %+v", i, got[i], g)
		}
	}
}

func TestIncidentGuardsExist(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(moduleRoot(t), "docs", "INCIDENTS.md"))
	if err != nil {
		t.Fatalf("read the incidents doc: %v", err)
	}
	guards := guardNames(string(data))
	if len(guards) == 0 {
		t.Fatal("docs/INCIDENTS.md holds no **Guard:** bullet with a `Test…` name: every incident names the test that stops it")
	}
	found := make(map[string]bool, len(guards))
	err = filepath.WalkDir(moduleRoot(t), func(path string, entry fs.DirEntry, err error) error {
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
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			for _, g := range guards {
				if strings.Contains(line, "func "+g.name+"(") {
					found[g.name] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the module's tests: %v", err)
	}
	for _, g := range guards {
		if !found[g.name] {
			t.Errorf("%s names the guard `%s` and no *_test.go in the module holds func %s(: the guard was renamed or deleted; restore it or point the incident at the test that now stops it", g.heading, g.name, g.name)
		}
	}
}
