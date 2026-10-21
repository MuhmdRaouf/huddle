package pricing

import (
	"slices"
	"testing"
	"time"
)

// A canonical id two offerings name is ambiguous and resolves to nothing, offering or not.
func TestResolveSheetAmbiguousID(t *testing.T) {
	sheet := ModelSheet{Base: Rates{Input: 1}, Verified: true}
	byCanonicalID["two-offerings"] = []sheetRef{
		{Offerings[0], "a", sheet},
		{Offerings[1], "b", sheet},
	}
	defer delete(byCanonicalID, "two-offerings")
	if ref := resolveSheet(nil, "two-offerings"); ref != nil {
		t.Errorf("no offering given: resolved to %v", ref)
	}
	deepseek := OfferingDeepseek
	if ref := resolveSheet(&deepseek, "two-offerings"); ref != nil {
		t.Errorf("offering given: resolved to %v", ref)
	}
}

// tierLabel shortens a tier's threshold the way the detail line prints it.
func TestTierLabel(t *testing.T) {
	cases := []struct {
		tokens float64
		want   string
	}{
		{100_000, "100k"},
		{512_000, "512k"},
		{1_000_000, "1M"},
		{2_500_000, "2.5M"},
	}
	for _, c := range cases {
		if got := tierLabel(c.tokens); got != c.want {
			t.Errorf("tierLabel(%v): got %q, want %q", c.tokens, got, c.want)
		}
	}
}

// A canonical id drops what a request's model string carries that the table's key does not:
// a provider prefix, a context bracket, a dated snapshot suffix and case.
func TestCanonicalModelID(t *testing.T) {
	for _, tc := range []struct{ model, want string }{
		{"zai/glm-5.3[1m]", "glm-5.3"},
		{" GLM-5.3 ", "glm-5.3"},
		{"glm-5.3@20261008", "glm-5.3"},
		{"glm-5.3-20261008", "glm-5.3"},
	} {
		if got := canonicalModelID(tc.model); got != tc.want {
			t.Errorf("canonicalModelID(%q): got %q want %q", tc.model, got, tc.want)
		}
	}
}

// str is the pointer an optional condition field carries.
func str(text string) *string { return &text }

// TestTrimsAreJavaScripts: every trim the table's lookups do is JavaScript's, which drops
// the BOM and keeps NEL.
func TestTrimsAreJavaScripts(t *testing.T) {
	t.Run("a canonical id", func(t *testing.T) {
		cases := []struct{ model, want string }{
			{"\ufeffGLM-5.3[1m]", "glm-5.3"},
			{"\u0085glm-5.3", "\u0085glm-5.3"}, // NEL is not JavaScript whitespace
		}
		for _, c := range cases {
			if got := canonicalModelID(c.model); got != c.want {
				t.Errorf("canonicalModelID(%q) = %q, want %q", c.model, got, c.want)
			}
		}
	})

	t.Run("fast mode", func(t *testing.T) {
		if !isFast(str("\ufefffast")) {
			t.Error(`isFast("\ufefffast") = false, want true`)
		}
		if isFast(str("\u0085fast")) {
			t.Error(`isFast("\u0085fast") = true, want false`)
		}
	})

	t.Run("the priority and geo conditions", func(t *testing.T) {
		priority, geo := 1.5, 1.1
		bom := str("\ufeffpriority")
		got := conditionsOf(ModelSheet{Priority: &priority, GeoUS: &geo}, PriceRequest{ServiceTier: bom})
		if len(got) != 1 || got[0].label != "priority ×1.5" {
			t.Errorf("conditionsOf of a BOM'd tier = %v, want the priority condition only", got)
		}
		nel := str("\u0085us")
		if got := conditionsOf(ModelSheet{Priority: &priority, GeoUS: &geo}, PriceRequest{Geo: nel}); len(got) != 0 {
			t.Errorf("conditionsOf of a NEL'd geo = %v, want none", got)
		}
	})
}

