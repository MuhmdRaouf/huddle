package ledger

import (
	"fmt"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// The billing columns history carries: the four usage conditions read the way history reads
// them — a whole count above zero, a non-empty word — plus the provider and the via, the
// route naming both when it saw the request, the transcript's model naming the provider
// when it alone did.

// routeBilling is a route source whose event carries the usage, plugin and model given.
func routeBilling(usage domain.RouteUsage, plugin, model string) Sources {
	return Sources{Route: &RouteFacts{Event: domain.RouteEvent{
		Ts: "2026-10-29T12:00:00Z", Event: domain.EventRoute, Plugin: plugin, Model: model,
		Upstream: "api.z.ai", Route: "provider", Status: 200, Usage: &usage,
		Headers: &map[string]string{"request-id": "req_bill"},
	}}}
}

// transcriptBilling is a transcript source carrying the usage and model given.
func transcriptBilling(usage domain.RouteUsage, model string) Sources {
	return Sources{Transcript: &TranscriptFacts{RequestID: "req_bill", Model: model, Usage: usage}}
}

// bothBilling is a route and a transcript that saw the same request, each with its own
// usage: the router's report is the one the billing columns read.
func bothBilling(routeUsage, transcriptUsage domain.RouteUsage) Sources {
	sources := routeBilling(routeUsage, "zai", "glm-4.6")
	sources.Transcript = &TranscriptFacts{RequestID: "req_bill", Model: "glm-4.6", Usage: transcriptUsage}
	return sources
}

// billingUsage is a usage block carrying all four billing keys.
func billingUsage() domain.RouteUsage {
	return domain.RouteUsage{
		"input_tokens":   float64(120),
		"output_tokens":  float64(45),
		"cache_creation": map[string]any{"ephemeral_1h_input_tokens": float64(32)},
		"speed":          "fast",
		"inference_geo":  "us",
		"service_tier":   "priority",
	}
}

// ptrEq answers whether two optional values hold the same thing, nil meaning absent.
func ptrEq[T comparable](got, want *T) bool {
	if got == nil || want == nil {
		return got == want
	}
	return *got == *want
}

// ptrShow renders an optional value for a failure line, nil as <nil>.
func ptrShow[T comparable](value *T) string {
	if value == nil {
		return "<nil>"
	}
	return fmt.Sprint(*value)
}

func TestReconcileAnswersTheBillingColumns(t *testing.T) {
	cases := []struct {
		name         string
		sources      Sources
		want1h       *int64
		wantSpeed    *string
		wantGeo      *string
		wantTier     *string
		wantProvider string
		wantVia      *string
	}{
		{
			name:         "a route usage carrying all four fills the four",
			sources:      routeBilling(billingUsage(), "zai", "glm-4.6"),
			want1h:       i64p(32),
			wantSpeed:    strp("fast"),
			wantGeo:      strp("us"),
			wantTier:     strp("priority"),
			wantProvider: "Z.ai",
			wantVia:      strp("zai"),
		},
		{
			name: "the 1-hour cache writes count only above zero",
			sources: routeBilling(domain.RouteUsage{
				"cache_creation": map[string]any{"ephemeral_1h_input_tokens": float64(0)},
			}, "zai", "glm-4.6"),
			wantProvider: "Z.ai",
			wantVia:      strp("zai"),
		},
		{
			name: "the 1-hour cache writes count only whole numbers",
			sources: routeBilling(domain.RouteUsage{
				"cache_creation": map[string]any{"ephemeral_1h_input_tokens": 2.5},
			}, "zai", "glm-4.6"),
			wantProvider: "Z.ai",
			wantVia:      strp("zai"),
		},
		{
			name:         "an empty speed says nothing",
			sources:      routeBilling(domain.RouteUsage{"speed": ""}, "zai", "glm-4.6"),
			wantProvider: "Z.ai",
			wantVia:      strp("zai"),
		},
		{
			name:         "a geo that is not a string says nothing",
			sources:      routeBilling(domain.RouteUsage{"inference_geo": float64(42)}, "zai", "glm-4.6"),
			wantProvider: "Z.ai",
			wantVia:      strp("zai"),
		},
		{
			name:         "a transcript alone fills the four from its own usage",
			sources:      transcriptBilling(billingUsage(), "glm-4.6"),
			want1h:       i64p(32),
			wantSpeed:    strp("fast"),
			wantGeo:      strp("us"),
			wantTier:     strp("priority"),
			wantProvider: "Z.ai",
		},
		{
			name: "both sources present, the route's speed wins",
			sources: bothBilling(
				domain.RouteUsage{"speed": "fast"},
				domain.RouteUsage{"speed": "slow"},
			),
			wantSpeed:    strp("fast"),
			wantProvider: "Z.ai",
			wantVia:      strp("zai"),
		},
		{
			name:         "a known model names its provider",
			sources:      routeBilling(domain.RouteUsage{}, "zai", "glm-4.6"),
			wantProvider: "Z.ai",
			wantVia:      strp("zai"),
		},
		{
			name:         "an unknown model takes its plugin's name",
			sources:      routeBilling(domain.RouteUsage{}, "kimi", "mystery-1"),
			wantProvider: "Kimi",
			wantVia:      strp("kimi"),
		},
		{
			name:         "an unknown model with no plugin stays other",
			sources:      routeBilling(domain.RouteUsage{}, "", "mystery-1"),
			wantProvider: "other",
			wantVia:      strp("router"),
		},
		{
			name:         "a transcript-only request names the model's provider",
			sources:      transcriptBilling(domain.RouteUsage{}, "claude-sonnet-4-5"),
			wantProvider: "Anthropic",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Reconcile(tc.sources)
			if !ok {
				t.Fatal("reconcile refused sources that know the request")
			}
			if !ptrEq(got.CacheWrite1h, tc.want1h) {
				t.Errorf("CacheWrite1h = %s, want %s", ptrShow(got.CacheWrite1h), ptrShow(tc.want1h))
			}
			if !ptrEq(got.Speed, tc.wantSpeed) {
				t.Errorf("Speed = %s, want %s", ptrShow(got.Speed), ptrShow(tc.wantSpeed))
			}
			if !ptrEq(got.Geo, tc.wantGeo) {
				t.Errorf("Geo = %s, want %s", ptrShow(got.Geo), ptrShow(tc.wantGeo))
			}
			if !ptrEq(got.ServiceTier, tc.wantTier) {
				t.Errorf("ServiceTier = %s, want %s", ptrShow(got.ServiceTier), ptrShow(tc.wantTier))
			}
			if got.Provider != tc.wantProvider {
				t.Errorf("Provider = %q, want %q", got.Provider, tc.wantProvider)
			}
			if !ptrEq(got.Via, tc.wantVia) {
				t.Errorf("Via = %s, want %s", ptrShow(got.Via), ptrShow(tc.wantVia))
			}
		})
	}
}
