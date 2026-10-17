// Which files the sessions of a channel edit, so two of them do not work on the same file
// without knowing. The PostToolUse hook reports each edit (Edit, Write, MultiEdit, NotebookEdit)
// as a path relative to its repo, never its contents; one row per session, repo and path keeps
// the latest time. A conflict is a file that two live sessions (not left) edited within
// WindowMin minutes in the same repo. The editing session hears about it once per file per
// window: one line in its context, nothing else. No locks; nothing ever blocks.
package channel

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// WindowMin is the conflict window, in minutes.
const WindowMin = 30

// keepDays is how long touches are kept; knowledge staleness reads old touches too.
const keepDays = 90

// TouchArgs is one reported edit; the values arrive untyped from the hook's input.
type TouchArgs struct {
	Path any
	Repo any
}

// TouchOther is one other session that edited the file in the window.
type TouchOther struct {
	Session string `json:"session"`
	At      string `json:"at"`
}

// TouchResult is what one reported edit answers.
type TouchResult struct {
	Path   string       `json:"path"`
	Repo   string       `json:"repo"`
	Others []TouchOther `json:"others"`
	Warn   *string      `json:"warn"`
}

// ConflictSession is one session of a conflict group.
type ConflictSession struct {
	Name string `json:"name"`
	At   string `json:"at"`
}

// ConflictGroup is one file two or more live sessions edited lately.
type ConflictGroup struct {
	Repo     string            `json:"repo"`
	RepoName string            `json:"repo_name"`
	Path     string            `json:"path"`
	Last     string            `json:"last"`
	Sessions []ConflictSession `json:"sessions"`
}

// ConflictsView is the Conflicts panel.
type ConflictsView struct {
	WindowMin int             `json:"window_min"`
	Conflicts []ConflictGroup `json:"conflicts"`
}

