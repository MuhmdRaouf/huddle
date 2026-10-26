// Read-only access to a channel's repo (channel config `repo`): paths inside it (never anything
// named secrets), one directory level with its README, a whole file for the code view, and the
// drift check that says whether each quoted snippet still matches its file.
package server

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// repoSkip names the entries a directory level never lists.
var repoSkip = map[string]bool{
	".git": true, ".terraform": true, "node_modules": true, ".direnv": true, "result": true, ".agents": true,
}

// secretsRe names a secrets directory, at any depth.
var secretsRe = regexp.MustCompile(`(^|/)secrets(/|$)`)

// relRefusedFor says rel can never name a repo file: any "..", and a path that names from the
// root, which a join would graft onto the repo instead of refusing.
func relRefusedFor(rel string) bool {
	if strings.Contains(rel, "..") {
		return true
	}
	return strings.HasPrefix(rel, "/")
}

// RepoTools reads one repo.
type RepoTools struct {
	repo    string
	real    string
	scratch string
}

// NewRepoTools answers the tools for a repo that exists, nil for a path that does not.
func NewRepoTools(repo string) *RepoTools {
	if _, err := os.Stat(repo); err != nil {
		return nil
	}
	real, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return nil
	}
	// the scratch tree is a symlink out of the worktree; quotes from it are allowed too
	scratch := ""
	if probe := filepath.Join(repo, ".agents", "scratch"); pathExists(probe) {
		if resolved, err := filepath.EvalSymlinks(probe); err == nil {
			scratch = resolved
		}
	}
	return &RepoTools{repo: repo, real: real, scratch: scratch}
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// Safe answers the path inside the repo (or its .agents/scratch tree), "" when it leaves either
// or is anything named secrets. rel is a repo-relative path the dashboard writes with "/", so it
// is spelled into the OS's separator before it touches the disk; the resolved path comes back in
// the OS's own spelling, so both sides are compared slashed.
func (r *RepoTools) Safe(rel string) string {
	if relRefusedFor(rel) {
		return ""
	}
	if secretsRe.MatchString(rel) {
		return ""
	}
	full := r.repo
	if rel != "" {
		full = filepath.Join(r.repo, filepath.FromSlash(rel))
	}
	if !pathExists(full) && r.scratch != "" && rel != "" {
		alt := filepath.Join(r.scratch, filepath.FromSlash(rel))
		if pathExists(alt) {
			full = alt
		}
	}
	if !pathExists(full) {
		return ""
	}
	rp, err := filepath.EvalSymlinks(full)
	if err != nil {
		return ""
	}
	rp = filepath.ToSlash(rp)
	if secretsRe.MatchString(rp) {
		return ""
	}
	within := func(root string) bool {
		root = filepath.ToSlash(root)
		return rp == root || strings.HasPrefix(rp, root+"/")
	}
	if within(r.real) || (r.scratch != "" && within(r.scratch)) {
		return full
	}
	return ""
}

// TreeEntry is one entry of a directory level.
type TreeEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size"`
}

// TreeAnswer is one directory level, with its README.
type TreeAnswer struct {
	Path    string      `json:"path"`
	Entries []TreeEntry `json:"entries"`
	Readme  *string     `json:"readme"`
}

// TreeFile says a tree path names a file.
type TreeFile struct {
	File bool   `json:"file"`
	Path string `json:"path"`
}

// NotFoundAnswer is the one-word miss every view shares.
type NotFoundAnswer struct {
	Error string `json:"error"`
}

// Tree answers one directory level, directories first, both sides in the collation order the
// dashboard's table uses.
func (r *RepoTools) Tree(ctx context.Context, rel string) (any, error) {
	full := r.Safe(rel)
	if full == "" {
		return NotFoundAnswer{Error: "not found"}, nil
	}
	if !isDir(full) {
		return TreeFile{File: true, Path: rel}, nil
	}
	names, err := os.ReadDir(full)
	if err != nil {
		return NotFoundAnswer{Error: "not found"}, nil
	}
	kept := []string{}
	for _, e := range names {
		name := e.Name()
		if repoSkip[name] || strings.HasPrefix(name, ".DS_Store") {
			continue
		}
		child := name
		if rel != "" {
			child = rel + "/" + name
		}
		if r.Safe(child) == "" {
			continue
		}
		kept = append(kept, name)
	}
	sort.SliceStable(kept, func(i, j int) bool {
		di, dj := isDir(filepath.Join(full, kept[i])), isDir(filepath.Join(full, kept[j]))
		if di != dj {
			return di
		}
		return jsscalar.LocaleCompare(kept[i], kept[j]) < 0
	})
	entries := make([]TreeEntry, 0, len(kept))
	for _, name := range kept {
		info, err := os.Stat(filepath.Join(full, name))
		if err != nil {
			continue
		}
		path := name
		if rel != "" {
			path = rel + "/" + name
		}
		entries = append(entries, TreeEntry{Name: name, Path: path, Dir: info.IsDir(), Size: info.Size()})
	}
	var readme *string
	for _, e := range entries {
		if strings.EqualFold(e.Name, "README.md") {
			if body, err := os.ReadFile(filepath.Join(full, e.Name)); err == nil {
				text := string(body)
				readme = &text
			}
			break
		}
	}
	return TreeAnswer{Path: rel, Entries: entries, Readme: readme}, nil
}

// FileAnswer is a whole file (capped) for the code view.
type FileAnswer struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	Text string `json:"text"`
}

// FileBinary says a file is not text.
type FileBinary struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Binary bool   `json:"binary"`
}

