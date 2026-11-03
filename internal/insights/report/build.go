// Package report builds the payload an insights run sends to a model, from the structure of the
// ledger and the transcripts: tokens, cost, timing, tool names, waits and counts. It never reads
// a prompt, a response or a tool's input or output, and it turns repo paths into short labels.
package report

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// Build reads every source for the window and returns the payload with the local label map. The
// first source error stops the build. The sources return only rows inside the window. A payload
// too large for MaxInputTokens comes back folded into per-day summaries (see fold).
func Build(ctx context.Context, src Sources, w Window) (Structure, *Labels, error) {
	rows, err := readAll(ctx, src, w)
	if err != nil {
		return Structure{}, nil, err
	}
	labels := NewLabels()
	repoOf := labelSessions(labels, rows)

	s := Structure{
		Window:    windowView(w, labels),
		Sessions:  sessionRows(rows, labels, repoOf),
		Models:    modelRows(rows.requests),
		Tools:     toolRows(rows.tools),
		Requests:  requestStats(rows.requests),
		Subagents: subagentStats(rows.subagents),
	}
	s.Totals = totals(rows.requests, len(s.Sessions))
	s.Blocked = blocked(rows, labels)
	s.Waste = waste(rows)
	if overCap(s) {
		s = fold(s, rows, labels, repoOf)
	}
	return s, labels, nil
}

// rows is everything the sources returned.
type rows struct {
	requests    []Request
	sessions    []Session
	tools       []ToolRun
	compactions []Compaction
	subagents   []SubagentRun
	asks        []Ask
}

func readAll(ctx context.Context, src Sources, w Window) (rows, error) {
	var r rows
	var err error
	steps := []struct {
		name string
		run  func() error
	}{
		{"requests", func() (e error) { r.requests, e = src.Requests.Requests(ctx, w); return }},
		{"sessions", func() (e error) { r.sessions, e = src.Requests.Sessions(ctx, w); return }},
		{"tool runs", func() (e error) { r.tools, e = src.Tools.ToolRuns(ctx, w); return }},
		{"compactions", func() (e error) { r.compactions, e = src.Transcripts.Compactions(ctx, w); return }},
		{"subagents", func() (e error) { r.subagents, e = src.Transcripts.Subagents(ctx, w); return }},
		{"asks", func() (e error) { r.asks, e = src.Asks.Asks(ctx, w); return }},
	}
	for _, step := range steps {
		if err = step.run(); err != nil {
			return rows{}, fmt.Errorf("read %s: %w", step.name, err)
		}
	}
	return r, nil
}

// labelSessions numbers the sessions in id order, so one input always gets the same labels, and
// returns each session's repo label.
func labelSessions(labels *Labels, r rows) map[string]string {
	repoOf := map[string]string{}
	for _, sess := range r.sessions {
		repoOf[sess.ID] = sess.Repo
	}
	ids := sessionIDs(r)
	for _, id := range ids {
		labels.Session(id)
		repoOf[id] = labels.Repo(repoOf[id])
	}
	return repoOf
}

// sessionIDs is every session any source mentions, sorted.
func sessionIDs(r rows) []string {
	set := map[string]bool{}
	for _, v := range r.requests {
		set[v.Session] = true
	}
	for _, v := range r.sessions {
		set[v.ID] = true
	}
	for _, v := range r.tools {
		set[v.Session] = true
	}
	for _, v := range r.subagents {
		set[v.Session] = true
	}
	for _, v := range r.compactions {
		set[v.Session] = true
	}
	for _, v := range r.asks {
		set[v.Session] = true
	}
	delete(set, "")
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func windowView(w Window, labels *Labels) WindowView {
	if w.Session != "" {
		return WindowView{Session: labels.Session(w.Session)}
	}
	const day = 24 * time.Hour
	return WindowView{Days: int((w.To.Sub(w.From) + day/2) / day)}
}
