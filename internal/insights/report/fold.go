package report

import (
	"encoding/json"
	"slices"
	"time"
)

const (
	// MaxInputTokens is the most a run sends a model: the instructions and the payload together.
	MaxInputTokens = 150_000
	// instructionReserve is the part of MaxInputTokens the instructions take, so the payload is
	// folded to fit beside them.
	instructionReserve = 1_000
	// bytesPerToken is how many bytes of the payload's JSON one token covers. Keys and numbers
	// split into short tokens, so the figure runs low and the estimate errs high.
	bytesPerToken = 3
	// daySessions is the most sessions a folded day lists, the busiest by tokens.
	daySessions = 50
)

// EstimateTokens is the tokens a model counts in text of this many bytes, rounded up.
func EstimateTokens(text []byte) int64 {
	return int64((len(text) + bytesPerToken - 1) / bytesPerToken)
}

// DaySummary is one UTC day of a folded window: its totals and its busiest sessions.
type DaySummary struct {
	Day      string       `json:"day"` // 2006-01-02
	Totals   Totals       `json:"totals"`
	Sessions []SessionRow `json:"sessions"`
}

// overCap reports whether the structure, sent as it is, would leave no room for the instructions.
func overCap(s Structure) bool {
	payload, err := json.Marshal(s)
	return err == nil && EstimateTokens(payload) > MaxInputTokens-instructionReserve
}

// fold replaces the structure's session rows with one summary per day. The totals, models, tools
// and the rest keep the whole window, and the days keep the sessions' labels from the one local
// map, so a label means the same session on every day. A day lists its busiest sessions, halving
// the list until the payload fits; with none listed a day is its totals alone.
func fold(s Structure, r rows, labels *Labels, repoOf map[string]string) Structure {
	days := byDay(r)
	keys := make([]string, 0, len(days))
	for day := range days {
		keys = append(keys, day)
	}
	slices.Sort(keys)
	summaries := make([]DaySummary, len(keys))
	busiest := make([][]SessionRow, len(keys))
	for i, day := range keys {
		busiest[i] = sessionRows(days[day], labels, repoOf)
		summaries[i] = DaySummary{Day: day, Totals: totals(days[day].requests, len(busiest[i]))}
	}
	s.Sessions = []SessionRow{}
	for keep := daySessions; ; keep /= 2 {
		for i := range summaries {
			summaries[i].Sessions = busiest[i][:min(keep, len(busiest[i]))]
		}
		s.Days = summaries
		if keep == 0 || !overCap(s) {
			return s
		}
	}
}

// byDay splits the rows that name a time by the UTC day they fall on. Sessions, compactions and
// asks do not reach the day summaries.
func byDay(r rows) map[string]rows {
	days := map[string]rows{}
	add := func(at time.Time, put func(*rows)) {
		day := at.UTC().Format(time.DateOnly)
		d := days[day]
		put(&d)
		days[day] = d
	}
	for _, v := range r.requests {
		add(v.At, func(d *rows) { d.requests = append(d.requests, v) })
	}
	for _, v := range r.tools {
		add(v.At, func(d *rows) { d.tools = append(d.tools, v) })
	}
	for _, v := range r.subagents {
		add(v.Started, func(d *rows) { d.subagents = append(d.subagents, v) })
	}
	return days
}
