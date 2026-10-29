package ledger

import (
	"reflect"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// The capture columns history carries: what the call was, the thinking tokens the chosen
// usage breaks out, the allow-listed response headers and the result-event totals flag.

// routeCapture is a route source whose event carries the usage and headers given.
func routeCapture(usage domain.RouteUsage, headers map[string]string) Sources {
	return Sources{Route: &RouteFacts{Event: domain.RouteEvent{
		Ts: "2026-10-29T12:00:00Z", Event: domain.EventRoute, Plugin: "zai", Model: "glm-4.6",
		Upstream: "api.z.ai", Route: "provider", Status: 200, Usage: &usage,
		Headers: &headers,
	}}}
}

// transcriptCapture is a transcript source carrying the usage, what and totals given.
func transcriptCapture(usage domain.RouteUsage, what *string, totals bool) Sources {
	return Sources{Transcript: &TranscriptFacts{
		RequestID: "req_capture", Model: "glm-4.6", Usage: usage, What: what, Totals: totals,
	}}
}

func TestReconcileAnswersTheCaptureColumns(t *testing.T) {
	cases := []struct {
		name        string
		sources     Sources
		wantWhat    *string
		wantThink   *int64
		wantHeaders map[string]string
		wantTotals  bool
	}{
		{
			name: "the route usage's thinking tokens count when whole and above zero",
			sources: routeCapture(domain.RouteUsage{
				"output_tokens_details": map[string]any{"thinking_tokens": float64(16)},
			}, nil),
			wantThink: i64p(16),
		},
		{
			name: "a zero thinking token count says nothing",
			sources: routeCapture(domain.RouteUsage{
				"output_tokens_details": map[string]any{"thinking_tokens": float64(0)},
			}, nil),
		},
		{
			name: "a fractional thinking token count says nothing",
			sources: routeCapture(domain.RouteUsage{
				"output_tokens_details": map[string]any{"thinking_tokens": 2.5},
			}, nil),
		},
		{
			name: "a thinking token count that is not a number says nothing",
			sources: routeCapture(domain.RouteUsage{
				"output_tokens_details": map[string]any{"thinking_tokens": "16"},
			}, nil),
		},
		{
			name: "an output_tokens_details that is not an object says nothing",
			sources: routeCapture(domain.RouteUsage{
				"output_tokens_details": "16",
			}, nil),
		},
		{
			name:      "a transcript alone fills thinking from its own usage",
			sources:   transcriptCapture(domain.RouteUsage{"output_tokens_details": map[string]any{"thinking_tokens": float64(7)}}, nil, false),
			wantThink: i64p(7),
		},
		{
			name: "both sources present, the route's thinking wins",
			sources: func() Sources {
				sources := routeCapture(domain.RouteUsage{
					"output_tokens_details": map[string]any{"thinking_tokens": float64(16)},
				}, nil)
				sources.Transcript = &TranscriptFacts{
					RequestID: "req_capture", Model: "glm-4.6",
					Usage: domain.RouteUsage{"output_tokens_details": map[string]any{"thinking_tokens": float64(7)}},
				}
				return sources
			}(),
			wantThink: i64p(16),
		},
		{
			name: "the route headers keep only the allow-listed pairs",
			sources: routeCapture(domain.RouteUsage{}, map[string]string{
				"request-id":            "req_capture",
				"x-ratelimit-remaining": "99",
				"authorization":         "Bearer sk-test-not-a-real-secret",
				"cookie":                "session=test-not-real",
			}),
			wantHeaders: map[string]string{"request-id": "req_capture", "x-ratelimit-remaining": "99"},
		},
		{
			name:    "route headers that all fail the list keep none",
			sources: routeCapture(domain.RouteUsage{}, map[string]string{"authorization": "Bearer sk-test-not-a-real-secret"}),
		},
		{
			name:    "a transcript alone carries no headers",
			sources: transcriptCapture(domain.RouteUsage{}, nil, false),
		},
		{
			name:     "the transcript's what line lands as it was computed",
			sources:  transcriptCapture(domain.RouteUsage{}, strp("↳ prompt: hi  → text"), false),
			wantWhat: strp("↳ prompt: hi  → text"),
		},
		{
			name:    "a route alone has no what line",
			sources: routeCapture(domain.RouteUsage{}, nil),
		},
		{
			name:       "a transcript's totals flag marks the row",
			sources:    transcriptCapture(domain.RouteUsage{}, nil, true),
			wantTotals: true,
		},
		{
			name:    "a route alone is never a run total",
			sources: routeCapture(domain.RouteUsage{}, nil),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Reconcile(tc.sources)
			if !ok {
				t.Fatal("reconcile refused sources that know the request")
			}
			if !ptrEq(got.What, tc.wantWhat) {
				t.Errorf("What = %s, want %s", ptrShow(got.What), ptrShow(tc.wantWhat))
			}
			if !ptrEq(got.Thinking, tc.wantThink) {
				t.Errorf("Thinking = %s, want %s", ptrShow(got.Thinking), ptrShow(tc.wantThink))
			}
			if !reflect.DeepEqual(got.Headers, tc.wantHeaders) {
				t.Errorf("Headers = %v, want %v", got.Headers, tc.wantHeaders)
			}
			if got.Totals != tc.wantTotals {
				t.Errorf("Totals = %t, want %t", got.Totals, tc.wantTotals)
			}
		})
	}
}