func isoOfMs(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05Z") }

// agoAt renders an instant's age in minutes against a clock; Ago is the same against the wall.
func agoAt(at string, nowMs int64) string {
	t, err := time.Parse("2006-01-02T15:04:05Z", at)
	if err != nil {
		return "just now"
	}
	d := nowMs - t.UnixMilli()
	m := int64(0)
	if d > 0 {
		m = (d + 30000) / 60000
	}
	if m < 1 {
		return "just now"
	}
	if m < 60 {
		return fmt.Sprintf("%d min ago", m)
	}
	return fmt.Sprintf("%d h ago", (m+30)/60)
}

// Ago renders how long ago an instant was, the way the panel says it.
func Ago(at string) string { return agoAt(at, time.Now().UnixMilli()) }

var badPathChars = regexp.MustCompile(`[\x00\n]`)
var dotDot = regexp.MustCompile(`(^|/)\.\.(/|$)`)

// RepoName is a repo key's label: the last part of its path.
func RepoName(repo string) string {
	trimmed := strings.TrimRight(repo, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		return repo
	}
	return parts[len(parts)-1]
}

// cleanTouch validates one reported edit: a file path relative to its repo, and the repo it is
// in.
func cleanTouch(path, repo any) (string, string, error) {
	p := strings.ReplaceAll(anyToString(path), `\`, "/")
	p = strings.TrimPrefix(p, "./")
	r := anyToString(repo)
	if p == "" || jsfmt.UTF16Len(p) > 500 || strings.HasPrefix(p, "/") || dotDot.MatchString(p) || badPathChars.MatchString(p) {
		return "", "", bad("path: a file path relative to its repo")
	}
	if r == "" || jsfmt.UTF16Len(r) > 500 || badPathChars.MatchString(r) {
		return "", "", bad("repo: the repo the file is in")
	}
	return p, r, nil
}

func anyToString(v any) string {
	s, _ := v.(string)
	return s
}

// Touched records a session's edit; it says who else (live, same repo) edited the file in the
// window, and whether this session should be told now (once per file per window).
func Touched(ctx context.Context, c *Channel, me string, a TouchArgs) (TouchResult, error) {
	path, repo, err := cleanTouch(a.Path, a.Repo)
	if err != nil {
		return TouchResult{}, err
	}
	name := me // a subagent's edits are its session's
	if parent, ok := ParentOf(me); ok {
		name = parent
	}
	var out TouchResult
	err = c.serial(ctx, func(ctx context.Context) error {
		s, err := c.Session(ctx, name)
		if err != nil {
			return err
		}
		if s == nil {
			return httpErr(404, fmt.Sprintf("%q has not joined channel %s", name, c.Name))
		}
		now := c.nowMs()
		at := isoOfMs(now)
		since := isoOfMs(now - WindowMin*60_000)
		others, err := c.dbAll(ctx, `SELECT t.session, t.at FROM touches t JOIN sessions s ON s.name = t.session
      WHERE t.repo=? AND t.path=? AND t.session != ? AND t.at > ? AND s.state != 'left' ORDER BY t.at DESC`, repo, path, name, since)
		if err != nil {
			return err
		}
		mine, err := c.dbGet(ctx, "SELECT warned_at FROM touches WHERE session=? AND repo=? AND path=?", name, repo, path)
		if err != nil {
			return err
		}
		warnedAt := rStr(mine, "warned_at")
		warn := len(others) > 0 && !(warnedAt != "" && warnedAt > since)
		var warnedAtParam any
		if warn {
			warnedAtParam = at
		}
		if err := c.dbRun(ctx, `INSERT INTO touches (session, repo, path, at, warned_at) VALUES (?,?,?,?,?)
      ON CONFLICT(session, repo, path) DO UPDATE SET at=excluded.at, warned_at=COALESCE(excluded.warned_at, touches.warned_at)`,
			name, repo, path, at, warnedAtParam); err != nil {
			return err
		}
		if c.Rand() < 0.02 {
			if err := c.dbRun(ctx, "DELETE FROM touches WHERE at < ?", isoOfMs(now-int64(keepDays)*86400_000)); err != nil {
				return err
			}
		}
		if len(others) > 0 {
			names := make([]*jsfmt.Value, 0, len(others)+1)
			names = append(names, jstr(name))
			for _, o := range others {
				names = append(names, jstr(rStr(o, "session")))
			}
			c.emit("conflict", jsfmt.Object(
				[]string{"repo", "path", "sessions"},
				[]*jsfmt.Value{jstr(repo), jstr(path), jsfmt.Array(names)},
			))
		}
		out = TouchResult{Path: path, Repo: repo, Others: []TouchOther{}}
		var line *string
		if warn {
			var who []string
			for _, o := range others {
				who = append(who, fmt.Sprintf("%s edited %s %s", rStr(o, "session"), path, agoAt(rStr(o, "at"), now)))
			}
			theirs := "it"
			if len(others) > 1 {
				theirs = "them"
			}
			var heads string
			if len(who) > 1 {
				var rest []string
				for _, o := range others[1:] {
					rest = append(rest, fmt.Sprintf("%s %s", rStr(o, "session"), agoAt(rStr(o, "at"), now)))
				}
				heads = fmt.Sprintf("%s, %s", who[0], strings.Join(rest, ", "))
			} else {
				heads = who[0]
			}
			l := fmt.Sprintf("Huddle: %s — coordinate with %s (send, or ask) before you change more.", heads, theirs)
			line = &l
		}
		for _, o := range others {
			out.Others = append(out.Others, TouchOther{Session: rStr(o, "session"), At: rStr(o, "at")})
		}
		out.Warn = line
		return nil
	})
	return out, err
}

// Conflicts lists every file two or more live sessions edited in the window, newest first.
func Conflicts(ctx context.Context, c *Channel) (ConflictsView, error) {
	since := isoOfMs(c.nowMs() - WindowMin*60_000)
	rows, err := c.dbAll(ctx, `SELECT t.session, t.repo, t.path, t.at FROM touches t JOIN sessions s ON s.name = t.session
      WHERE t.at > ? AND s.state != 'left' ORDER BY t.at DESC`, since)
	if err != nil {
		return ConflictsView{}, err
	}
	type group struct {
		g        ConflictGroup
		sessions []ConflictSession
	}
	var order []string
	by := map[string]*group{}
	for _, r := range rows {
		k := rStr(r, "repo") + "\x00" + rStr(r, "path")
		g, ok := by[k]
		if !ok {
			g = &group{g: ConflictGroup{
				Repo: rStr(r, "repo"), RepoName: RepoName(rStr(r, "repo")), Path: rStr(r, "path"), Last: rStr(r, "at"),
			}}
			by[k] = g
			order = append(order, k)
		}
		g.sessions = append(g.sessions, ConflictSession{Name: rStr(r, "session"), At: rStr(r, "at")})
	}
	out := ConflictsView{WindowMin: WindowMin, Conflicts: []ConflictGroup{}}
	for _, k := range order {
		g := by[k]
		if len(g.sessions) > 1 {
			g.g.Sessions = g.sessions
			out.Conflicts = append(out.Conflicts, g.g)
		}
	}
	return out, nil
}

// LastEdit is the last time any session of the channel edited a path (for knowledge
// staleness): a ref matches a touched path when it is that path or ends with "/<path>".
func LastEdit(ctx context.Context, c *Channel, ref, after string) (*string, error) {
	parts := strings.Split(ref, "/")
	name := ref
	if len(parts) > 0 {
		name = parts[len(parts)-1]
	}
	escaped := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(name, `\`, `\\`), "%", `\%`), "_", `\_`)
	rows, err := c.dbAll(ctx, "SELECT path, MAX(at) at FROM touches WHERE path LIKE ? ESCAPE '\\' AND at > ? GROUP BY path", "%"+escaped, after)
	if err != nil {
		return nil, err
	}
	var best string
	for _, r := range rows {
		path := rStr(r, "path")
		if ref == path || strings.HasSuffix(ref, "/"+path) {
			if at := rStr(r, "at"); at > best {
				best = at
			}
		}
	}
	if best == "" {
		return nil, nil
	}
	return &best, nil
}
