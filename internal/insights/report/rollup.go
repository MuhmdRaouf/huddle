package report

import (
	"cmp"
	"math"
	"slices"
	"time"
)

const (
	// askWaited is how long an answered ask must have stopped a session to count as waited.
	askWaited = 30 * time.Second
	// idleGap is the quiet stretch in a session's life that counts as idle time.
	idleGap = 30 * time.Minute
	// readTool is the tool whose repeats are re-reads.
	readTool = "Read"
)

func roundUSD(v float64) float64 { return math.Round(v*1e6) / 1e6 }

type tally struct {
	requests  int
	tokens    Tokens
	cost      float64
	toolCalls int
	subagents int
}

func (t *tally) addRequest(r Request) {
	t.requests++
	t.tokens = t.tokens.plus(r.Tokens)
	t.cost += r.CostUSD
}

func totals(requests []Request, sessions int) Totals {
	var t tally
	for _, r := range requests {
		t.addRequest(r)
	}
	return Totals{Sessions: sessions, Requests: t.requests, Tokens: t.tokens, CostUSD: roundUSD(t.cost)}
}

func sessionRows(r rows, labels *Labels, repoOf map[string]string) []SessionRow {
	by := map[string]*tally{}
	for _, id := range sessionIDs(r) {
		by[id] = &tally{}
	}
	for _, v := range r.requests {
		if t := by[v.Session]; t != nil {
			t.addRequest(v)
		}
	}
	for _, v := range r.tools {
		if t := by[v.Session]; t != nil {
			t.toolCalls++
		}
	}
	for _, v := range r.subagents {
		if t := by[v.Session]; t != nil {
			t.subagents++
		}
	}
	out := make([]SessionRow, 0, len(by))
	for id, t := range by {
		out = append(out, SessionRow{
			Label: labels.Session(id), Repo: repoOf[id],
			Requests: t.requests, Tokens: t.tokens, CostUSD: roundUSD(t.cost),
			ToolCalls: t.toolCalls, Subagents: t.subagents,
		})
	}
	slices.SortFunc(out, func(a, b SessionRow) int {
		return cmp.Or(cmp.Compare(b.Tokens.Total(), a.Tokens.Total()), cmp.Compare(a.Label, b.Label))
	})
	return out
}

func modelRows(requests []Request) []ModelRow {
	type key struct{ model, provider string }
	by := map[key]*tally{}
	for _, v := range requests {
		k := key{v.Model, v.Provider}
		if by[k] == nil {
			by[k] = &tally{}
		}
		by[k].addRequest(v)
	}
	out := make([]ModelRow, 0, len(by))
	for k, t := range by {
		out = append(out, ModelRow{Model: k.model, Provider: k.provider, Requests: t.requests, Tokens: t.tokens, CostUSD: roundUSD(t.cost)})
	}
	slices.SortFunc(out, func(a, b ModelRow) int {
		return cmp.Or(cmp.Compare(b.Tokens.Total(), a.Tokens.Total()), cmp.Compare(a.Model, b.Model), cmp.Compare(a.Provider, b.Provider))
	})
	return out
}

func toolRows(tools []ToolRun) []ToolRow {
	by := map[string]*ToolRow{}
	for _, v := range tools {
		row := by[v.Tool]
		if row == nil {
			row = &ToolRow{Name: v.Tool}
			by[v.Tool] = row
		}
		row.Calls++
		if !v.OK {
			row.Failures++
		}
		row.TotalMs += v.DurationMs
		row.MaxMs = max(row.MaxMs, v.DurationMs)
	}
	out := make([]ToolRow, 0, len(by))
	for _, row := range by {
		out = append(out, *row)
	}
	slices.SortFunc(out, func(a, b ToolRow) int {
		return cmp.Or(cmp.Compare(b.Calls, a.Calls), cmp.Compare(a.Name, b.Name))
	})
	return out
}

func requestStats(requests []Request) RequestStats {
	stats := RequestStats{Count: len(requests)}
	var durations []int64
	for _, r := range requests {
		switch {
		case r.Status == 0:
			stats.Outcomes.NoAnswer++
		case r.Status == 429:
			stats.Outcomes.RateLimited++
		case r.Status >= 500 && r.Status < 600:
			stats.Outcomes.ServerError++
		case r.Status >= 200 && r.Status < 400:
			stats.Outcomes.OK++
		default:
			stats.Outcomes.OtherError++
		}
		if r.DurationMs > 0 {
			durations = append(durations, r.DurationMs)
		}
	}
	stats.DurationMs = timing(durations)
	return stats
}

