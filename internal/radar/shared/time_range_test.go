package shared

import (
	"net/url"
	"reflect"
	"testing"
	"time"
)

const now = int64(1_800_000_000_000)

// atMs is a local wall-clock timestamp, so the label tests never depend on the runner's
// zone offset.
func atMs(year int, month time.Month, day, hour, minute int) int64 {
	return time.Date(year, month, day, hour, minute, 0, 0, time.Local).UnixMilli()
}

func presetOf(key Preset) TimeRange {
	return TimeRange{Preset: &key, From: 0, To: nil}
}

func customOf(from int64, to *int64) TimeRange {
	return TimeRange{Preset: nil, From: from, To: to}
}

func TestPresets(t *testing.T) {
	t.Run("lists the picker's presets in display order, with their labels", func(t *testing.T) {
		var keys []Preset
		var labels []string
		var ms []*int64
		for _, entry := range Presets {
			keys = append(keys, entry.Key)
			labels = append(labels, entry.Label)
			ms = append(ms, entry.Ms)
		}
		wantKeys := []Preset{"5m", "15m", "1h", "6h", "24h", "7d", "30d", "all"}
		wantLabels := []string{
			"Last 5 minutes", "Last 15 minutes", "Last 1 hour", "Last 6 hours",
			"Last 24 hours", "Last 7 days", "Last 30 days", "All time",
		}
		wantMs := []*int64{
			ptr(int64(300_000)), ptr(int64(900_000)), ptr(int64(3_600_000)), ptr(int64(21_600_000)),
			ptr(int64(86_400_000)), ptr(int64(604_800_000)), ptr(int64(2_592_000_000)), nil,
		}
		if !reflect.DeepEqual(keys, wantKeys) {
			t.Errorf("keys = %v, want %v", keys, wantKeys)
		}
		if !reflect.DeepEqual(labels, wantLabels) {
			t.Errorf("labels = %v, want %v", labels, wantLabels)
		}
		if !reflect.DeepEqual(ms, wantMs) {
			t.Errorf("ms = %v, want %v", ms, wantMs)
		}
	})
}

func TestResolveRange(t *testing.T) {
	t.Run("resolves a preset against now, ending at it", func(t *testing.T) {
		cases := []struct {
			key  Preset
			want Span
		}{
			{"5m", Span{now - 300_000, now}},
			{"15m", Span{now - 900_000, now}},
			{"1h", Span{now - 3_600_000, now}},
			{"6h", Span{now - 21_600_000, now}},
			{"24h", Span{now - 86_400_000, now}},
			{"7d", Span{now - 604_800_000, now}},
			{"30d", Span{now - 2_592_000_000, now}},
		}
		for _, tt := range cases {
			if got := ResolveRange(presetOf(tt.key), now, 0); got != tt.want {
				t.Errorf("ResolveRange(%q) = %+v, want %+v", tt.key, got, tt.want)
			}
		}
	})

	t.Run("reaches back to the oldest known request for all, epoch zero without one", func(t *testing.T) {
		if got := ResolveRange(presetOf(PresetAll), now, 555); got != (Span{555, now}) {
			t.Errorf("ResolveRange(all, oldest 555) = %+v, want {555 %d}", got, now)
		}
		if got := ResolveRange(presetOf(PresetAll), now, 0); got != (Span{0, now}) {
			t.Errorf("ResolveRange(all, no oldest) = %+v, want {0 %d}", got, now)
		}
	})

	t.Run("uses a custom range as it stands, to = now when the end is open", func(t *testing.T) {
		if got := ResolveRange(customOf(100, ptr(int64(200))), now, 0); got != (Span{100, 200}) {
			t.Errorf("ResolveRange(custom 100→200) = %+v, want {100 200}", got)
		}
		if got := ResolveRange(customOf(100, nil), now, 0); got != (Span{100, now}) {
			t.Errorf("ResolveRange(custom 100→now) = %+v, want {100 %d}", got, now)
		}
	})

	t.Run("reads an unknown preset as all time", func(t *testing.T) {
		nope := Preset("nope")
		got := ResolveRange(TimeRange{Preset: &nope, From: 0, To: nil}, now, 42)
		if got != (Span{42, now}) {
			t.Errorf("ResolveRange(nope) = %+v, want {42 %d}", got, now)
		}
	})
}