// File answers a whole file, capped at two megabytes.
func (r *RepoTools) File(ctx context.Context, rel string) (any, error) {
	full := r.Safe(rel)
	if full == "" || isDir(full) {
		return NotFoundAnswer{Error: "not a file"}, nil
	}
	info, err := os.Stat(full)
	if err != nil {
		return NotFoundAnswer{Error: "not a file"}, nil
	}
	if info.Size() > 2_000_000 {
		return FileAnswer{Path: rel, Size: info.Size(), Text: "(file larger than 2 MB)"}, nil
	}
	body, err := os.ReadFile(full)
	if err != nil {
		return NotFoundAnswer{Error: "not a file"}, nil
	}
	text := string(body)
	head := text
	if jsscalar.UTF16Len(head) > 2000 {
		head = jsscalar.UTF16Slice(head, 0, 2000)
	}
	if strings.ContainsRune(head, 0) {
		return FileBinary{Path: rel, Size: info.Size(), Binary: true}, nil
	}
	return FileAnswer{Path: rel, Size: info.Size(), Text: text}, nil
}

// DriftRow is one snippet's verdict.
type DriftRow struct {
	Step  string `json:"step"`
	I     int    `json:"i"`
	Path  string `json:"path"`
	State string `json:"state"`
	Score *int   `json:"score,omitempty"`
}

// lineRangeRe strips the line range a quoted path may carry ("src/x.go:12-30").
var lineRangeRe = regexp.MustCompile(`:\d+(-\d+)?$`)

// spaceRuns matches runs of whitespace the way /\s+/ does, JavaScript's set.
var spaceRuns = regexp.MustCompile(jsscalar.SpaceClass + `+`)

// norm flattens one line the way the drift check reads it: any whitespace run one space, the
// ends trimmed.
func norm(line string) string {
	return strings.Trim(spaceRuns.ReplaceAllString(line, " "), " ")
}

// ellipsisEnds strips a leading "… " and a trailing " …", the markers a hand-written quote uses.
func ellipsisEnds(line string) string {
	line = strings.TrimPrefix(line, "…")
	line = strings.TrimSuffix(line, "…")
	// the markers may carry spaces on their inside edge
	line = strings.Trim(line, " ")
	return line
}

// skippedQuote says a line is one of the placeholders that carry no content.
func skippedQuote(line string) bool {
	if line == "…" || line == "..." {
		return true
	}
	rest := strings.TrimPrefix(line, "#")
	if rest == line {
		rest = strings.TrimPrefix(line, "//")
	}
	return jsscalar.Trim(rest) == "…"
}

// truthy reads a JSON value the way JavaScript's if does.
func truthy(v *jsfmt.Value) bool {
	if v == nil || v.Kind() == jsfmt.KindNull {
		return false
	}
	if v.Kind() == jsfmt.KindBool {
		return v.EncodeJSON() == "true"
	}
	if v.Kind() == jsfmt.KindNumber {
		return v.EncodeJSON() != "0"
	}
	if v.Kind() == jsfmt.KindString {
		return v.Text() != ""
	}
	return true
}

func fieldOf(v *jsfmt.Value, key string) *jsfmt.Value {
	if v == nil || v.Kind() != jsfmt.KindObject {
		return nil
	}
	for i, k := range v.Keys() {
		if k == key {
			return v.Fields()[i]
		}
	}
	return nil
}

func textOf(v *jsfmt.Value) string {
	if v == nil {
		return ""
	}
	if v.Kind() == jsfmt.KindString {
		return v.Text()
	}
	if v.Kind() == jsfmt.KindNumber || v.Kind() == jsfmt.KindBool {
		return v.EncodeJSON()
	}
	return ""
}

// Drift answers whether each non-proposed snippet still matches the file it quotes: a snippet
// matches when most of its meaningful lines still occur in the file, wrapped Markdown and long
// XML lines included.
func (r *RepoTools) Drift(ctx context.Context, steps []*channel.TaskView) ([]DriftRow, error) {
	out := []DriftRow{}
	cache := map[string]*string{}
	for _, s := range steps {
		snippets := s.JSON("snippets")
		if snippets == nil || snippets.Kind() != jsfmt.KindArray {
			continue
		}
		for i, sn := range snippets.Items() {
			path := textOf(fieldOf(sn, "path"))
			if path == "" || truthy(fieldOf(sn, "proposed")) {
				continue
			}
			path = lineRangeRe.ReplaceAllString(path, "")
			text, cached := cache[path]
			if !cached {
				text = nil
				if f := r.Safe(path); f != "" && !isDir(f) {
					if body, err := os.ReadFile(f); err == nil {
						bodyText := string(body)
						text = &bodyText
					}
				}
				cache[path] = text
			}
			if text == nil {
				out = append(out, DriftRow{Step: s.ID, I: i, Path: path, State: "missing"})
				continue
			}
			flat := norm(*text)
			var lines []string
			for _, raw := range strings.Split(textOf(fieldOf(sn, "code")), "\n") {
				line := ellipsisEnds(norm(raw))
				if jsscalar.UTF16Len(line) <= 3 || skippedQuote(line) {
					continue
				}
				lines = append(lines, line)
			}
			if len(lines) == 0 {
				continue
			}
			hits := 0
			for _, line := range lines {
				if strings.Contains(flat, line) {
					hits++
				}
			}
			hit := float64(hits) / float64(len(lines))
			score := int(math.Round(hit * 100))
			state := "drift"
			if hit >= 0.6 {
				state = "ok"
			} else if hit >= 0.3 {
				state = "partial"
			}
			out = append(out, DriftRow{Step: s.ID, I: i, Path: path, State: state, Score: &score})
		}
	}
	return out, nil
}
