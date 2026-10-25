package isoenv

// The docs guard: a doc, or a code comment that points a reader at one, names things the code
// owns — the harnesses the registry wires, the workspaces web/ holds, the repositories and files
// a note cites — and nothing notices when the code moves on. These tests read those docs and hold
// each claim to the code it describes, so a line that drifted fails here naming the file and what
// is true now.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/harness"
)

// moduleFile answers the text of one file of the module, named by its slash path from the root.
func moduleFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(moduleRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// blocks splits a doc into the runs of lines that read as one thing: a paragraph, a table, a code
// fence, or one top-level list item with its wrapped lines. A block's lines are joined by single
// spaces, so a phrase the editor wrapped is one phrase again.
func blocks(doc string) []string {
	var found, lines []string
	flush := func() {
		if len(lines) > 0 {
			found = append(found, strings.Join(lines, " "))
			lines = nil
		}
	}
	for _, line := range strings.Split(doc, "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "- ") {
			flush()
		}
		if words := strings.Fields(line); len(words) > 0 {
			lines = append(lines, strings.Join(words, " "))
		}
	}
	flush()
	return found
}

// blockWith answers the first block of the doc that holds the anchor.
func blockWith(doc, anchor string) (string, bool) {
	for _, block := range blocks(doc) {
		if strings.Contains(block, anchor) {
			return block, true
		}
	}
	return "", false
}

func TestDocsSplitIntoBlocks(t *testing.T) {
	doc := "# Title\n" +
		"\n" +
		"A paragraph that the editor\n" +
		"  wrapped.\n" +
		"\n" +
		"- First item, which\n" +
		"  runs on.\n" +
		"- Second item.\n" +
		"| a | b |\n" +
		"|---|---|\n"
	want := []string{
		"# Title",
		"A paragraph that the editor wrapped.",
		"- First item, which runs on.",
		"- Second item. | a | b | |---|---|",
	}
	got := blocks(doc)
	if !slices.Equal(got, want) {
		t.Errorf("blocks = %q, want %q", got, want)
	}
	if block, ok := blockWith(doc, "editor wrapped"); !ok || block != want[1] {
		t.Errorf("blockWith(a phrase the editor wrapped) = %q, %v, want %q", block, ok, want[1])
	}
	if _, ok := blockWith(doc, "absent"); ok {
		t.Error("blockWith found a phrase the doc does not hold")
	}
}

// conflictMarker matches the lines git writes into a file it could not merge: the ours, base
// (diff3) and theirs markers, each followed by a space or nothing, and the lone divider.
// pre-commit's check-merge-conflict does not know the base marker and looks only while a merge
// runs, and markdownlint reads a marker as text, so a line left behind passes both.
var conflictMarker = regexp.MustCompile(`^(?:<{7}|\|{7}|>{7})(?: |$)|^={7}$`)

func TestDocsConflictMarkerPattern(t *testing.T) {
	cases := map[string]bool{
		"<<<<<<< HEAD": true,
		"||||||| parent of a1d3859 (test(repo): keep tests off the repository's own huddle.json)": true,
		"=======": true,
		">>>>>>> a1d3859 (test(repo): keep tests off the repository's own huddle.json)": true,
		"|||||||":         true,
		"||||||":          false,
		"<<<<<<<<":        false,
		"| a | b |":       false,
		"==== Title ====": false,
		"    ||||||| x":   false,
	}
	for line, want := range cases {
		if got := conflictMarker.MatchString(line); got != want {
			t.Errorf("conflictMarker on %q = %v, want %v", line, got, want)
		}
	}
}

func TestNoConflictMarkersInDocs(t *testing.T) {
	root := moduleRoot(t)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			// .agents holds the worktrees and scratch of other cards, whose docs are their own;
			// testdata holds byte-exact fixtures, which markdownlint skips as well.
			case ".git", ".agents", "node_modules", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".md") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		for n, line := range strings.Split(string(data), "\n") {
			if conflictMarker.MatchString(line) {
				t.Errorf("%s:%d is a merge marker (%q): finish resolving the conflict it came from", filepath.ToSlash(rel), n+1, line)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the module's docs: %v", err)
	}
}

// harnessLists are the places the docs spell out every harness the registry wires: the file, and
// words from the paragraph, row or code block that holds the list.
var harnessLists = []struct{ file, anchor string }{
	{"README.md", "Connect Codex"},
	{"README.md", "`codex` (Codex CLI)"},
	{"README.md", "belong to their owners"},
	{"plugin/README.md", "wiring other agents in"},
	{"plugin/README.md", "belong to their respective"},
	{"docs/USAGE.md", "huddle harness install codex"},
	{"docs/USAGE.md", "The names are exactly"},
}

