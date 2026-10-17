// The file a PostToolUse edit touched, as Huddle records it (the channel package's touches):
// the repo it is in and its path inside that repo. Only Edit, Write, MultiEdit and NotebookEdit
// count, and only the path leaves this machine's hook: never the contents. The repo is found
// without running git: the nearest .git up from the file; a worktree's .git file points at the
// repository's common dir, so every worktree of one repository is the same repo (their edits to
// the same file meet at merge time). A file outside any git repo counts within the session's
// cwd.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// Touch is one edit as Huddle records it: the repository (its shared git dir's checkout) and
// the file's path inside it, slash-separated.
type Touch struct {
	Repo string `json:"repo"`
	Path string `json:"path"`
}

// TouchInput is the hook input's edit half: which tool ran, on which file, from which cwd.
type TouchInput struct {
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	} `json:"tool_input"`
	Cwd string `json:"cwd"`
}

// EditTool answers whether a tool is one whose edits count.
func EditTool(name string) bool {
	switch name {
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		return true
	}
	return false
}

// gitdirOf reads the "gitdir: <path>" line of a .git file.
var gitdirOf = regexp.MustCompile(`(?m)^gitdir:\s*(.+)$`)

// absoluteFor says whether p names from a root: a leading slash.
func absoluteFor(p string) bool {
	return strings.HasPrefix(p, "/")
}

// resolvePath answers p as an absolute, cleaned path.
func resolvePath(p string) string {
	if absoluteFor(p) {
		return filepath.Clean(p)
	}
	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}
	return filepath.Clean(filepath.Join(wd, p))
}

// resolveIn answers p as an absolute, cleaned path against dir.
func resolveIn(dir, p string) string {
	if absoluteFor(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(dir, p))
}

// realPath answers p with its symlinks resolved; p itself when it cannot be resolved.
func realPath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// relPath answers f's path inside root, slash-separated; ok is false when f is not inside root.
func relPath(root, f string) (string, bool) {
	rel, err := filepath.Rel(root, f)
	if err != nil || rel == "" || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// commonRepo answers the repository a .git entry stands for: a worktree's (or submodule's) .git
// file names its gitdir, whose commondir names the shared one.
func commonRepo(git string) string {
	info, err := os.Stat(git)
	if err != nil {
		return realPath(git)
	}
	if info.IsDir() {
		return realPath(git)
	}
	raw, err := os.ReadFile(git)
	if err != nil {
		return realPath(git)
	}
	match := gitdirOf.FindStringSubmatch(string(raw))
	if match == nil {
		return realPath(git)
	}
	gitdir := resolveIn(filepath.Dir(git), strings.TrimSpace(match[1]))
	common, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	if err != nil {
		return realPath(gitdir)
	}
	return realPath(resolveIn(gitdir, strings.TrimSpace(string(common))))
}

// TouchOf answers the file one hook input touched, or nil when the tool does not edit or names
// no file. cwd is the session's working directory the hook runs in.
func TouchOf(input TouchInput) *Touch {
	if !EditTool(input.ToolName) {
		return nil
	}
	raw := input.ToolInput.FilePath
	if raw == "" {
		raw = input.ToolInput.NotebookPath
	}
	if raw == "" {
		return nil
	}
	cwd := input.Cwd
	if cwd == "" {
		if dir, err := os.Getwd(); err == nil {
			cwd = dir
		} else {
			cwd = "."
		}
	}
	// as given: a symlinked ancestor is fine, both sides keep it
	file := resolveIn(cwd, raw)
	for dir, i := filepath.Dir(file), 0; i < 64; i++ {
		git := filepath.Join(dir, ".git")
		if _, err := os.Stat(git); err == nil {
			path, ok := relPath(dir, file)
			if !ok {
				return nil
			}
			repo := commonRepo(git)
			if base := filepath.Base(repo); base == ".git" {
				repo = filepath.Dir(repo)
			}
			return &Touch{Repo: repo, Path: path}
		}
		up := filepath.Dir(dir)
		if up == dir {
			break
		}
		dir = up
	}
	path, ok := relPath(resolvePath(cwd), file)
	if !ok {
		return nil
	}
	return &Touch{Repo: realPath(cwd), Path: path}
}

// ReportTouch sends one touch to the channel's touched op. The answer is one line for the
// session when another live session edited the same file lately (once per file per window),
// else nothing. It never fails and never waits longer than ms.
func ReportTouch(ctx context.Context, w *World, id Identity, touch Touch, ms int) string {
	body, err := domain.MarshalCompactJSON(touch)
	if err != nil {
		return ""
	}
	callURL := fmt.Sprintf("%s/api/c/%s/op/touched?as=%s", id.URL, url.PathEscape(id.Channel), url.PathEscape(id.As))
	resp, err := HFetch(ctx, w, http.MethodPost, callURL, map[string]string{"content-type": "application/json"}, body, time.Duration(ms)*time.Millisecond, "")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	var answer struct {
		Result struct {
			Warn string `json:"warn"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return ""
	}
	return answer.Result.Warn
}
