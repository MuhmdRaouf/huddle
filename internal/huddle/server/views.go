// Read-only views for a channel whose plan lives next to a repo (channel config `repo` = a path
// Huddle can read): a directory level or a file excerpt with the tasks that touch it, drift,
// diagrams, refs, and the whole plan as Markdown.
package server

import (
	"context"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// builtinViews are the repo tabs every channel with a repo offers.
var builtinViews = []string{"code", "drift", "diagrams"}

// driftEntry is one channel's cached drift answer.
type driftEntry struct {
	atMs int64
	rows []DriftRow
}

// Views answers the repo views of a channel, keeping one repo-tools instance per repo path and
// one drift answer per channel for two minutes.
type Views struct {
	mu    sync.Mutex
	repos map[string]*RepoTools
	drift map[string]driftEntry
}

// NewViews answers an empty view state.
func NewViews() *Views {
	return &Views{repos: map[string]*RepoTools{}, drift: map[string]driftEntry{}}
}

func (v *Views) repoFor(repo string) *RepoTools {
	v.mu.Lock()
	defer v.mu.Unlock()
	if t, ok := v.repos[repo]; ok {
		return t
	}
	t := NewRepoTools(repo)
	v.repos[repo] = t
	return t
}

// Names answers the repo tabs a channel offers (an empty list without a reachable repo).
func (v *Views) Names(ch *channel.Channel) []string {
	repo := ch.Config().Repo
	if repo == nil || *repo == "" || !pathExists(*repo) {
		return []string{}
	}
	return builtinViews
}

// NoRepoAnswer names what a channel without a usable repo is told.
type NoRepoAnswer struct {
	Error string `json:"error"`
}

// CodeMissingAnswer says a path is not in the repo.
type CodeMissingAnswer struct {
	Missing bool   `json:"missing"`
	Path    string `json:"path"`
}

// CodeDirAnswer is one directory level of the code view.
type CodeDirAnswer struct {
	Dir     bool     `json:"dir"`
	Path    string   `json:"path"`
	Entries []string `json:"entries"`
}

// CodeExcerptAnswer is one file excerpt of the code view.
type CodeExcerptAnswer struct {
	Path    string `json:"path"`
	From    int    `json:"from"`
	Total   int    `json:"total"`
	Excerpt string `json:"excerpt"`
}

// RefRow is one task that touches a path.
type RefRow struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// DriftAnswer is the drift view: when it was computed and each snippet's verdict.
type DriftAnswer struct {
	At   string     `json:"at"`
	Rows []DriftRow `json:"rows"`
}

// pathColonRe strips a line-range suffix a task's file reference may carry ("x.go:12").
var pathColonRe = regexp.MustCompile(`:\d.*$`)

// Route answers one repo view; query carries the view's own parameters.
func (v *Views) Route(ctx context.Context, ch *channel.Channel, sub string, query url.Values) (any, error) {
	cfg := ch.Config()
	var tools *RepoTools
	if cfg.Repo != nil {
		tools = v.repoFor(*cfg.Repo)
	}
	if tools == nil {
		if cfg.Repo != nil && *cfg.Repo != "" {
			return NoRepoAnswer{Error: "repo " + *cfg.Repo + " is not reachable from Huddle"}, nil
		}
		return NoRepoAnswer{Error: "this channel has no repo configured"}, nil
	}
	path := query.Get("path")
	switch sub {
	case "tree":
		return tools.Tree(ctx, path)
	case "file":
		return tools.File(ctx, path)
	case "code":
		full := tools.Safe(path)
		if path == "" || full == "" {
			return CodeMissingAnswer{Missing: true, Path: path}, nil
		}
		if isDir(full) {
			names, err := os.ReadDir(full)
			if err != nil {
				return CodeMissingAnswer{Missing: true, Path: path}, nil
			}
			entries := []string{}
			for _, e := range names {
				child := e.Name()
				if path != "" {
					child = path + "/" + e.Name()
				}
				if tools.Safe(child) != "" {
					entries = append(entries, e.Name())
				}
			}
			sort.Strings(entries)
			if len(entries) > 200 {
				entries = entries[:200]
			}
			return CodeDirAnswer{Dir: true, Path: path, Entries: entries}, nil
		}
		body, err := os.ReadFile(full)
		if err != nil {
			return CodeMissingAnswer{Missing: true, Path: path}, nil
		}
		lines := strings.Split(string(body), "\n")
		start := 0
		if anchor := query.Get("anchor"); anchor != "" {
			for i, line := range lines {
				if strings.Contains(line, anchor) {
					start = i - 3
					if start < 0 {
						start = 0
					}
					break
				}
			}
		}
		end := start + 80
		if end > len(lines) {
			end = len(lines)
		}
		return CodeExcerptAnswer{
			Path: path, From: start + 1, Total: len(lines), Excerpt: strings.Join(lines[start:end], "\n"),
		}, nil
	case "refs":
		q := strings.TrimSuffix(path, "/")
		if q == "" {
			return []RefRow{}, nil
		}
		tasks, err := allTasks(ctx, ch)
		if err != nil {
			return nil, err
		}
		rows := []RefRow{}
		for _, t := range tasks {
			touches := false
			for _, f := range stringsOf(t.JSON("files")) {
				if f == "" {
					continue
				}
				if f == q || strings.HasPrefix(f, q+"/") || pathColonRe.ReplaceAllString(f, "") == q {
					touches = true
					break
				}
			}
			if !touches {
				if snippets := t.JSON("snippets"); snippets != nil && snippets.Kind() == jsfmt.KindArray {
					for _, sn := range snippets.Items() {
						f := textOf(fieldOf(sn, "path"))
						if f != "" && (f == q || strings.HasPrefix(f, q+"/") || pathColonRe.ReplaceAllString(f, "") == q) {
							touches = true
							break
						}
					}
				}
			}
			if touches {
				rows = append(rows, RefRow{ID: t.ID, Title: t.Title, Status: t.Status})
			}
		}
		return rows, nil
	case "drift":
		nowMs := ch.Now().UnixMilli()
		v.mu.Lock()
		cached, ok := v.drift[ch.Name]
		v.mu.Unlock()
		if ok && nowMs-cached.atMs < 120_000 {
			return DriftAnswer{At: jsfmt.ISOTime(time.UnixMilli(cached.atMs)), Rows: cached.rows}, nil
		}
		tasks, err := allTasks(ctx, ch)
		if err != nil {
			return nil, err
		}
		rows, err := tools.Drift(ctx, tasks)
		if err != nil {
			return nil, err
		}
		v.mu.Lock()
		v.drift[ch.Name] = driftEntry{atMs: nowMs, rows: rows}
		v.mu.Unlock()
		return DriftAnswer{At: jsfmt.ISOTime(time.UnixMilli(nowMs)), Rows: rows}, nil
	case "diagrams":
		names, err := os.ReadDir(*cfg.Repo + "/docs/architecture")
		out := []string{}
		if err != nil {
			return out, nil
		}
		for _, e := range names {
			if strings.HasSuffix(e.Name(), ".png") {
				out = append(out, e.Name())
			}
		}
		sort.Strings(out)
		return out, nil
	}
	return NoRepoAnswer{Error: "no repo view " + sub}, nil
}

// allTasks answers every task with its effective body.
func allTasks(ctx context.Context, ch *channel.Channel) ([]*channel.TaskView, error) {
	list, err := ch.Tasks(ctx, channel.TasksFilter{})
	if err != nil {
		return nil, err
	}
	out := make([]*channel.TaskView, 0, len(list))
	for _, t := range list {
		full, err := ch.Task(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		if full != nil {
			out = append(out, full)
		}
	}
	return out, nil
}

func stringsOf(v *jsfmt.Value) []string {
	if v == nil || v.Kind() != jsfmt.KindArray {
		return nil
	}
	out := make([]string, 0, len(v.Items()))
	for _, item := range v.Items() {
		if item != nil && item.Kind() == jsfmt.KindString {
			out = append(out, item.Text())
		}
	}
	return out
}

// diagramName admits one PNG file name in the architecture folder.
var diagramName = regexp.MustCompile(`^[\w.-]+\.png$`)

// DiagramFile answers the PNG a channel's repo keeps under docs/architecture, "" for none.
func (v *Views) DiagramFile(ch *channel.Channel, name string) string {
	repo := ch.Config().Repo
	if repo == nil || *repo == "" || !diagramName.MatchString(name) {
		return ""
	}
	return *repo + "/docs/architecture/" + name
}

// ExportMd renders the whole plan as Markdown (effective text + open notes).
func (v *Views) ExportMd(ctx context.Context, ch *channel.Channel) (string, error) {
	cfg := ch.Config()
	out := []string{
		"# " + cfg.Title, "",
		"Exported " + jsfmt.ISOTime(ch.Now()) + " from Huddle (channel " + ch.Name + ").", "",
	}
	tasks, err := ch.Tasks(ctx, channel.TasksFilter{})
	if err != nil {
		return "", err
	}
	full := map[string]*channel.TaskView{}
	for _, t := range tasks {
		view, err := ch.Task(ctx, t.ID)
		if err != nil {
			return "", err
		}
		full[t.ID] = view
	}
	phased := map[string]bool{}
	step := func(t *channel.TaskView) {
		phased[t.ID] = true
		owner := ""
		if t.Owner != nil {
			owner = " · owner " + *t.Owner
		}
		edited := ""
		if len(t.Edited) > 0 {
			edited = " · *edited*"
		}
		// a task without a kind, gate or risk prints undefined, the word an absent field renders as
		undef := func(key string) string {
			if v := t.JSON(key); v == nil || v.Kind() == jsfmt.KindNull {
				return "undefined"
			}
			return t.Text(key)
		}
		out = append(out, "### "+t.ID+" "+t.Title, "",
			"`"+undef("kind")+"` · gate `"+undef("gate")+"` · risk `"+undef("risk")+"` · status **"+t.Status+"**"+owner+edited, "")
		if what := t.Text("what"); what != "" {
			out = append(out, "**What.** "+what, "")
		}
		if why := t.Text("why"); why != "" {
			out = append(out, "**Why.** "+why, "")
		}
		if alts := t.JSON("alternatives"); alts != nil && alts.Kind() == jsfmt.KindArray && len(alts.Items()) > 0 {
			out = append(out, "**Not done that way:**", "")
			for _, a := range alts.Items() {
				out = append(out, "- *"+textOf(fieldOf(a, "option"))+"*: "+textOf(fieldOf(a, "why_not")))
			}
			out = append(out, "")
		}
		if how := stringsOf(t.JSON("how")); len(how) > 0 {
			out = append(out, "**How:**", "")
			for i, h := range how {
				out = append(out, strconv.Itoa(i+1)+". "+h)
			}
			out = append(out, "")
		}
		if snippets := t.JSON("snippets"); snippets != nil && snippets.Kind() == jsfmt.KindArray {
			for _, sn := range snippets.Items() {
				proposed := ""
				if truthy(fieldOf(sn, "proposed")) {
					proposed = " (proposed)"
				}
				path := ""
				if p := textOf(fieldOf(sn, "path")); p != "" {
					path = " — `" + p + "`"
				}
				lang := textOf(fieldOf(sn, "lang"))
				out = append(out,
					"*"+textOf(fieldOf(sn, "title"))+"*"+path+proposed, "",
					"```"+lang, textOf(fieldOf(sn, "code")), "```", "")
			}
		}
		if use := t.Text("use"); use != "" {
			out = append(out, "**Use.** "+use, "")
		}
		if verify := t.JSON("verify"); verify != nil && verify.Kind() == jsfmt.KindArray && len(verify.Items()) > 0 {
			out = append(out, "**Verify:**", "")
			for _, item := range verify.Items() {
				out = append(out, "- `"+textOf(fieldOf(item, "cmd"))+"` → "+textOf(fieldOf(item, "expect")))
			}
			out = append(out, "")
		}
		if value := t.Text("value"); value != "" {
			out = append(out, "**Value.** "+value, "")
		}
		if rollback := t.Text("rollback"); rollback != "" {
			out = append(out, "**Rollback.** "+rollback, "")
		}
		open := []string{}
		for _, c := range t.Comments {
			if c.Resolved == 0 {
				open = append(open, "- ["+c.Kind+"] "+c.Body)
			}
		}
		if len(open) > 0 {
			out = append(out, "**Owner review:**", "")
			out = append(out, open...)
			out = append(out, "")
		}
	}
	phases, err := ch.Phases(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range phases {
		makes := stringsOf(fieldOf(p.Body, "make"))
		makeWords := make([]string, 0, len(makes))
		for _, m := range makes {
			makeWords = append(makeWords, "`make "+m+"`")
		}
		out = append(out,
			"## Step "+strconv.Itoa(int(p.N))+": "+p.Title, "",
			textOf(fieldOf(p.Body, "summary")), "",
			"- **Needs:** "+orDash(textOf(fieldOf(p.Body, "needs"))),
			"- **Make:** "+orDash(strings.Join(makeWords, ", ")),
			"- **Value:** "+orDash(textOf(fieldOf(p.Body, "value"))), "")
		for _, t := range tasks {
			if t.PhaseN == p.N {
				if view := full[t.ID]; view != nil {
					step(view)
				}
			}
		}
	}
	loose := false
	for _, t := range tasks {
		if !phased[t.ID] && !loose {
			out = append(out, "## Tasks", "")
			loose = true
		}
		if !phased[t.ID] {
			if view := full[t.ID]; view != nil {
				step(view)
			}
		}
	}
	return strings.Join(out, "\n"), nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
