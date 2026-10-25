// The self-check route: today's (UTC) Claude Code totals as Radar's history holds them, next to
// the same day re-summed from the transcripts, and the worst counter's drift between the two.
package server

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/history"
	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// SelfCheckPath is the self-check's route; huddle serves it at /api/radar/api/selfcheck.
const SelfCheckPath = "/api/selfcheck"

// SelfCheckCacheFor is how long one answer is served again: a re-sum walks every transcript.
const SelfCheckCacheFor = 60 * time.Second

// selfCheckBody is the route's answer. DriftPct is null when the transcripts hold no call
// that day: there is nothing to measure against.
type selfCheckBody struct {
	Day         string         `json:"day"`
	Radar       ingest.DaySums `json:"radar"`
	Transcripts ingest.DaySums `json:"transcripts"`
	DriftPct    *float64       `json:"driftPct"`
}

// SelfCheck answers the self-check, re-summing at most once per SelfCheckCacheFor.
type SelfCheck struct {
	history *history.History
	roots   []string
	now     func() time.Time

	mu       sync.Mutex
	cached   ApiResponse
	cachedAt time.Time
	hasCache bool
}

// NewSelfCheck is a self-check over the history and the transcript roots; a nil now reads
// the wall clock.
func NewSelfCheck(h *history.History, roots []string, now func() time.Time) *SelfCheck {
	if now == nil {
		now = time.Now
	}
	return &SelfCheck{history: h, roots: roots, now: now}
}

// Answer is the self-check for today's UTC date: the cached answer while it is fresh, a 503
// without a history.
func (c *SelfCheck) Answer(ctx context.Context) (ApiResponse, error) {
	if c.history == nil {
		return errorOf(503, "history unavailable"), nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	at := c.now()
	if c.hasCache && at.Sub(c.cachedAt) < SelfCheckCacheFor {
		return c.cached, nil
	}
	body, err := c.measure(ctx, at.UTC())
	if err != nil {
		return ApiResponse{}, err
	}
	c.cached, c.cachedAt, c.hasCache = jsonOf(200, body), at, true
	return c.cached, nil
}

// measure reads both sums for the UTC day at falls on.
func (c *SelfCheck) measure(ctx context.Context, at time.Time) (selfCheckBody, error) {
	from := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
	rows, err := c.history.UsageRows(ctx, from.UnixMilli(), from.AddDate(0, 0, 1).UnixMilli()-1)
	if err != nil {
		return selfCheckBody{}, err
	}
	var radar ingest.DaySums
	for _, row := range rows {
		// the transcripts are Claude Code's own; other harnesses' calls have no line there
		if row.Harness != "" && row.Harness != shared.HarnessClaude {
			continue
		}
		radar.Requests++
		radar.Input += row.Input
		radar.Output += row.Output
		radar.CacheRead += row.CacheRead
		radar.CacheWrite += row.CacheWrite
	}
	transcripts, err := ingest.SumTranscripts(c.roots, from)
	if err != nil {
		return selfCheckBody{}, err
	}
	return selfCheckBody{
		Day:         from.Format(time.DateOnly),
		Radar:       radar,
		Transcripts: transcripts,
		DriftPct:    driftPctOf(radar, transcripts),
	}, nil
}

// driftPctOf is the largest per-counter |radar - transcripts| / transcripts, as a percent with
// two decimals; nil when the transcripts hold no call.
func driftPctOf(radar, transcripts ingest.DaySums) *float64 {
	if transcripts.Requests == 0 {
		return nil
	}
	pairs := [][2]int64{
		{radar.Requests, transcripts.Requests},
		{radar.Input, transcripts.Input},
		{radar.Output, transcripts.Output},
		{radar.CacheRead, transcripts.CacheRead},
		{radar.CacheWrite, transcripts.CacheWrite},
	}
	worst := 0.0
	for _, pair := range pairs {
		ours, truth := pair[0], pair[1]
		var drift float64
		switch {
		case truth != 0:
			drift = math.Abs(float64(ours-truth)) / float64(truth) * 100
		case ours != 0:
			drift = 100 // tokens the transcripts never wrote
		}
		worst = max(worst, drift)
	}
	worst = math.Round(worst*100) / 100
	return &worst
}