func TestRangeLabel(t *testing.T) {
	t.Run("labels every preset in words", func(t *testing.T) {
		for _, entry := range Presets {
			if got := RangeLabel(presetOf(entry.Key), now); got != entry.Label {
				t.Errorf("RangeLabel(%q) = %q, want %q", entry.Key, got, entry.Label)
			}
		}
	})

	t.Run("draws a custom span as two en-GB stamps, the shared year left out", func(t *testing.T) {
		from := atMs(2026, time.October, 6, 14, 0)
		to := atMs(2026, time.October, 6, 18, 30)
		want := "6 Oct 14:00 – 6 Oct 18:30"
		if got := RangeLabel(customOf(from, &to), to); got != want {
			t.Errorf("RangeLabel() = %q, want %q", got, want)
		}
	})

	t.Run("keeps the year of an end that is not the current one, zero-pads the clock", func(t *testing.T) {
		from := atMs(2025, time.March, 1, 9, 5)
		to := atMs(2026, time.October, 6, 18, 30)
		if got := RangeLabel(customOf(from, &to), to); got != "1 Mar 2025 09:05 – 6 Oct 18:30" {
			t.Errorf("RangeLabel() = %q, want \"1 Mar 2025 09:05 – 6 Oct 18:30\"", got)
		}
		december := atMs(2026, time.December, 25, 8, 5)
		if got := RangeLabel(customOf(december, &to), to); got != "25 Dec 08:05 – 6 Oct 18:30" {
			t.Errorf("RangeLabel() = %q, want \"25 Dec 08:05 – 6 Oct 18:30\"", got)
		}
	})

	t.Run("says now for an open-ended custom range", func(t *testing.T) {
		from := atMs(2026, time.October, 6, 14, 0)
		end := atMs(2026, time.October, 6, 18, 30)
		if got := RangeLabel(customOf(from, nil), end); got != "6 Oct 14:00 – now" {
			t.Errorf("RangeLabel() = %q, want \"6 Oct 14:00 – now\"", got)
		}
	})

	t.Run("falls back to All time for an unknown preset", func(t *testing.T) {
		nope := Preset("nope")
		if got := RangeLabel(TimeRange{Preset: &nope, From: 0, To: nil}, now); got != "All time" {
			t.Errorf("RangeLabel(nope) = %q, want \"All time\"", got)
		}
	})
}

func TestParseRangeHash(t *testing.T) {
	t.Run("reads a preset from range=", func(t *testing.T) {
		cases := []struct {
			hash string
			want TimeRange
		}{
			{"range=1h", presetOf(Preset1h)},
			{"range=30d", presetOf(Preset30d)},
			{"range=all", presetOf(PresetAll)},
		}
		for _, tt := range cases {
			params, err := url.ParseQuery(tt.hash)
			if err != nil {
				t.Fatalf("ParseQuery(%q): %v", tt.hash, err)
			}
			if got := ParseRangeHash(params); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseRangeHash(%q) = %+v, want %+v", tt.hash, got, tt.want)
			}
		}
	})

	t.Run("reads a custom range from from= and to=, to = now when absent", func(t *testing.T) {
		params, err := url.ParseQuery("from=100&to=200")
		if err != nil {
			t.Fatal(err)
		}
		if got := ParseRangeHash(params); !reflect.DeepEqual(got, customOf(100, ptr(int64(200)))) {
			t.Errorf("ParseRangeHash(\"from=100&to=200\") = %+v, want custom 100→200", got)
		}
		params, err = url.ParseQuery("from=100")
		if err != nil {
			t.Fatal(err)
		}
		if got := ParseRangeHash(params); !reflect.DeepEqual(got, customOf(100, nil)) {
			t.Errorf("ParseRangeHash(\"from=100\") = %+v, want custom 100→now", got)
		}
	})

	t.Run("falls back to the default 1h for missing or bad input", func(t *testing.T) {
		cases := []struct {
			hash string
			want TimeRange
		}{
			{"", DefaultRange},
			{"range=yesterday", DefaultRange},
			{"from=abc", DefaultRange},
			{"from=", DefaultRange},
			{"from=100&to=later", customOf(100, nil)},
		}
		for _, tt := range cases {
			params, err := url.ParseQuery(tt.hash)
			if err != nil {
				t.Fatalf("ParseQuery(%q): %v", tt.hash, err)
			}
			if got := ParseRangeHash(params); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseRangeHash(%q) = %+v, want %+v", tt.hash, got, tt.want)
			}
		}
	})

	t.Run("lets a valid range= win over a from=", func(t *testing.T) {
		params, err := url.ParseQuery("range=24h&from=100")
		if err != nil {
			t.Fatal(err)
		}
		if got := ParseRangeHash(params); !reflect.DeepEqual(got, presetOf(Preset24h)) {
			t.Errorf("ParseRangeHash(\"range=24h&from=100\") = %+v, want preset 24h", got)
		}
	})
}