// timing is the mean, the nearest-rank 95th percentile and the longest of the durations.
func timing(durations []int64) Timing {
	n := len(durations)
	if n == 0 {
		return Timing{}
	}
	slices.Sort(durations)
	var sum int64
	for _, d := range durations {
		sum += d
	}
	rank := (95*n + 99) / 100
	return Timing{Count: n, MeanMs: sum / int64(n), P95Ms: durations[rank-1], MaxMs: durations[n-1]}
}

func subagentStats(runs []SubagentRun) Subagents {
	s := Subagents{Count: len(runs)}
	for _, r := range runs {
		if r.Stopped.IsZero() {
			s.Unfinished++
		}
	}
	return s
}

func blocked(r rows, labels *Labels) Blocked {
	b := Blocked{Asks: askStats(r.asks, labels)}
	for _, t := range r.tools {
		if !t.OK {
			b.FailedTools++
		}
	}
	for _, q := range r.requests {
		if q.Status == 429 {
			b.Retries++
		}
	}
	b.ReReads, _ = repeats(r.tools)
	return b
}

func askStats(asks []Ask, labels *Labels) AskStats {
	s := AskStats{Count: len(asks)}
	longestSession := ""
	for _, a := range asks {
		if a.DecidedAt.IsZero() {
			s.Unanswered++
			continue
		}
		s.Answered++
		wait := a.DecidedAt.Sub(a.CreatedAt)
		if wait >= askWaited {
			s.Waited++
		}
		ms := wait.Milliseconds()
		s.TotalWaitMs += ms
		if label := labels.Session(a.Session); ms > s.LongestWaitMs || (ms == s.LongestWaitMs && label < longestSession) {
			s.LongestWaitMs = ms
			longestSession = label
		}
	}
	s.LongestWaitSession = longestSession
	return s
}

// repeats counts the extra calls of a tool with an input the same session already used: reReads
// for the Read tool, repeatedCalls for every other. A call with no input key never counts.
func repeats(tools []ToolRun) (reReads, repeatedCalls int) {
	type call struct{ session, tool, key string }
	seen := map[call]bool{}
	for _, t := range tools {
		if t.InputKey == "" {
			continue
		}
		c := call{t.Session, t.Tool, t.InputKey}
		switch {
		case !seen[c]:
			seen[c] = true
		case t.Tool == readTool:
			reReads++
		default:
			repeatedCalls++
		}
	}
	return reReads, repeatedCalls
}

func waste(r rows) Waste {
	w := Waste{Compactions: len(r.compactions)}
	_, w.RepeatedCalls = repeats(r.tools)
	for _, a := range r.asks {
		if a.Decision == "deny" {
			w.Denied++
		}
	}
	w.IdleSessions, w.IdleMs = idle(r)
	return w
}

// idle sums, per session, the quiet stretches of at least idleGap between its start, its
// requests and tool calls, and its end. A running session has no end, so its tail is not counted.
func idle(r rows) (sessions int, ms int64) {
	events := map[string][]time.Time{}
	for _, q := range r.requests {
		events[q.Session] = append(events[q.Session], q.At)
	}
	for _, t := range r.tools {
		events[t.Session] = append(events[t.Session], t.At)
	}
	spans := map[string]Session{}
	for _, s := range r.sessions {
		spans[s.ID] = s
	}
	for _, id := range sessionIDs(r) {
		quiet := quietTime(events[id], spans[id])
		if quiet > 0 {
			sessions++
			ms += quiet.Milliseconds()
		}
	}
	return sessions, ms
}

func quietTime(events []time.Time, span Session) time.Duration {
	points := slices.Clone(events)
	if !span.Start.IsZero() {
		points = append(points, span.Start)
	}
	if !span.End.IsZero() {
		points = append(points, span.End)
	}
	slices.SortFunc(points, time.Time.Compare)
	var quiet time.Duration
	for i := 1; i < len(points); i++ {
		if gap := points[i].Sub(points[i-1]); gap >= idleGap {
			quiet += gap
		}
	}
	return quiet
}
