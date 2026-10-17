package shared

import (
	"encoding/json"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"
)

// The golden locks the presets and the hash codec the dashboard's range picker and this
// package share, so neither copy drifts: change the presets, the codec and this golden in
// the same change.
//
// The custom stamps carry local wall-clock components, not epochs, so the golden holds on
// any timezone.

func TestTimeRangeGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/time_range_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Now     int64 `json:"now"`
		Presets []struct {
			Key           string `json:"key"`
			Label         string `json:"label"`
			Ms            *int64 `json:"ms"`
			Resolve       Span   `json:"resolve"`
			ResolveOldest Span   `json:"resolveOldest"`
			RangeLabel    string `json:"rangeLabel"`
			Hash          string `json:"hash"`
		} `json:"presets"`
		UnknownPreset struct {
			Resolve    Span   `json:"resolve"`
			RangeLabel string `json:"rangeLabel"`
		} `json:"unknownPreset"`
		Stamps []struct {
			Local [5]int `json:"local"`
			Year  int64  `json:"year"`
			Out   string `json:"out"`
		} `json:"stamps"`
		Custom []struct {
			Now   [5]int  `json:"now"`
			From  [5]int  `json:"from"`
			To    *[5]int `json:"to"`
			Label string  `json:"label"`
		} `json:"custom"`
		Parse []struct {
			Hash  string `json:"hash"`
			Range struct {
				Preset *string `json:"preset"`
				From   int64   `json:"from"`
				To     *int64  `json:"to"`
			} `json:"range"`
		} `json:"parse"`
		RoundTrips []string `json:"roundTrips"`
		Buckets    []struct {
			From     int64 `json:"from"`
			To       int64 `json:"to"`
			Target   int64 `json:"target"`
			BucketMs int64 `json:"bucketMs"`
			Buckets  int64 `json:"buckets"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}

	if len(golden.Presets) != len(Presets) {
		t.Fatalf("golden lists %d presets, Go has %d", len(golden.Presets), len(Presets))
	}
	for i, want := range golden.Presets {
		have := Presets[i]
		if string(have.Key) != want.Key || have.Label != want.Label ||
			!reflect.DeepEqual(have.Ms, want.Ms) {
			t.Errorf("preset %d = %+v, want key %q label %q ms %v", i, have, want.Key, want.Label, want.Ms)
		}
		key := Preset(want.Key)
		if got := ResolveRange(TimeRange{Preset: &key}, golden.Now, 0); got != want.Resolve {
			t.Errorf("preset %s resolves to %+v, want %+v", want.Key, got, want.Resolve)
		}
		if got := ResolveRange(TimeRange{Preset: &key}, golden.Now, 555); got != want.ResolveOldest {
			t.Errorf("preset %s (oldest 555) resolves to %+v, want %+v", want.Key, got, want.ResolveOldest)
		}
		if got := RangeLabel(TimeRange{Preset: &key}, golden.Now); got != want.RangeLabel {
			t.Errorf("preset %s labels as %q, want %q", want.Key, got, want.RangeLabel)
		}
		if got := RangeToHash(TimeRange{Preset: &key}); got != want.Hash {
			t.Errorf("preset %s hashes as %q, want %q", want.Key, got, want.Hash)
		}
	}

	nope := Preset("nope")
	if got := ResolveRange(TimeRange{Preset: &nope}, golden.Now, 42); got != golden.UnknownPreset.Resolve {
		t.Errorf("unknown preset resolves to %+v, want %+v", got, golden.UnknownPreset.Resolve)
	}
	if got := RangeLabel(TimeRange{Preset: &nope}, golden.Now); got != golden.UnknownPreset.RangeLabel {
		t.Errorf("unknown preset labels as %q, want %q", got, golden.UnknownPreset.RangeLabel)
	}

	for _, want := range golden.Stamps {
		ts := localMs(want.Local)
		if got := StampOf(ts, want.Year); got != want.Out {
			t.Errorf("StampOf(%v) = %q, want %q", want.Local, got, want.Out)
		}
	}

	for _, want := range golden.Custom {
		from := localMs(want.From)
		var to *int64
		if want.To != nil {
			to = ptr(localMs(*want.To))
		}
		now := localMs(want.Now)
		if got := RangeLabel(TimeRange{From: from, To: to}, now); got != want.Label {
			t.Errorf("custom label = %q, want %q", got, want.Label)
		}
	}

	for _, want := range golden.Parse {
		params, err := url.ParseQuery(want.Hash)
		if err != nil {
			t.Fatalf("ParseQuery(%q): %v", want.Hash, err)
		}
		got := ParseRangeHash(params)
		var gotPreset *string
		if got.Preset != nil {
			gotPreset = ptr(string(*got.Preset))
		}
		if gotPreset == nil && want.Range.Preset != nil || gotPreset != nil && want.Range.Preset == nil {
			t.Errorf("parse(%q) preset = %v, want %v", want.Hash, gotPreset, want.Range.Preset)
		}
		if gotPreset != nil && *gotPreset != *want.Range.Preset {
			t.Errorf("parse(%q) preset = %q, want %q", want.Hash, *gotPreset, *want.Range.Preset)
		}
		if got.From != want.Range.From || !reflect.DeepEqual(got.To, want.Range.To) {
			t.Errorf("parse(%q) = from %d to %v, want from %d to %v",
				want.Hash, got.From, got.To, want.Range.From, want.Range.To)
		}
	}

	for _, want := range golden.RoundTrips {
		params, err := url.ParseQuery(want)
		if err != nil {
			t.Fatalf("ParseQuery(%q): %v", want, err)
		}
		if got := RangeToHash(ParseRangeHash(params)); got != want {
			t.Errorf("hash round-trip of %q came back as %q", want, got)
		}
	}

	for _, want := range golden.Buckets {
		if got := BucketPlan(want.From, want.To, want.Target); got != (Plan{want.BucketMs, want.Buckets}) {
			t.Errorf("BucketPlan(%d, %d, %d) = %+v, want {%d %d}",
				want.From, want.To, want.Target, got, want.BucketMs, want.Buckets)
		}
	}
}

// localMs builds an epoch from a golden stamp's [year, month0, day, hour, minute] the way
// new Date(y, mo, d, h, mi) does: the runner's local wall clock.
func localMs(parts [5]int) int64 {
	return time.Date(parts[0], time.Month(parts[1]+1), parts[2], parts[3], parts[4], 0, 0, time.Local).UnixMilli()
}