// The index holds every sheet of every offering exactly once: a lookup can only miss what
// the table has, and never resolves a sheet under two keys.
func TestIndexHoldsEverySheetOnce(t *testing.T) {
	sheets := 0
	for _, offering := range Offerings {
		sheets += len(offering.Models)
	}
	refs := 0
	for _, list := range byCanonicalID {
		refs += len(list)
	}
	if refs != sheets {
		t.Errorf("byCanonicalID holds %d refs for %d sheets", refs, sheets)
	}
}

// PriceRequestOf trims the service tier like JavaScript's trim, which drops the BOM: a tier
// of no-break spaces and one of a BOM both bill at the priority multiplier on a sheet that
// has one.
func TestPriceRequestOfTrimsServiceTierLikeJavaScript(t *testing.T) {
	priority := 1.5
	byCanonicalID["tier-trim"] = []sheetRef{
		{Offerings[0], "tier-trim", ModelSheet{Priority: &priority}},
	}
	defer delete(byCanonicalID, "tier-trim")
	for _, tier := range []string{"\u00a0priority\u00a0", "\ufeffpriority"} {
		got := PriceRequestOf(PriceRequest{Model: "tier-trim", ServiceTier: str(tier)})
		if got == nil {
			t.Fatalf("service tier %q: no price", tier)
		}
		if !slices.Contains(got.Detail, "priority ×1.5") {
			t.Errorf("service tier %q: detail %v, want it to contain %q", tier, got.Detail, "priority ×1.5")
		}
	}
}

// sheetOf is the table's sheet for an offering's model, so a test computes its expected
// figure from the same table the code prices with.
func sheetOf(t *testing.T, id OfferingID, model string) ModelSheet {
	t.Helper()
	for _, offering := range Offerings {
		if offering.ID != id {
			continue
		}
		sheet, ok := offering.Models[model]
		if !ok {
			t.Fatalf("%s: no sheet for %s", id, model)
		}
		return sheet
	}
	t.Fatalf("no offering %s", id)
	return ModelSheet{}
}

// ms reads an ISO timestamp to the Unix ms a request carries.
func ms(iso string) int64 {
	at, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		panic(err)
	}
	return at.UnixMilli()
}

// closeTo fails at vitest's toBeCloseTo(x, 12) threshold.
func closeTo(t *testing.T, label string, got, want float64) {
	t.Helper()
	if diff := got - want; !(diff < 0.5e-12 && diff > -0.5e-12) {
		t.Errorf("%s: got %v want %v (close to 12 digits)", label, got, want)
	}
}

// offering is the pointer a request that knows its offering carries.
func offering(id OfferingID) *OfferingID { return &id }

// OfferingOfHost reads the host a request's upstream names out of a full URL, a bare host,
// a port and a path, case- and space-insensitively; an unknown host and an unparsable URL
// name no offering.
func TestOfferingOfHost(t *testing.T) {
	for _, tc := range []struct {
		upstream string
		want     OfferingID
		found    bool
	}{
		{"https://api.z.ai/v1/messages", OfferingZai, true},
		{"https://api.moonshot.ai:443/v1/chat/completions", OfferingMoonshot, true},
		{" API.Z.AI ", OfferingZai, true},
		{" api.z.ai:8443 ", OfferingZai, true},
		{"api.minimax.io/v1", OfferingMinimax, true},
		{"https://dashscope-intl.aliyuncs.com", OfferingQwen, true},
		{"https://example.com", "", false},
		{"http://[::1", "", false},
	} {
		got, found := OfferingOfHost(tc.upstream)
		if got != tc.want || found != tc.found {
			t.Errorf("OfferingOfHost(%q): got (%q, %v) want (%q, %v)", tc.upstream, got, found, tc.want, tc.found)
		}
	}
}

