// The reservation side of an edit: what the PreToolUse hook asks the channel before its session
// changes a file, and what the reserve word sends for the paths a session is about to change.
// The repository's identity is its normalised origin — the way a board's key folds every clone
// of one repository into one — so a clone or a satellite on another machine is recognised; a
// repository with no origin is this machine's alone, and matching stays on absolute paths.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
)

// Target is one file an edit is about to touch, as the reservation lookup asks it: the
// absolute path, and — when it sits in a git checkout — the path inside that checkout and the
// repository's identity.
type Target struct {
	Path string `json:"path"`
	Rel  string `json:"rel,omitempty"`
	Repo string `json:"repo,omitempty"`
	Root string `json:"-"`
}

// originBudgetMS bounds the one git call an origin lookup gets: a hook never waits on a slow
// git, and a lookup that misses leaves the identity empty (absolute matching only).
const originBudgetMS = 150

// TargetOf answers the file one edit is about to touch: its absolute, cleaned path (relative
// paths read against cwd) and what of the repository the reservation lookup can use. The git
// child runs in the world's environment, bounded at ms.
func TargetOf(ctx context.Context, w *World, raw, cwd string, ms int) Target {
	if cwd == "" {
		if dir, err := os.Getwd(); err == nil {
			cwd = dir
		} else {
			cwd = "."
		}
	}
	// as given: a symlinked ancestor is fine, both sides keep it
	t := Target{Path: resolveIn(cwd, raw)}
	root, ok := gitRootOf(t.Path)
	if !ok {
		return t
	}
	rel, ok := relPath(root, t.Path)
	if !ok {
		return t
	}
	t.Rel, t.Root = rel, root
	t.Repo = originOf(ctx, w, root, ms)
	return t
}

// originOf answers a checkout's repository identity: its origin remote, normalised the way a
// board's key folds every clone of one repository into one. "" when there is none — a
// repository two machines do not share — so matching falls back to absolute paths.
func originOf(ctx context.Context, w *World, root string, ms int) string {
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(ms)*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(callCtx, "git", "-C", root, "config", "--get", "remote.origin.url")
	if w != nil && w.Environ != nil {
		cmd.Env = w.Environ()
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return board.NormalizeRemote(strings.TrimSpace(string(out)))
}

// reserveGroup is the paths of one repository: what one reserve operation carries. The root
// rides along so the server can keep each path's shape inside the repository.
type reserveGroup struct {
	Repo  string
	Root  string
	Paths []string
}

// groupsOf answers the targets' paths grouped by repository, one group for the paths that name
// no repository.
func groupsOf(targets []Target) []reserveGroup {
	var groups []reserveGroup
	at := func(repo, root string) int {
		for i, g := range groups {
			if g.Repo == repo && g.Root == root {
				return i
			}
		}
		groups = append(groups, reserveGroup{Repo: repo, Root: root})
		return len(groups) - 1
	}
	for _, t := range targets {
		i := at(t.Repo, t.Root)
		groups[i].Paths = append(groups[i].Paths, t.Path)
	}
	return groups
}

// ReserveWarn asks the channel who holds the file this session is about to change and answers
// the one line to put in its context: "" when nobody does, when the session is not in a
// huddle, or when anything fails. It never waits longer than ms.
func ReserveWarn(ctx context.Context, w *World, id Identity, t Target, ms int) string {
	if id.URL == "" || id.Channel == "" || id.As == "" {
		return ""
	}
	body, err := domain.MarshalCompactJSON(struct {
		Path string `json:"path"`
		Repo string `json:"repo,omitempty"`
		Rel  string `json:"rel,omitempty"`
	}{t.Path, t.Repo, t.Rel})
	if err != nil {
		return ""
	}
	callURL := fmt.Sprintf("%s/api/c/%s/op/reserved?as=%s", id.URL, url.PathEscape(id.Channel), url.PathEscape(id.As))
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
			Held bool `json:"held"`
		} `json:"result"`
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return ""
	}
	if !answer.Result.Held {
		return ""
	}
	return answer.Text
}
