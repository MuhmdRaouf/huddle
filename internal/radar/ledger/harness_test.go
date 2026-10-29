package ledger

// The reconcile scenarios for a harness's own record: the row files under the record's id
// and reads the rest off the record — the model, the session and agent, the moment, the
// counters and the cost the harness itself reported.

import (
	"reflect"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

func TestReconcileFillsARowFromHarnessFacts(t *testing.T) {
	usage := domain.RouteUsage{
		"input_tokens":                float64(800),
		"output_tokens":               float64(300),
		"cache_read_input_tokens":     float64(200),
		"cache_creation_input_tokens": float64(40),
		"output_tokens_details":       map[string]any{"thinking_tokens": int64(100)},
	}
	cases := []struct {
		name string
		fact HarnessFacts
		want Row
	}{
		{
			name: "a codex record",
			fact: HarnessFacts{
				ID:        "codex:s9:1",
				Harness:   shared.HarnessCodex,
				SessionID: "s9",
				AgentID:   "main",
				Model:     "gpt-5-codex",
				Ts:        1730000000000,
				Usage:     usage,
				NodeID:    "s9/main",
			},
			want: Row{
				ID:          "codex:s9:1",
				StartedAt:   1730000000000,
				Harness:     "codex",
				Model:       "gpt-5-codex",
				SessionID:   "s9",
				AgentID:     "main",
				Input:       800,
				Output:      300,
				CacheRead:   200,
				CacheWrite:  40,
				Thinking:    i64p(100),
				Provider:    shared.ProviderOf("gpt-5-codex"),
				NodeID:      "s9/main",
				UsageSource: "harness",
			},
		},
		{
			name: "an opencode record with the cost it reported",
			fact: HarnessFacts{
				ID:             "opencode:msg_aaa",
				Harness:        shared.HarnessOpencode,
				SessionID:      "ses_a1",
				AgentID:        "main",
				Model:          "claude-sonnet-5-5",
				Ts:             1730000005000,
				Usage:          domain.RouteUsage{"input_tokens": float64(600), "output_tokens": float64(400)},
				HarnessCostUSD: f64p(0.03),
				NodeID:         "ses_a1/main",
			},
			want: Row{
				ID:             "opencode:msg_aaa",
				StartedAt:      1730000005000,
				Harness:        "opencode",
				Model:          "claude-sonnet-5-5",
				SessionID:      "ses_a1",
				AgentID:        "main",
				Input:          600,
				Output:         400,
				Provider:       "Anthropic",
				NodeID:         "ses_a1/main",
				UsageSource:    "harness",
				HarnessCostUSD: f64p(0.03),
			},
		},
		{
			name: "a record with no session counts outside any session",
			fact: HarnessFacts{
				ID:      "codex:rollout-plain:0",
				Harness: shared.HarnessCodex,
				AgentID: "main",
				Model:   "gpt-5-codex",
				Ts:      1730000010000,
				Usage:   domain.RouteUsage{"input_tokens": float64(5)},
			},
			want: Row{
				ID:          "codex:rollout-plain:0",
				StartedAt:   1730000010000,
				Harness:     "codex",
				Model:       "gpt-5-codex",
				SessionID:   shared.OutsideSession,
				AgentID:     "main",
				Input:       5,
				Provider:    "other",
				UsageSource: "harness",
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := Reconcile(Sources{Harness: &testCase.fact})
			if !ok {
				t.Fatal("reconcile refused a harness source")
			}
			if got.AnthropicID != "" {
				t.Errorf("AnthropicID = %q, want empty: the record's id is not a request-id header", got.AnthropicID)
			}
			if got.PriceUSD != 0 {
				t.Errorf("PriceUSD = %v, want 0: history does not price these rows either", got.PriceUSD)
			}
			if !reflect.DeepEqual(got, testCase.want) {
				t.Errorf("row =\n %+v\nwant\n %+v", got, testCase.want)
			}
		})
	}
}
