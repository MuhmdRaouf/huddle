package shared

import (
	"math"
	"net/url"
	"strconv"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// Preset is one of the time picker's named ranges.
type Preset string

// The picker's presets, in display order.
const (
	Preset5m  Preset = "5m"
	Preset15m Preset = "15m"
	Preset1h  Preset = "1h"
	Preset6h  Preset = "6h"
	Preset24h Preset = "24h"
	Preset7d  Preset = "7d"
	Preset30d Preset = "30d"
	PresetAll Preset = "all"
)

// TimeRange is the range as the dashboard holds it: a preset (with To nil meaning "until
// now", following the clock) or a custom span (Preset nil; To nil there also reads as
// now). From only carries meaning for a custom range; presets derive it from now at
// resolve time.
type TimeRange struct {
	Preset *Preset `json:"preset"`
	From   int64   `json:"from"`
	To     *int64  `json:"to"`
}

// PresetEntry is one row of the picker's preset list.
type PresetEntry struct {
	Key   Preset `json:"key"`
	Label string `json:"label"`
	// Ms is how far back the preset reaches; nil = all.
	Ms *int64 `json:"ms"`
}

// Presets is the picker's list in display order.
var Presets = []PresetEntry{
	{Key: Preset5m, Label: "Last 5 minutes", Ms: ptr(int64(5 * 60_000))},
	{Key: Preset15m, Label: "Last 15 minutes", Ms: ptr(int64(15 * 60_000))},
	{Key: Preset1h, Label: "Last 1 hour", Ms: ptr(int64(3_600_000))},
	{Key: Preset6h, Label: "Last 6 hours", Ms: ptr(int64(6 * 3_600_000))},
	{Key: Preset24h, Label: "Last 24 hours", Ms: ptr(int64(24 * 3_600_000))},
	{Key: Preset7d, Label: "Last 7 days", Ms: ptr(int64(7 * 86_400_000))},
	{Key: Preset30d, Label: "Last 30 days", Ms: ptr(int64(30 * 86_400_000))},
	{Key: PresetAll, Label: "All time", Ms: nil},
}

// DefaultRange is the range everything falls back to: last 1 hour, ending now.
var DefaultRange = TimeRange{Preset: ptr(Preset1h), From: 0, To: nil}

// Span is the concrete epochs a chart or query needs; From inclusive, To inclusive.
type Span struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}

// ResolveRange turns a range into concrete epochs. An unknown preset reads as all time,
// reaching back to oldest (epoch 0 without one).
func ResolveRange(r TimeRange, now, oldest int64) Span {
	if r.Preset == nil {
		to := now
		if r.To != nil {
			to = *r.To
		}
		return Span{From: r.From, To: to}
	}
	for _, entry := range Presets {
		if entry.Key != *r.Preset {
			continue
		}
		if entry.Ms == nil {
			return Span{From: oldest, To: now}
		}
		return Span{From: now - *entry.Ms, To: now}
	}
	return Span{From: oldest, To: now}
}

// months are the en-GB month abbreviations the stamps use, built by hand so every
// environment sees the same words.
var months = []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

// StampOf renders "6 Oct 14:00"; the year joins in only when it is not the current one.
func StampOf(ts, year int64) string {
	date := time.UnixMilli(ts)
	day := strconv.Itoa(date.Day()) + " " + months[date.Month()-1]
	clock := pad2(date.Hour()) + ":" + pad2(date.Minute())
	if int64(date.Year()) == year {
		return day + " " + clock
	}
	return day + " " + strconv.Itoa(date.Year()) + " " + clock
}

// pad2 zero-pads a clock field.
func pad2(value int) string {
	if value < 10 {
		return "0" + strconv.Itoa(value)
	}
	return strconv.Itoa(value)
}

// RangeLabel is the range in words: a preset's label, or both ends as
// "6 Oct 14:00 – 6 Oct 18:30" (en-GB, 24 h). An unknown preset falls back to All time.
func RangeLabel(r TimeRange, now int64) string {
	if r.Preset != nil {
		for _, entry := range Presets {
			if entry.Key == *r.Preset {
				return entry.Label
			}
		}
		return "All time"
	}
	year := int64(time.UnixMilli(now).Year())
	to := "now"
	if r.To != nil {
		to = StampOf(*r.To, year)
	}
	return StampOf(r.From, year) + " – " + to
}

// ParseRangeHash reads the range out of the hash's params: `range=1h`, or a custom
// `from=<ms>&to=<ms>` (to = now when absent). Anything missing or malformed falls back to
// the default 1h.
func ParseRangeHash(params url.Values) TimeRange {
	for _, entry := range Presets {
		if string(entry.Key) == params.Get("range") {
			preset := entry.Key
			return TimeRange{Preset: &preset, From: 0, To: nil}
		}
	}
	from, ok := numOf(params.Get("from"))
	if !ok {
		return DefaultRange
	}
	return TimeRange{Preset: nil, From: from, To: numOfPtr(params.Get("to"))}
}

// RangeToHash is the hash params for a range: `range=1h` for a preset, `from=<ms>` plus
// `to=<ms>` for a custom one (to omitted while it means now).
func RangeToHash(r TimeRange) string {
	if r.Preset != nil {
		return "range=" + string(*r.Preset)
	}
	hash := "from=" + strconv.FormatInt(r.From, 10)
	if r.To != nil {
		hash += "&to=" + strconv.FormatInt(*r.To, 10)
	}
	return hash
}

// numOf reads a finite number from a hash value; anything else (missing, empty, "abc")
// reads as none.
func numOf(raw string) (int64, bool) {
	if raw == "" {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(jsfmt.Trim(raw), 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, false
	}
	return int64(parsed), true
}

// numOfPtr reads a number the way numOf does, none as nil.
func numOfPtr(raw string) *int64 {
	if value, ok := numOf(raw); ok {
		return &value
	}
	return nil
}

// BucketSteps lists the readable bucket widths, in ms, smallest to largest: 1 s, 5 s,
// 15 s, 30 s, 1 min, 5 min, 15 min, 30 min, 1 h, 3 h, 6 h, 12 h, 1 d.
var BucketSteps = []int64{
	1_000, 5_000, 15_000, 30_000, 60_000, 300_000, 900_000, 1_800_000, 3_600_000,
	10_800_000, 21_600_000, 43_200_000, 86_400_000,
}

// MaxBuckets is the most bars a chart draws.
const MaxBuckets = 240

// Plan is the bars a chart draws over a span: one bar per BucketMs step.
type Plan struct {
	BucketMs int64 `json:"bucketMs"`
	Buckets  int64 `json:"buckets"`
}

// BucketPlan picks the smallest readable step that fits target bars over [from, to],
// then however many of those steps the span holds, capped at MaxBuckets.
func BucketPlan(from, to, target int64) Plan {
	span := max(to-from, 0)
	need := float64(span) / float64(target)
	step := BucketSteps[len(BucketSteps)-1]
	for _, candidate := range BucketSteps {
		if float64(candidate) >= need {
			step = candidate
			break
		}
	}
	buckets := min(int64(MaxBuckets), (span+step-1)/step)
	return Plan{BucketMs: step, Buckets: buckets}
}
