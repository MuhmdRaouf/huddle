// Router health: provider plugins' routers append "router.event" lines to the shared spool
// (fallback to Anthropic, refusal, rate limit, budget stop, restart). This package reads
// those lines tolerantly — the health kind may sit in `event` (when the line names itself
// with `kind: "router.event"`) or in `kind`/`type` (when `event` is "router.event") — and
// folds them into per-provider counts over time. Pure; no IO.
package routerhealth

import (
	"cmp"
	"encoding/json"
	"math"
	"slices"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// routerEventKind marks a router health line.
const routerEventKind = "router.event"

// recentCap is how many recent events the view keeps.
const recentCap = 50

// kindKeys are where a health line may name its kind, in lookup order.
var kindKeys = []string{"event", "kind", "type", "action", "name"}

// RouterCounts is one provider's health events by kind.
type RouterCounts struct {
	Fallback          int64 `json:"fallback"`
	Refusal           int64 `json:"refusal"`
	RateLimited       int64 `json:"rate_limited"`
	BudgetStop        int64 `json:"budget_stop"`
	Restart           int64 `json:"restart"`
	UpstreamFailing   int64 `json:"upstream_failing"`
	UpstreamRecovered int64 `json:"upstream_recovered"`
}

// ProviderHealth is one provider's folded health events.
type ProviderHealth struct {
	Plugin string       `json:"plugin"`
	Counts RouterCounts `json:"counts"`
	// Series is events per bucket over the window, oldest first: one total per bucket.
	Series []int64 `json:"series"`
	// KindSeries holds one row per kind, same buckets, in shared.RouterEventKinds order.
	KindSeries [][]int64 `json:"kindSeries"`
	LastAt     *int64    `json:"lastAt"`
	// LastReason is the most recent reason given, for the card's subtitle.
	LastReason *string `json:"lastReason"`
}

// RouterHealth is the health view over one window.
type RouterHealth struct {
	WindowMs  int64                      `json:"windowMs"`
	Buckets   int64                      `json:"buckets"`
	Providers []ProviderHealth           `json:"providers"`
	Recent    []shared.RouterEventRecord `json:"recent"`
}

// IsRouterEventLine says a spool line is a router health event, by either naming convention.
func IsRouterEventLine(line shared.SpoolLine) bool {
	return line.Event == routerEventKind || stringExtra(line, "kind") == routerEventKind
}

// stringExtra reads one of the line's undeclared fields as a string, or "".
func stringExtra(line shared.SpoolLine, key string) string {
	raw, ok := line.Extra[key]
	if !ok {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

// healthKindOf reads the line's health kind from the first key that names a known one.
func healthKindOf(line shared.SpoolLine) (shared.RouterEventKind, bool) {
	for _, key := range kindKeys {
		value := ""
		if key == "event" {
			value = line.Event
		} else {
			value = stringExtra(line, key)
		}
		if slices.Contains(shared.RouterEventKinds, shared.RouterEventKind(value)) {
			return shared.RouterEventKind(value), true
		}
	}
	return "", false
}

// tsOf reads a timestamp as epoch ms.
func tsOf(value string) (int64, bool) {
	parsed, ok := jsfmt.ParseISO(value)
	if !ok {
		return 0, false
	}
	return parsed.UnixMilli(), true
}

// ParseRouterEvent reads one spool line as a router health event; nil when the line is not
// one, names no kind, holds no readable timestamp, or names no plugin.
func ParseRouterEvent(line shared.SpoolLine) *shared.RouterEventRecord {
	if !IsRouterEventLine(line) {
		return nil
	}
	event, ok := healthKindOf(line)
	if !ok {
		return nil
	}
	ts, ok := tsOf(line.Ts)
	if !ok {
		return nil
	}
	plugin := stringExtra(line, "plugin")
	if plugin == "" {
		return nil
	}
	reason := ""
	if line.Reason != nil {
		reason = jsfmt.UTF16Slice(*line.Reason, 0, 200)
	}
	var model *string
	if line.Model != nil && *line.Model != "" {
		model = line.Model
	}
	return &shared.RouterEventRecord{Ts: ts, Plugin: plugin, Event: event, Reason: reason, Model: model}
}

// newProviderEntry is a provider's empty health entry: zeroed counts and buckets-many zeros.
func newProviderEntry(plugin string, buckets int64) *ProviderHealth {
	series := make([]int64, buckets)
	kindSeries := make([][]int64, len(shared.RouterEventKinds))
	for i := range kindSeries {
		kindSeries[i] = make([]int64, buckets)
	}
	return &ProviderHealth{Plugin: plugin, Series: series, KindSeries: kindSeries}
}

// addEvent folds one event into a provider's entry; an event at least as new as the last one
// sets the entry's last-at and last-reason.
func addEvent(entry *ProviderHealth, event shared.RouterEventRecord, bucket int64) {
	switch event.Event {
	case shared.EventFallback:
		entry.Counts.Fallback++
	case shared.EventRefusal:
		entry.Counts.Refusal++
	case shared.EventRateLimited:
		entry.Counts.RateLimited++
	case shared.EventBudgetStop:
		entry.Counts.BudgetStop++
	case shared.EventRestart:
		entry.Counts.Restart++
	case shared.EventUpstreamFailing:
		entry.Counts.UpstreamFailing++
	case shared.EventUpstreamRecovered:
		entry.Counts.UpstreamRecovered++
	}
	entry.Series[bucket]++
	for i, kind := range shared.RouterEventKinds {
		if kind == event.Event {
			entry.KindSeries[i][bucket]++
			break
		}
	}
	if entry.LastAt == nil || event.Ts >= *entry.LastAt {
		at := event.Ts
		entry.LastAt = &at
		if event.Reason == "" {
			entry.LastReason = nil
		} else {
			entry.LastReason = &event.Reason
		}
	}
}

// Health folds health events into per-provider counts and bucket series over the last
// windowMs of now, busiest provider first; recent is the newest fifty, newest first.
func Health(events []shared.RouterEventRecord, now, windowMs, buckets int64) RouterHealth {
	start := now - windowMs
	span := float64(windowMs) / float64(buckets)
	var inWindow []shared.RouterEventRecord
	for _, event := range events {
		if event.Ts > start && event.Ts <= now {
			inWindow = append(inWindow, event)
		}
	}
	var order []string
	byPlugin := make(map[string]*ProviderHealth)
	for _, event := range inWindow {
		entry, ok := byPlugin[event.Plugin]
		if !ok {
			entry = newProviderEntry(event.Plugin, buckets)
			byPlugin[event.Plugin] = entry
			order = append(order, event.Plugin)
		}
		bucket := min(buckets-1, int64(math.Floor(float64(event.Ts-start)/span)))
		addEvent(entry, event, bucket)
	}
	providers := make([]ProviderHealth, 0, len(order))
	for _, plugin := range order {
		providers = append(providers, *byPlugin[plugin])
	}
	slices.SortStableFunc(providers, func(a, b ProviderHealth) int {
		if seriesTotal(a) != seriesTotal(b) {
			return cmp.Compare(seriesTotal(b), seriesTotal(a))
		}
		return jsfmt.LocaleCompare(a.Plugin, b.Plugin)
	})
	recent := slices.Clone(inWindow)
	if recent == nil {
		recent = []shared.RouterEventRecord{}
	}
	slices.SortStableFunc(recent, func(a, b shared.RouterEventRecord) int { return cmp.Compare(b.Ts, a.Ts) })
	if len(recent) > recentCap {
		recent = recent[:recentCap]
	}
	return RouterHealth{WindowMs: windowMs, Buckets: buckets, Providers: providers, Recent: recent}
}

// seriesTotal sums a provider's series.
func seriesTotal(entry ProviderHealth) int64 {
	var total int64
	for _, count := range entry.Series {
		total += count
	}
	return total
}
