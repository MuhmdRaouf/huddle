package store

import (
	"slices"
	"strings"
	"sync"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// maxOtelRows bounds the rows held: the oldest go first past it. A day of Claude Code's export
// at its 60 s default is about 7 points a minute per session, so this keeps weeks of a few
// sessions.
const maxOtelRows = 100_000

// OtelRows holds the rows of Claude Code's OpenTelemetry export, a source apart from the Store:
// nothing here reaches the transcripts' requests, summaries or costs. Safe for concurrent use.
type OtelRows struct {
	mu   sync.Mutex
	rows []shared.OtelRow
}

// NewOtelRows builds an empty holder.
func NewOtelRows() *OtelRows { return &OtelRows{} }

// Add appends one export's rows, dropping the oldest past the bound.
func (o *OtelRows) Add(rows []shared.OtelRow) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.rows = append(o.rows, rows...)
	if over := len(o.rows) - maxOtelRows; over > 0 {
		o.rows = slices.Delete(o.rows, 0, over)
	}
}

// Rows answers a copy of every row held, in arrival order.
func (o *OtelRows) Rows() []shared.OtelRow {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.rows)
}

// OtelModelTotals is one model's tokens by kind, its cost and its session count, as the export
// reported them.
type OtelModelTotals struct {
	Model         string  `json:"model"`
	Input         float64 `json:"input"`
	Output        float64 `json:"output"`
	CacheRead     float64 `json:"cacheRead"`
	CacheCreation float64 `json:"cacheCreation"`
	CostUsd       float64 `json:"costUsd"`
	Sessions      int     `json:"sessions"`
}

// otelSeries names one series of the export: a cumulative series' points share it, and its
// newest point is its total.
type otelSeries struct {
	metric, session, model, kind string
	startTs                      int64
}

// OtelTotals sums rows per model, costliest first (then by name). A delta point adds its value;
// a cumulative series counts once, at its newest point.
func OtelTotals(rows []shared.OtelRow) []OtelModelTotals {
	newest := map[otelSeries]shared.OtelRow{}
	counted := []shared.OtelRow{}
	for _, row := range rows {
		if !row.Cumulative {
			counted = append(counted, row)
			continue
		}
		key := otelSeries{row.Metric, row.SessionID, row.Model, row.Type, row.StartTs}
		if held, ok := newest[key]; !ok || row.Ts >= held.Ts {
			newest[key] = row
		}
	}
	for _, row := range newest {
		counted = append(counted, row)
	}
	byModel := map[string]*OtelModelTotals{}
	sessions := map[string]map[string]bool{}
	for _, row := range counted {
		totals, ok := byModel[row.Model]
		if !ok {
			totals = &OtelModelTotals{Model: row.Model}
			byModel[row.Model] = totals
			sessions[row.Model] = map[string]bool{}
		}
		sessions[row.Model][row.SessionID] = true
		if row.Metric == shared.OtelCostUsage {
			totals.CostUsd += row.Value
			continue
		}
		switch row.Type {
		case "input":
			totals.Input += row.Value
		case "output":
			totals.Output += row.Value
		case "cacheRead":
			totals.CacheRead += row.Value
		case "cacheCreation":
			totals.CacheCreation += row.Value
		}
	}
	out := make([]OtelModelTotals, 0, len(byModel))
	for model, totals := range byModel {
		totals.Sessions = len(sessions[model])
		out = append(out, *totals)
	}
	slices.SortFunc(out, func(a, b OtelModelTotals) int {
		if a.CostUsd != b.CostUsd {
			if a.CostUsd > b.CostUsd {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Model, b.Model)
	})
	return out
}
