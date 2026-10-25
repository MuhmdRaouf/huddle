package isoenv

// The test plan: docs/TESTPLAN.md names, for every feature and every daily-use flow, the test
// that proves it. A test named there can be renamed or deleted like any other, and nothing else
// notices. This test reads the plan and requires every backticked `Test…` name and every
// backticked `"…"` web test title in it to exist, the way the incidents doc's guards must.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// planNamePattern matches a backticked Go test name, a backticked `path:TestName` and a
// backticked, double-quoted web test title anywhere in the plan.
var planNamePattern = regexp.MustCompile("`(?:[^`\"]*:)?(Test[A-Za-z0-9_]+)`|`(\"[^\"`]+\")`")

// planNames answers every test the plan names, in document order, each with the nearest `## `
// heading above it. Names inside one backtick span separated by commas count one by one.
func planNames(doc string) []guard {
	var names []guard
	heading := ""
	for line := range strings.SplitSeq(doc, "\n") {
		if h, ok := strings.CutPrefix(line, "## "); ok {
			heading = h
			continue
		}
		for _, m := range planNamePattern.FindAllStringSubmatch(line, -1) {
			if m[1] != "" {
				names = append(names, guard{name: m[1], heading: heading})
				continue
			}
			names = append(names, guard{name: m[2][1 : len(m[2])-1], heading: heading, title: true})
		}
	}
	return names
}

func TestPlanNames(t *testing.T) {
	doc := "# Test plan\n" +
		"\n" +
		"## Features\n" +
		"\n" +
		"| A | `cmd/huddle/a_test.go:TestOne`; `TestTwo` | words |\n" +
		"| B | `web/e2e/b.spec.ts` \"b holds\"; `\"quoted title\"` | words |\n"
	want := []guard{
		{"TestOne", "Features", false},
		{"TestTwo", "Features", false},
		{"quoted title", "Features", true},
	}
	got := planNames(doc)
	if len(got) != len(want) {
		t.Fatalf("planNames answered %d names, want %d: %+v", len(got), len(want), got)
	}
	for i, g := range want {
		if got[i] != g {
			t.Errorf("planNames[%d] = %+v, want %+v", i, got[i], g)
		}
	}
}

func TestTestplanNamesExistingTests(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(moduleRoot(t), "docs", "TESTPLAN.md"))
	if err != nil {
		t.Fatalf("read the test plan: %v", err)
	}
	names := planNames(string(data))
	if len(names) == 0 {
		t.Fatal("docs/TESTPLAN.md names no test: every feature and every flow names the test that proves it")
	}
	found := presentInModule(t, names)
	for _, g := range names {
		if found[g.name] {
			continue
		}
		if g.title {
			t.Errorf("%s names `\"%s\"` and no web test file in the module holds that title: the test was renamed or deleted; point the plan at the test that now proves it", g.heading, g.name)
			continue
		}
		t.Errorf("%s names `%s` and no *_test.go in the module holds func %s(: the test was renamed or deleted; point the plan at the test that now proves it", g.heading, g.name, g.name)
	}
}
