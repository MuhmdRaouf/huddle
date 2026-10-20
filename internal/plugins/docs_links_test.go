package plugins

// Every relative link in a committed markdown file must name a file that exists. The docs move
// between repositories and plugin directories; a link left pointing at the old layout renders as
// a broken page on GitHub and nowhere else, so this walks every `*.md` outside `.agents/` and
// `node_modules/` and stats each `](path)` target that is not a URL or a section of the same
// page.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// linkPattern is a markdown inline link's target: `](path)`, optionally wrapped in angle
// brackets when the path carries spaces.
var linkPattern = regexp.MustCompile(`\]\(([^)\r\n]+)\)`)

// TestDocsLinksResolve fails on the first relative link whose file is missing, naming file and
// line, so a moved document takes its links with it.
func TestDocsLinksResolve(t *testing.T) {
	root := filepath.Join("..", "..")
	var failures []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".agents", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".md") {
			return nil
		}
		failures = append(failures, brokenLinks(t, path)...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(failures) > 0 {
		t.Fatalf("%d markdown link(s) point at missing files:\n%s",
			len(failures), strings.Join(failures, "\n"))
	}
}

// brokenLinks stats one file's relative link targets; each miss is one named failure.
func brokenLinks(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	dir := filepath.Dir(path)
	var failures []string
	for number, line := range strings.Split(string(raw), "\n") {
		for _, match := range linkPattern.FindAllStringSubmatch(line, -1) {
			target := strings.TrimSuffix(strings.TrimPrefix(match[1], "<"), ">")
			if target == "" || strings.HasPrefix(target, "#") ||
				strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			// the section after # names a heading of the linked page, not its file name
			if cut := strings.Index(target, "#"); cut >= 0 {
				target = target[:cut]
			}
			if target == "" {
				continue
			}
			resolved := filepath.Join(dir, target)
			if _, err := os.Stat(resolved); err != nil {
				failures = append(failures, path+":"+strconv.Itoa(number+1)+": "+match[0])
			}
		}
	}
	return failures
}