// UniqueOffering names the id as its page spells it when one offering serves the id, and
// nothing when none or two do.
func TestUniqueOffering(t *testing.T) {
	if id, got, ok := UniqueOffering("kimi-k3"); !ok || id != "kimi-k3" || got != OfferingMoonshot {
		t.Errorf("UniqueOffering(kimi-k3): got (%q, %q, %v)", id, got, ok)
	}
	if id, got, ok := UniqueOffering("zai/glm-5.3[1m]"); !ok || id != "glm-5.3" || got != OfferingZai {
		t.Errorf("UniqueOffering(zai/glm-5.3[1m]): got (%q, %q, %v)", id, got, ok)
	}
	if id, got, ok := UniqueOffering("made-up-model"); ok || id != "" || got != "" {
		t.Errorf("UniqueOffering(made-up-model): got (%q, %q, %v) want none", id, got, ok)
	}
	byCanonicalID["two-offerings"] = []sheetRef{
		{Offerings[0], "a", ModelSheet{}},
		{Offerings[1], "b", ModelSheet{}},
	}
	defer delete(byCanonicalID, "two-offerings")
	if id, got, ok := UniqueOffering("two-offerings"); ok || id != "" || got != "" {
		t.Errorf("UniqueOffering(two-offerings): got (%q, %q, %v) want none", id, got, ok)
	}
}

// ratesMatch reports whether two rate sets name the same figures.
func ratesMatch(got, want Rates) bool {
	oneH := (got.CacheWrite1h == nil) == (want.CacheWrite1h == nil)
	if got.CacheWrite1h != nil && want.CacheWrite1h != nil {
		oneH = *got.CacheWrite1h == *want.CacheWrite1h
	}
	return got.Input == want.Input && got.Output == want.Output &&
		got.CacheRead == want.CacheRead && got.CacheWrite5m == want.CacheWrite5m && oneH
}

// RatesOf reads the base rates: the upstream's host picks the offering, with no upstream an
// id only one offering serves decides, and what nothing serves prices to nothing.
func TestRatesOf(t *testing.T) {
	glm := sheetOf(t, OfferingZai, "glm-5.3").Base
	if got, ok := RatesOf("glm-5.3", "https://api.z.ai"); !ok || !ratesMatch(got, glm) {
		t.Errorf("RatesOf(glm-5.3, api.z.ai): got (%v, %v) want the zai base rates", got, ok)
	}
	opus := sheetOf(t, OfferingAnthropic, "claude-opus-5-5").Base
	if got, ok := RatesOf("claude-opus-5-5", ""); !ok || !ratesMatch(got, opus) {
		t.Errorf("RatesOf(claude-opus-5-5, no upstream): got (%v, %v) want the anthropic base rates", got, ok)
	}
	if got, ok := RatesOf("claude-opus-5-5", "https://example.com"); !ok || !ratesMatch(got, opus) {
		t.Errorf("RatesOf(claude-opus-5-5, example.com): got (%v, %v) want the anthropic base rates", got, ok)
	}
	if got, ok := RatesOf("glm-5.3", "https://api.anthropic.com"); ok || got != (Rates{}) {
		t.Errorf("RatesOf(glm-5.3, api.anthropic.com): got (%v, %v) want none", got, ok)
	}
	if got, ok := RatesOf("made-up-model", ""); ok || got != (Rates{}) {
		t.Errorf("RatesOf(made-up-model): got (%v, %v) want none", got, ok)
	}
	byCanonicalID["two-offerings"] = []sheetRef{
		{Offerings[0], "a", ModelSheet{}},
		{Offerings[1], "b", ModelSheet{}},
	}
	defer delete(byCanonicalID, "two-offerings")
	if got, ok := RatesOf("two-offerings", ""); ok || got != (Rates{}) {
		t.Errorf("RatesOf(two-offerings, no upstream): got (%v, %v) want none", got, ok)
	}
}