func TestRangeToHash(t *testing.T) {
	t.Run("writes range= for a preset and from=&to= for a custom span, to left out when open", func(t *testing.T) {
		cases := []struct {
			r    TimeRange
			want string
		}{
			{presetOf(Preset1h), "range=1h"},
			{presetOf(PresetAll), "range=all"},
			{customOf(100, ptr(int64(200))), "from=100&to=200"},
			{customOf(100, nil), "from=100"},
		}
		for _, tt := range cases {
			if got := RangeToHash(tt.r); got != tt.want {
				t.Errorf("RangeToHash(%+v) = %q, want %q", tt.r, got, tt.want)
			}
		}
	})

	t.Run("round-trips through the URLSearchParams a hash parses", func(t *testing.T) {
		for _, r := range []TimeRange{
			presetOf(Preset5m), presetOf(PresetAll), customOf(100, ptr(int64(200))), customOf(100, nil),
		} {
			params, err := url.ParseQuery(RangeToHash(r))
			if err != nil {
				t.Fatalf("ParseQuery(RangeToHash(%+v)): %v", r, err)
			}
			if got := ParseRangeHash(params); !reflect.DeepEqual(got, r) {
				t.Errorf("round-trip(%+v) = %+v, want the same range back", r, got)
			}
		}
	})
}

func TestBucketPlan(t *testing.T) {
	t.Run("picks the readable step that fits the target bar count", func(t *testing.T) {
		cases := []struct {
			from, to int64
			want     Plan
		}{
			{0, 300_000, Plan{5_000, 60}},            // 5m → 5 s bars
			{0, 3_600_000, Plan{60_000, 60}},         // 1h → 1 min bars
			{0, 21_600_000, Plan{900_000, 24}},       // 6h → 15 min bars
			{0, 86_400_000, Plan{1_800_000, 48}},     // 24h → 30 min bars
			{0, 604_800_000, Plan{10_800_000, 56}},   // 7d → 3 h bars
			{0, 2_592_000_000, Plan{43_200_000, 60}}, // 30d → 12 h bars
		}
		for _, tt := range cases {
			if got := BucketPlan(tt.from, tt.to, 60); got != tt.want {
				t.Errorf("BucketPlan(%d, %d) = %+v, want %+v", tt.from, tt.to, got, tt.want)
			}
		}
	})

	t.Run("honours a custom target, still under the cap", func(t *testing.T) {
		if got := BucketPlan(0, 300_000, 5); got != (Plan{60_000, 5}) {
			t.Errorf("BucketPlan(0, 300000, 5) = %+v, want {60000 5}", got)
		}
		if got := BucketPlan(0, 300_000, 1000); got != (Plan{1_000, MaxBuckets}) {
			t.Errorf("BucketPlan(0, 300000, 1000) = %+v, want {1000 %d}", got, MaxBuckets)
		}
	})

	t.Run("rounds an awkward span up to the next step and the bucket count up", func(t *testing.T) {
		if got := BucketPlan(0, 61_000, 60); got != (Plan{5_000, 13}) {
			t.Errorf("BucketPlan(0, 61000) = %+v, want {5000 13}", got)
		}
		if got := BucketPlan(0, 500, 60); got != (Plan{1_000, 1}) {
			t.Errorf("BucketPlan(0, 500) = %+v, want {1000 1}", got)
		}
	})

	t.Run("caps the bars a chart draws", func(t *testing.T) {
		if MaxBuckets != 240 {
			t.Errorf("MaxBuckets = %d, want 240", MaxBuckets)
		}
		want := Plan{86_400_000, MaxBuckets}
		if got := BucketPlan(0, 400*86_400_000, 60); got != want {
			t.Errorf("BucketPlan(0, 400d) = %+v, want %+v", got, want)
		}
	})
}

// TestNumOfTrimsJavaScriptWhitespace: Number() trims JavaScript's whitespace, which drops the
// BOM and keeps NEL.
func TestNumOfTrimsJavaScriptWhitespace(t *testing.T) {
	if got, ok := numOf("\ufeff42"); !ok || got != 42 {
		t.Errorf(`numOf("\ufeff42") = %d, %t, want 42, true`, got, ok)
	}
	if got, ok := numOf(" 42 "); !ok || got != 42 {
		t.Errorf(`numOf(" 42 ") = %d, %t, want 42, true`, got, ok)
	}
	if got, ok := numOf("\u008542"); ok {
		t.Errorf(`numOf("\u008542") = %d, true, want none`, got)
	}
}