func TestDocsNameEveryHarness(t *testing.T) {
	names := harness.Names()
	for _, list := range harnessLists {
		block, ok := blockWith(moduleFile(t, list.file), list.anchor)
		if !ok {
			t.Errorf("%s holds no paragraph with %q: the harness list moved or was reworded; point harnessLists at it", list.file, list.anchor)
			continue
		}
		for _, name := range names {
			if !regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(name) + `\b`).MatchString(block) {
				t.Errorf("%s: the paragraph with %q names no %s, and the registry wires %s: list every harness",
					list.file, list.anchor, name, strings.Join(names, ", "))
			}
		}
	}
}

// harnessCount matches the sentence that counts the harnesses the binary wires itself into.
var harnessCount = regexp.MustCompile(`For (\w+) of them the binary wires itself in`)

// countWords spells the counts a doc may write, indexed by the number.
var countWords = []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten"}

func TestDocsCountEveryHarness(t *testing.T) {
	wired := len(harness.Names())
	if wired >= len(countWords) {
		t.Fatalf("the registry wires %d harnesses and countWords stops at %d: extend it", wired, len(countWords)-1)
	}
	for _, file := range []string{"README.md", "docs/USAGE.md"} {
		block, ok := blockWith(moduleFile(t, file), "of them the binary wires itself in")
		if !ok {
			t.Errorf("%s has no sentence that counts the harnesses the binary wires in: point this guard at it", file)
			continue
		}
		match := harnessCount.FindStringSubmatch(block)
		if match == nil {
			t.Errorf("%s counts the harnesses in words this guard does not read (%q): match harnessCount to the sentence", file, block)
			continue
		}
		if match[1] != countWords[wired] {
			t.Errorf("%s says the binary wires itself into %s harnesses, and the registry wires %s: write \"For %s of them\"",
				file, match[1], countWords[wired], countWords[wired])
		}
	}
}

// codeSpan matches one inline code span and answers its text.
var codeSpan = regexp.MustCompile("`([^`]+)`")

// webWorkspaceList matches the two ways the docs list the web workspaces outside the layout: a
// brace list after web/ ("web/{ui,hub}") and the words "web workspaces" with a parenthesised list.
var webWorkspaceList = regexp.MustCompile(`web/\{([^}]*)\}|web workspaces \(([^)]*)\)`)

// requireSameNames fails when a doc's list of workspaces is not the set web/ holds.
func requireSameNames(t *testing.T, where string, listed, workspaces []string) {
	t.Helper()
	for _, name := range listed {
		if !slices.Contains(workspaces, name) {
			t.Errorf("%s names the workspace %s, and web/ holds no such workspace (it holds %s)", where, name, strings.Join(workspaces, ", "))
		}
	}
	for _, name := range workspaces {
		if !slices.Contains(listed, name) {
			t.Errorf("%s leaves out the workspace %s, which web/ holds (it has a package.json)", where, name)
		}
	}
}

func TestDocsListTheRealWebWorkspaces(t *testing.T) {
	root := moduleRoot(t)
	manifests, err := filepath.Glob(filepath.Join(root, "web", "*", "package.json"))
	if err != nil {
		t.Fatalf("list the web workspaces: %v", err)
	}
	var workspaces []string
	for _, manifest := range manifests {
		workspaces = append(workspaces, filepath.Base(filepath.Dir(manifest)))
	}
	slices.Sort(workspaces)

	bullet, ok := blockWith(moduleFile(t, "AGENTS.md"), "- `web/`:")
	if !ok {
		t.Fatal("AGENTS.md has no layout bullet that starts with `web/`: point this guard at the bullet that describes web/")
	}
	var named []string
	for _, match := range codeSpan.FindAllStringSubmatch(bullet, -1) {
		span := match[1]
		if !strings.Contains(span, "/") {
			named = append(named, span)
			continue
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(span))); err != nil {
			t.Errorf("AGENTS.md's web/ bullet names %s, and the repository holds no such path", span)
		}
	}
	requireSameNames(t, "AGENTS.md's web/ bullet", named, workspaces)

	for _, file := range []string{"AGENTS.md", "README.md"} {
		lists := webWorkspaceList.FindAllStringSubmatch(moduleFile(t, file), -1)
		if len(lists) == 0 {
			t.Errorf("%s lists the web workspaces nowhere (\"web/{…}\" or \"web workspaces (…)\"): point webWorkspaceList at the line that does", file)
		}
		for _, list := range lists {
			var listed []string
			for _, name := range strings.Split(list[1]+list[2], ",") {
				listed = append(listed, strings.TrimSpace(name))
			}
			requireSameNames(t, file+": "+list[0], listed, workspaces)
		}
	}
}

func TestDocsAgentsListsTheCoreScope(t *testing.T) {
	commits, ok := blockWith(moduleFile(t, "AGENTS.md"), "Scopes:")
	if !ok {
		t.Fatal("AGENTS.md lists no commit scopes (\"Scopes:\"): point this guard at the list")
	}
	_, scopes, _ := strings.Cut(commits, "Scopes:")
	if !strings.Contains(scopes, "`core`") {
		t.Error("AGENTS.md's commit scopes leave out `core`: internal/core, the provider engine, has its own commits, as fix(core) and test(core)")
	}
}