// resolveSheet serves a request that names both an offering and an id: the offering's sheet
// when it serves the id, nothing when it does not.
func TestResolveSheet(t *testing.T) {
	zai := OfferingZai
	if ref := resolveSheet(&zai, "glm-5.3"); ref == nil || ref.id != "glm-5.3" {
		t.Errorf("resolveSheet(zai, glm-5.3): got %v want glm-5.3", ref)
	}
	anthropic := OfferingAnthropic
	if ref := resolveSheet(&anthropic, "glm-5.3"); ref != nil {
		t.Errorf("resolveSheet(anthropic, glm-5.3): got %v want nil", ref)
	}
}

// inPeak reads a UTC wall clock: a window matches on the hour range and the weekday, its
// start bills and its end does not, and no window is never peak.
func TestInPeak(t *testing.T) {
	peak := deepseekOffPeak.Peak // Mon to Fri, 01:00-04:00 and 06:00-10:00 UTC
	for _, tc := range []struct {
		at   string
		want bool
	}{
		{"2026-10-07T01:00:00Z", true},
		{"2026-10-07T02:00:00Z", true},
		{"2026-10-07T07:00:00Z", true},
		{"2026-10-07T04:00:00Z", false},
		{"2026-10-07T12:00:00Z", false},
		{"2026-10-10T02:00:00Z", false},
	} {
		if got := inPeak(ms(tc.at), peak); got != tc.want {
			t.Errorf("inPeak(%s): got %v want %v", tc.at, got, tc.want)
		}
	}
	if inPeak(ms("2026-10-07T02:00:00Z"), nil) {
		t.Error("inPeak of no window: got true want false")
	}
}

// isFast reads the request's speed case- and space-insensitively; no speed is not fast.
func TestIsFast(t *testing.T) {
	if isFast(nil) {
		t.Error("isFast(nil): got true want false")
	}
	for _, speed := range []string{"fast", " FAST "} {
		if !isFast(str(speed)) {
			t.Errorf("isFast(%q): got false want true", speed)
		}
	}
	if isFast(str("slow")) {
		t.Error(`isFast("slow"): got true want false`)
	}
}

// billedFast prices a fast request's cache off its ratios of the base input: the page
// prices fast input and output only, and a base input of zero reads no ratio.
func TestBilledFast(t *testing.T) {
	fast := FastRates{Input: 8, Output: 40}
	base := sheetOf(t, OfferingAnthropic, "claude-opus-5-5").Base
	billed := billedFast(fast, base)
	closeTo(t, "fast input", billed.input, fast.Input)
	closeTo(t, "fast output", billed.output, fast.Output)
	closeTo(t, "fast cache read", billed.cacheRead, (fast.Input*base.CacheRead)/base.Input)
	closeTo(t, "fast 5m write", billed.write5m, 1.25*fast.Input)
	closeTo(t, "fast 1h write", billed.write1h, 2*fast.Input)

	billed = billedFast(fast, Rates{CacheRead: 0.2})
	if billed.cacheRead != 0 {
		t.Errorf("fast cache read of a zero input rate: got %v want 0", billed.cacheRead)
	}
}

// tierOver bills the highest tier a prompt's token count passes; at or below a threshold
// the base rates stand.
func TestTierOver(t *testing.T) {
	haiku := sheetOf(t, OfferingAnthropic, "claude-haiku-5-5") // one tier over 100k
	if tier := tierOver(haiku, 100_000); tier != nil {
		t.Errorf("tierOver at the threshold: got %v want nil", tier)
	}
	if tier := tierOver(haiku, 99_999); tier != nil {
		t.Errorf("tierOver below the threshold: got %v want nil", tier)
	}
	tier := tierOver(haiku, 100_001)
	if tier == nil || tier.Over != haiku.Tiers[0].Over {
		t.Errorf("tierOver above the threshold: got %v want the over-100k tier", tier)
	}

	two := ModelSheet{Tiers: []Tier{
		{Over: 100, Rates: Rates{Input: 1}},
		{Over: 200, Rates: Rates{Input: 2}},
	}}
	if tier := tierOver(two, 150); tier == nil || tier.Over != 100 {
		t.Errorf("tierOver past the first tier: got %v want the over-100 tier", tier)
	}
	if tier := tierOver(two, 250); tier == nil || tier.Over != 200 {
		t.Errorf("tierOver past both tiers: got %v want the over-200 tier", tier)
	}
}

