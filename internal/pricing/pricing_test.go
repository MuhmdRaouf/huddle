package pricing

import (
	"slices"
	"testing"
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
