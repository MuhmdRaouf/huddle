package isoenv

// The incidents doc: every incident the repo has been through is a written case in
// docs/INCIDENTS.md — what happened, the cause, the guard test that now stops it and the rule
// that follows. A guard named there can be renamed or deleted like any other test, and nothing
// else notices. This test reads the doc and requires every guard a **Guard:** bullet names to
// exist: a backticked `Test…` name as a func in a *_test.go file of the module, a backticked
// `"…"` title as that quoted string in a web test file (*.test.ts, *.test.tsx, *.spec.ts). A
// renamed or deleted guard fails here naming the incident it stood for.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// guard is one test a Guard bullet names, with the incident heading above it: a Go test's func
// name, or a web test's title when title is set.
type guard struct {
	name    string
	heading string
	title   bool
}

// guardNamePattern matches a backticked Go test name or a backticked, double-quoted web test
// title on a Guard bullet; the paths after the names are plain text and match nothing.
var guardNamePattern = regexp.MustCompile("`(Test[A-Za-z0-9_]+|\"[^\"`]+\")`")

// webTestSuffixes are the file names a web test title lives in.
var webTestSuffixes = []string{".test.ts", ".test.tsx", ".spec.ts"}

// guardNames answers every guard a **Guard:** bullet of the doc names, in document order, each
// with the nearest `## ` heading above it. The bullet runs on through its indented
// continuation lines. A test name in any other line — the intro, a What happened, a Rule —
// names no guard.
func guardNames(doc string) []guard {
	var guards []guard
	heading := ""
	inGuard := false
	for line := range strings.SplitSeq(doc, "\n") {
		if h, ok := strings.CutPrefix(line, "## "); ok {
			heading, inGuard = h, false
			continue
		}
		switch {
		case strings.Contains(line, "**Guard:**"):
			inGuard = true
		case !strings.HasPrefix(line, " ") || strings.TrimSpace(line) == "":
			inGuard = false
		}
		if !inGuard {
			continue
		}
		for _, m := range guardNamePattern.FindAllStringSubmatch(line, -1) {
			name, title := m[1], strings.HasPrefix(m[1], `"`)
			if title {
				name = name[1 : len(name)-1]
			}
			guards = append(guards, guard{name: name, heading: heading, title: title})
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
		"- **Guard:** `TestOne`, `TestTwo` (cmd/one_test.go),\n" +
		"  `TestWrapped` (cmd/wrapped_test.go)\n" +
		"- **Rule:** one rule.\n" +
		"\n" +
		"## 2026-10-21, the second incident\n" +
		"\n" +
		"- **Guard:** `TestThree` (internal/three_test.go)\n" +
		"\n" +
		"## 2026-10-22, the third incident\n" +
		"\n" +
		"- **Guard:** `\"reads once per channel\"` (web/hub/test/app/App.test.tsx)\n"
	want := []guard{
		{"TestOne", "2026-10-20, the first incident", false},
		{"TestTwo", "2026-10-20, the first incident", false},
		{"TestWrapped", "2026-10-20, the first incident", false},
		{"TestThree", "2026-10-21, the second incident", false},
		{"reads once per channel", "2026-10-22, the third incident", true},
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
		t.Fatal("docs/INCIDENTS.md holds no **Guard:** bullet naming a test: every incident names the test that stops it")
	}
	found := presentInModule(t, guards)
	for _, g := range guards {
		if !found[g.name] {
			if g.title {
				t.Errorf("%s names the guard `\"%s\"` and no web test file in the module holds that title: the guard was renamed or deleted; restore it or point the incident at the test that now stops it", g.heading, g.name)
				continue
			}
			t.Errorf("%s names the guard `%s` and no *_test.go in the module holds func %s(: the guard was renamed or deleted; restore it or point the incident at the test that now stops it", g.heading, g.name, g.name)
		}
	}
}

// presentInModule walks the module's test files once and answers which of the wanted tests
// exist: a Go name as `func Name(` in a *_test.go, a web title as that quoted string in a web
// test file. The worktrees and scratch under .agents hold other cards' copies, not this run's.
func presentInModule(t *testing.T, wants []guard) map[string]bool {
	t.Helper()
	found := make(map[string]bool, len(wants))
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
		goTest := strings.HasSuffix(path, "_test.go")
		webTest := slices.ContainsFunc(webTestSuffixes, func(suffix string) bool { return strings.HasSuffix(path, suffix) })
		if !goTest && !webTest {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, g := range wants {
			switch {
			case g.title && webTest && strings.Contains(string(data), `"`+g.name+`"`):
				found[g.name] = true
			case !g.title && goTest && strings.Contains(string(data), "func "+g.name+"("):
				found[g.name] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the module's tests: %v", err)
	}
	return found
}