// billedFrom copies a sheet's rates and reads the 1-hour write where the page lists one;
// where it lists none, the 1-hour tokens bill at the 5-minute rate.
func TestBilledFrom(t *testing.T) {
	base := sheetOf(t, OfferingAnthropic, "claude-opus-5-5").Base
	billed := billedFrom(base)
	closeTo(t, "input", billed.input, base.Input)
	closeTo(t, "output", billed.output, base.Output)
	closeTo(t, "cache read", billed.cacheRead, base.CacheRead)
	closeTo(t, "5m write", billed.write5m, base.CacheWrite5m)
	closeTo(t, "1h write", billed.write1h, *base.CacheWrite1h)

	noHour := sheetOf(t, OfferingMinimax, "MiniMax-M2.7").Base
	billed = billedFrom(noHour)
	closeTo(t, "1h write at the 5m rate", billed.write1h, noHour.CacheWrite5m)
}

// conditionsOf names a condition only when the request trips it: the off-peak discount
// outside the windows and none inside, and the geo multiplier for "us" in any case.
func TestConditionsOf(t *testing.T) {
	sheet := ModelSheet{OffPeak: &deepseekOffPeak}
	off := conditionsOf(sheet, PriceRequest{Ts: ms("2026-10-07T12:00:00Z")})
	if len(off) != 1 || off[0].label != "off-peak ×0.5" || off[0].multiplier != 0.5 {
		t.Errorf("conditionsOf off-peak: got %v want [off-peak ×0.5]", off)
	}
	if in := conditionsOf(sheet, PriceRequest{Ts: ms("2026-10-07T02:00:00Z")}); len(in) != 0 {
		t.Errorf("conditionsOf in peak: got %v want none", in)
	}
	geo := 1.1
	us := conditionsOf(ModelSheet{GeoUS: &geo}, PriceRequest{Geo: str(" US ")})
	if len(us) != 1 || us[0].label != "US geo ×1.1" || us[0].multiplier != 1.1 {
		t.Errorf(`conditionsOf of geo "us": got %v want [US geo ×1.1]`, us)
	}
}

