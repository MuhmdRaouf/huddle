package cost

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"testing"
	"time"
)

// The golden locks the Go table to the dashboard's price table, model for model, so neither
// can drift: a price change edits the table and this golden in the same change.
func TestPriceTable(t *testing.T) {
	t.Run("holds the same offerings, rates, conditions and sources, model for model", func(t *testing.T) {
		raw, err := os.ReadFile("testdata/price_table.json")
		if err != nil {
			t.Fatal(err)
		}
		var golden struct {
			Retrieved string     `json:"retrieved"`
			Offerings []Offering `json:"offerings"`
		}
		if err := json.Unmarshal(raw, &golden); err != nil {
			t.Fatal(err)
		}

		if PricesRetrieved != golden.Retrieved {
			t.Errorf("retrieved: got %q want %q", PricesRetrieved, golden.Retrieved)
		}
		if len(Offerings) != len(golden.Offerings) {
			t.Fatalf("offerings: got %d want %d", len(Offerings), len(golden.Offerings))
		}
		for i := range golden.Offerings {
			got, want := Offerings[i], golden.Offerings[i]
			if got.ID != want.ID {
				t.Errorf("offering %d: id: got %q want %q", i, got.ID, want.ID)
			}
			if got.Host != want.Host {
				t.Errorf("offering %s: host: got %q want %q", got.ID, got.Host, want.Host)
			}
			if got.Source != want.Source {
				t.Errorf("offering %s: source: got %q want %q", got.ID, got.Source, want.Source)
			}
			if got.Retrieved != want.Retrieved {
				t.Errorf("offering %s: retrieved: got %q want %q", got.ID, got.Retrieved, want.Retrieved)
			}
			if diff := diffModels(got.Models, want.Models); diff != "" {
				t.Errorf("offering %s: %s", got.ID, diff)
			}
		}
	})

	t.Run("prices the same request the same way (a tiered, off-peak, US-geo, fast, priority case each)", func(t *testing.T) {
		glm := PriceRequestOf(PriceRequest{
			Offering: offering(OfferingZai),
			Model:    "glm-5.3",
			Tokens:   TokenUse{Input: 1_000_000, Output: 100_000},
			Ts:       ms("2026-10-08T12:00:00Z"),
		})
		if glm == nil {
			t.Fatal("glm-5.3: no price")
		}
		closeTo(t, "glm-5.3", float64(glm.USD), (1_000_000/1e6)*1.4+(100_000/1e6)*4.4)

		// DeepSeek off peak: Wednesday 12:00 UTC is outside both peak windows, so half price.
		offPeak := PriceRequestOf(PriceRequest{
			Offering: offering(OfferingDeepseek),
			Model:    "deepseek-flash",
			Tokens:   TokenUse{Input: 1_000_000},
			Ts:       ms("2026-10-07T12:00:00Z"),
		})
		if offPeak == nil {
			t.Fatal("deepseek-flash: no price")
		}
		if !slices.Contains(offPeak.Detail, "off-peak ×0.5") {
			t.Errorf("off-peak: detail: got %v want to contain %q", offPeak.Detail, "off-peak ×0.5")
		}
		closeTo(t, "off-peak", float64(offPeak.USD), 0.3*0.5)

		haikuTier := PriceRequestOf(PriceRequest{
			Offering: offering(OfferingAnthropic),
			Model:    "claude-haiku-5-5",
			Tokens:   TokenUse{Input: 200_000},
			Ts:       ms("2026-10-08T12:00:00Z"),
		})
		if haikuTier == nil {
			t.Fatal("claude-haiku-5-5: no price")
		}
		if !slices.Contains(haikuTier.Detail, "tier over 100k") {
			t.Errorf("haiku tier: detail: got %v want to contain %q", haikuTier.Detail, "tier over 100k")
		}
		closeTo(t, "haiku tier", float64(haikuTier.USD), 0.2*0.5)

		fast := PriceRequestOf(PriceRequest{
			Offering: offering(OfferingAnthropic),
			Model:    "claude-opus-5-5",
			Tokens:   TokenUse{Input: 1_000_000, Output: 1_000_000},
			Ts:       ms("2026-10-08T12:00:00Z"),
			Speed:    str("fast"),
		})
		if fast == nil {
			t.Fatal("claude-opus-5-5 fast: no price")
		}
		if !slices.Contains(fast.Detail, "fast mode") {
			t.Errorf("fast: detail: got %v want to contain %q", fast.Detail, "fast mode")
		}
		closeTo(t, "fast", float64(fast.USD), 8+40)

		us := PriceRequestOf(PriceRequest{
			Offering: offering(OfferingAnthropic),
			Model:    "claude-sonnet-5-5",
			Tokens:   TokenUse{Input: 1_000_000},
			Ts:       ms("2026-10-08T12:00:00Z"),
			Geo:      str("us"),
		})
		if us == nil {
			t.Fatal("claude-sonnet-5-5 us: no price")
		}
		if !slices.Contains(us.Detail, "US geo ×1.1") {
			t.Errorf("us geo: detail: got %v want to contain %q", us.Detail, "US geo ×1.1")
		}
		closeTo(t, "us geo", float64(us.USD), 2*1.1)

		// MiniMax's 1M prompt tokens land in the over-512k tier (input 0.6), and priority multiplies it.
		priority := PriceRequestOf(PriceRequest{
			Offering:    offering(OfferingMinimax),
			Model:       "MiniMax-M3",
			Tokens:      TokenUse{Input: 1_000_000},
			Ts:          ms("2026-10-08T12:00:00Z"),
			ServiceTier: str("priority"),
		})
		if priority == nil {
			t.Fatal("MiniMax-M3 priority: no price")
		}
		if !slices.Contains(priority.Detail, "tier over 512k") {
			t.Errorf("priority: detail: got %v want to contain %q", priority.Detail, "tier over 512k")
		}
		if !slices.Contains(priority.Detail, "priority ×1.5") {
			t.Errorf("priority: detail: got %v want to contain %q", priority.Detail, "priority ×1.5")
		}
		closeTo(t, "priority", float64(priority.USD), 0.6*1.5)

		if got := PriceRequestOf(PriceRequest{Model: "made-up-model", Tokens: TokenUse{Input: 1, Output: 1}}); got != nil {
			t.Errorf("made-up-model: got %v want nil", got)
		}
	})
}