// PriceRequestOf prices every condition the sheet carries and names it in Detail, exactly
// as the code writes it; the expected sums are the table's own rates.
func TestPriceRequestOf(t *testing.T) {
	t.Run("an unknown model prices to nothing", func(t *testing.T) {
		if got := PriceRequestOf(PriceRequest{Model: "made-up-model", Tokens: TokenUse{Input: 1}}); got != nil {
			t.Errorf("made-up-model: got %v want nil", got)
		}
	})

	t.Run("prices the base rates when no condition trips", func(t *testing.T) {
		sheet := sheetOf(t, OfferingZai, "glm-5.3")
		got := PriceRequestOf(PriceRequest{
			Model:  "glm-5.3",
			Tokens: TokenUse{Input: 1e6, Output: 1e5},
			Ts:     ms("2026-10-07T12:00:00Z"),
		})
		if got == nil {
			t.Fatal("glm-5.3: no price")
		}
		closeTo(t, "glm-5.3", got.USD, (1e6/1e6)*sheet.Base.Input+(1e5/1e6)*sheet.Base.Output)
		want := []string{"Z.ai list"}
		if !slices.Equal(got.Detail, want) {
			t.Errorf("detail: got %v want %v", got.Detail, want)
		}
	})

	t.Run("bills the base rates below a tier threshold and the tier's above it", func(t *testing.T) {
		sheet := sheetOf(t, OfferingAnthropic, "claude-haiku-5-5")
		below := PriceRequestOf(PriceRequest{Model: "claude-haiku-5-5", Tokens: TokenUse{Input: 100_000}})
		above := PriceRequestOf(PriceRequest{Model: "claude-haiku-5-5", Tokens: TokenUse{Input: 200_000}})
		if below == nil || above == nil {
			t.Fatalf("claude-haiku-5-5: below %v above %v", below, above)
		}
		closeTo(t, "below the threshold", below.USD, (100_000/1e6)*sheet.Base.Input)
		closeTo(t, "above the threshold", above.USD, (200_000/1e6)*sheet.Tiers[0].Rates.Input)
		wantBelow := []string{"Anthropic list"}
		if !slices.Equal(below.Detail, wantBelow) {
			t.Errorf("below: detail: got %v want %v", below.Detail, wantBelow)
		}
		wantAbove := []string{"Anthropic list", "tier over 100k"}
		if !slices.Equal(above.Detail, wantAbove) {
			t.Errorf("above: detail: got %v want %v", above.Detail, wantAbove)
		}
	})

	t.Run("bills the priority service tier", func(t *testing.T) {
		sheet := sheetOf(t, OfferingMinimax, "MiniMax-M3") // a 1M prompt lands in the over-512k tier
		tier := sheet.Tiers[0].Rates.Input
		priority := *sheet.Priority
		for _, tc := range []struct {
			tier       string
			multiplier float64
			want       []string
		}{
			{"priority", priority, []string{"MiniMax list", "tier over 512k", "priority ×1.5"}},
			{"standard", 1, []string{"MiniMax list", "tier over 512k"}},
		} {
			got := PriceRequestOf(PriceRequest{
				Model:       "MiniMax-M3",
				Tokens:      TokenUse{Input: 1e6},
				Ts:          ms("2026-10-07T12:00:00Z"),
				ServiceTier: str(tc.tier),
			})
			if got == nil {
				t.Fatalf("MiniMax-M3 %s: no price", tc.tier)
			}
			closeTo(t, tc.tier, got.USD, (1e6/1e6)*tier*tc.multiplier)
			if !slices.Equal(got.Detail, tc.want) {
				t.Errorf("%s: detail: got %v want %v", tc.tier, got.Detail, tc.want)
			}
		}
	})

	t.Run("bills the US geo multiplier", func(t *testing.T) {
		sheet := sheetOf(t, OfferingAnthropic, "claude-sonnet-5-5")
		got := PriceRequestOf(PriceRequest{
			Offering: offering(OfferingAnthropic),
			Model:    "claude-sonnet-5-5",
			Tokens:   TokenUse{Input: 1e6},
			Ts:       ms("2026-10-07T12:00:00Z"),
			Geo:      str("us"),
		})
		if got == nil {
			t.Fatal("claude-sonnet-5-5: no price")
		}
		closeTo(t, "us geo", got.USD, (1e6/1e6)*sheet.Base.Input*(*sheet.GeoUS))
		want := []string{"Anthropic list", "US geo ×1.1"}
		if !slices.Equal(got.Detail, want) {
			t.Errorf("detail: got %v want %v", got.Detail, want)
		}
	})

	t.Run("bills off-peak at the discount and peak at the sheet", func(t *testing.T) {
		sheet := sheetOf(t, OfferingDeepseek, "deepseek-flash")
		for _, tc := range []struct {
			at   string
			usd  float64
			want []string
		}{
			// Wednesday 12:00 UTC sits outside both weekday windows, 02:00 inside the first.
			{"2026-10-07T12:00:00Z", sheet.Base.Input * sheet.OffPeak.Multiplier, []string{"DeepSeek list", "off-peak ×0.5"}},
			{"2026-10-07T02:00:00Z", sheet.Base.Input, []string{"DeepSeek list"}},
		} {
			got := PriceRequestOf(PriceRequest{
				Model:  "deepseek-flash",
				Tokens: TokenUse{Input: 1e6},
				Ts:     ms(tc.at),
			})
			if got == nil {
				t.Fatalf("deepseek-flash at %s: no price", tc.at)
			}
			closeTo(t, tc.at, got.USD, (1e6/1e6)*tc.usd)
			if !slices.Equal(got.Detail, tc.want) {
				t.Errorf("%s: detail: got %v want %v", tc.at, got.Detail, tc.want)
			}
		}
	})

	t.Run("bills fast mode where the sheet has fast rates and not where it has none", func(t *testing.T) {
		got := PriceRequestOf(PriceRequest{
			Offering: offering(OfferingAnthropic),
			Model:    "claude-opus-5-5",
			Tokens:   TokenUse{Input: 1e6, Output: 1e6},
			Ts:       ms("2026-10-07T12:00:00Z"),
			Speed:    str("fast"),
		})
		if got == nil {
			t.Fatal("claude-opus-5-5 fast: no price")
		}
		fast := *sheetOf(t, OfferingAnthropic, "claude-opus-5-5").Fast
		closeTo(t, "fast", got.USD, (1e6/1e6)*fast.Input+(1e6/1e6)*fast.Output)
		want := []string{"Anthropic list", "fast mode"}
		if !slices.Equal(got.Detail, want) {
			t.Errorf("detail: got %v want %v", got.Detail, want)
		}

		sheet := sheetOf(t, OfferingZai, "glm-5.3") // no fast rates: the speed reads as nothing
		plain := PriceRequestOf(PriceRequest{
			Model:  "glm-5.3",
			Tokens: TokenUse{Input: 1e6},
			Ts:     ms("2026-10-07T12:00:00Z"),
			Speed:  str("fast"),
		})
		if plain == nil {
			t.Fatal("glm-5.3 fast: no price")
		}
		closeTo(t, "glm-5.3 fast", plain.USD, (1e6/1e6)*sheet.Base.Input)
		wantPlain := []string{"Z.ai list"}
		if !slices.Equal(plain.Detail, wantPlain) {
			t.Errorf("detail: got %v want %v", plain.Detail, wantPlain)
		}
	})

	t.Run("names the 1 h cache writes and bills them at the hour rate", func(t *testing.T) {
		sheet := sheetOf(t, OfferingAnthropic, "claude-opus-5-5")
		got := PriceRequestOf(PriceRequest{
			Model: "claude-opus-5-5",
			Tokens: TokenUse{
				CacheWrite5m: 1e6,
				CacheWrite1h: 1e6,
			},
			Ts: ms("2026-10-07T12:00:00Z"),
		})
		if got == nil {
			t.Fatal("claude-opus-5-5: no price")
		}
		closeTo(t, "5m and 1h writes", got.USD, (1e6/1e6)*sheet.Base.CacheWrite5m+(1e6/1e6)*(*sheet.Base.CacheWrite1h))
		want := []string{"Anthropic list", "1 h cache writes"}
		if !slices.Equal(got.Detail, want) {
			t.Errorf("detail: got %v want %v", got.Detail, want)
		}
	})

	t.Run("bills the 1 h writes at the 5-minute rate where no hour is listed", func(t *testing.T) {
		sheet := sheetOf(t, OfferingMinimax, "MiniMax-M2.7")
		got := PriceRequestOf(PriceRequest{
			Model: "MiniMax-M2.7",
			Tokens: TokenUse{
				CacheWrite1h: 1e6,
			},
			Ts: ms("2026-10-07T12:00:00Z"),
		})
		if got == nil {
			t.Fatal("MiniMax-M2.7: no price")
		}
		closeTo(t, "1h writes at the 5m rate", got.USD, (1e6/1e6)*sheet.Base.CacheWrite5m)
		want := []string{"MiniMax list", "1 h cache writes"}
		if !slices.Equal(got.Detail, want) {
			t.Errorf("detail: got %v want %v", got.Detail, want)
		}
	})
}