// The host an upstream names decides the offering: a full URL, a bare host, a port and a
// path all read, and an unknown or unparseable one leaves no offering.
func TestOfferingOfHost(t *testing.T) {
	for _, tc := range []struct {
		upstream string
		want     OfferingID
		found    bool
	}{
		{"https://api.z.ai/v1/messages", OfferingZai, true},
		{" API.Z.AI ", OfferingZai, true},
		{" api.z.ai:8443 ", OfferingZai, true},
		{"api.minimax.io/v1", OfferingMinimax, true},
		{"https://dashscope-intl.aliyuncs.com", OfferingQwen, true},
		{"https://example.com", "", false},
		{"http://[::1", "", false},
	} {
		got, found := OfferingOfHost(tc.upstream)
		if got != tc.want || found != tc.found {
			t.Errorf("offeringOfHost(%q): got (%q, %v) want (%q, %v)", tc.upstream, got, found, tc.want, tc.found)
		}
	}
}

// The sheet a request resolves to: the offering must serve the model, and an id only the
// model names must be unique across offerings to price.
func TestResolveSheet(t *testing.T) {
	if got := PriceRequestOf(PriceRequest{
		Offering: offering(OfferingZai),
		Model:    "claude-sonnet-5-5",
		Tokens:   TokenUse{Input: 1_000_000},
	}); got != nil {
		t.Errorf("an offering that does not serve the model: got %v want nil", got)
	}
	if got := PriceRequestOf(PriceRequest{
		Model:  "minimax-m3",
		Tokens: TokenUse{Input: 1_000_000},
		Ts:     ms("2026-10-10T12:00:00Z"),
	}); got == nil {
		t.Fatal("MiniMax-M3 by its id alone: no price")
	} else {
		// 1M prompt tokens land in the over-512k tier, whose input rate is 0.6
		closeTo(t, "minimax by id", float64(got.USD), 0.6)
	}
}

// diffModels names the first sheet the two model maps disagree on, or "" when they match.
func diffModels(got, want map[string]ModelSheet) string {
	gotKeys := slices.Sorted(maps.Keys(got))
	wantKeys := slices.Sorted(maps.Keys(want))
	if !slices.Equal(gotKeys, wantKeys) {
		return fmt.Sprintf("models: got %v want %v", gotKeys, wantKeys)
	}
	for _, key := range wantKeys {
		if diff := diffSheet(key, got[key], want[key]); diff != "" {
			return diff
		}
	}
	return ""
}

// diffSheet names a model's first differing field, or "" when the sheets match.
func diffSheet(key string, got, want ModelSheet) string {
	if diff := diffRates(key+".base", got.Base, want.Base); diff != "" {
		return diff
	}
	if len(got.Tiers) != len(want.Tiers) {
		return fmt.Sprintf("%s.tiers: got %d want %d", key, len(got.Tiers), len(want.Tiers))
	}
	for i := range want.Tiers {
		path := fmt.Sprintf("%s.tiers[%d]", key, i)
		if got.Tiers[i].Over != want.Tiers[i].Over {
			return fmt.Sprintf("%s.over: got %v want %v", path, got.Tiers[i].Over, want.Tiers[i].Over)
		}
		if diff := diffRates(path+".rates", got.Tiers[i].Rates, want.Tiers[i].Rates); diff != "" {
			return diff
		}
	}
	if diff := diffOffPeak(key, got.OffPeak, want.OffPeak); diff != "" {
		return diff
	}
	if (got.Fast == nil) != (want.Fast == nil) {
		return fmt.Sprintf("%s.fast: got %v want %v", key, got.Fast, want.Fast)
	}
	if got.Fast != nil {
		if got.Fast.Input != want.Fast.Input || got.Fast.Output != want.Fast.Output {
			return fmt.Sprintf("%s.fast: got %v want %v", key, *got.Fast, *want.Fast)
		}
	}
	if diff := diffF64(key+".priority", got.Priority, want.Priority); diff != "" {
		return diff
	}
	if diff := diffF64(key+".geoUs", got.GeoUS, want.GeoUS); diff != "" {
		return diff
	}
	if got.Verified != want.Verified {
		return fmt.Sprintf("%s.verified: got %v want %v", key, got.Verified, want.Verified)
	}
	switch {
	case got.Note == nil && want.Note == nil:
	case got.Note == nil || want.Note == nil:
		return fmt.Sprintf("%s.note: got %v want %v", key, got.Note, want.Note)
	case *got.Note != *want.Note:
		return fmt.Sprintf("%s.note: got %q want %q", key, *got.Note, *want.Note)
	}
	return ""
}

// diffRates names a rate set's first differing figure, or "" when they match.
func diffRates(path string, got, want Rates) string {
	fields := []struct {
		name string
		got  float64
		want float64
	}{
		{name: "input", got: got.Input, want: want.Input},
		{name: "output", got: got.Output, want: want.Output},
		{name: "cacheRead", got: got.CacheRead, want: want.CacheRead},
		{name: "cacheWrite5m", got: got.CacheWrite5m, want: want.CacheWrite5m},
	}
	for _, field := range fields {
		if field.got != field.want {
			return fmt.Sprintf("%s.%s: got %v want %v", path, field.name, field.got, field.want)
		}
	}
	return diffF64(path+".cacheWrite1h", got.CacheWrite1h, want.CacheWrite1h)
}

// diffOffPeak names a sheet's first differing off-peak condition, or "" when they match.
func diffOffPeak(key string, got, want *OffPeak) string {
	if (got == nil) != (want == nil) {
		return fmt.Sprintf("%s.offPeak: got %v want %v", key, got, want)
	}
	if want == nil {
		return ""
	}
	if got.Multiplier != want.Multiplier {
		return fmt.Sprintf("%s.offPeak.multiplier: got %v want %v", key, got.Multiplier, want.Multiplier)
	}
	if len(got.Peak) != len(want.Peak) {
		return fmt.Sprintf("%s.offPeak.peak: got %d windows want %d", key, len(got.Peak), len(want.Peak))
	}
	for i := range want.Peak {
		path := fmt.Sprintf("%s.offPeak.peak[%d]", key, i)
		gotWindow, wantWindow := got.Peak[i], want.Peak[i]
		if !slices.Equal(gotWindow.Days, wantWindow.Days) {
			return fmt.Sprintf("%s.days: got %v want %v", path, gotWindow.Days, wantWindow.Days)
		}
		if gotWindow.Start != wantWindow.Start {
			return fmt.Sprintf("%s.start: got %d want %d", path, gotWindow.Start, wantWindow.Start)
		}
		if gotWindow.End != wantWindow.End {
			return fmt.Sprintf("%s.end: got %d want %d", path, gotWindow.End, wantWindow.End)
		}
	}
	return ""
}

// diffF64 names an optional figure that one side carries and the other does not, or that
// differs, or "" when they match.
func diffF64(path string, got, want *float64) string {
	switch {
	case got == nil && want == nil:
		return ""
	case got == nil || want == nil:
		return fmt.Sprintf("%s: got %v want %v", path, got, want)
	case *got != *want:
		return fmt.Sprintf("%s: got %v want %v", path, *got, *want)
	}
	return ""
}

// ms reads an ISO timestamp the way JavaScript's Date.parse does (the inputs here all carry
// a Z).
func ms(iso string) int64 {
	at, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		panic(err)
	}
	return at.UnixMilli()
}

// offering is the pointer a request that knows its offering carries.
func offering(id OfferingID) *OfferingID { return &id }

// str is the pointer an optional condition field carries.
func str(text string) *string { return &text }

// closeTo fails at vitest's toBeCloseTo(x, 12) threshold.
func closeTo(t *testing.T, label string, got, want float64) {
	t.Helper()
	if diff := got - want; !(diff < 0.5e-12 && diff > -0.5e-12) {
		t.Errorf("%s: got %v want %v (close to 12 digits)", label, got, want)
	}
}

// TestTableTrimsAreJavaScripts: the trims the table's own lookups still do are JavaScript's,
// which drops the BOM and keeps NEL.
func TestTableTrimsAreJavaScripts(t *testing.T) {
	t.Run("a host", func(t *testing.T) {
		if got, ok := OfferingOfHost("\ufeffapi.z.ai"); !ok || got != OfferingZai {
			t.Errorf(`offeringOfHost("\ufeffapi.z.ai") = %q, %v, want zai, true`, got, ok)
		}
		if got, ok := OfferingOfHost("\u0085api.z.ai"); ok || got != "" {
			t.Errorf(`offeringOfHost("\u0085api.z.ai") = %q, %v, want "", false`, got, ok)
		}
	})
}
