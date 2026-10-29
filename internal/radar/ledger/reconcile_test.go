package ledger

import (
	"reflect"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// The reconcile scenarios: one request seen by up to three sources, each field read from
// the first source that has it.

func strp(text string) *string { return &text }

func i64p(n int64) *int64 { return &n }

func f64p(f float64) *float64 { return &f }

func TestReconcileFillsEveryFieldFromItsSource(t *testing.T) {
	t.Run("all three sources, the route winning each field it has", func(t *testing.T) {
		sources := Sources{
			Route: &RouteFacts{
				Event: domain.RouteEvent{
					Ts:            "2026-10-25T12:00:00.123Z",
					Event:         domain.EventRoute,
					Plugin:        "zai",
					Model:         "glm-5.3-flash",
					Upstream:      "api.z.ai",
					Route:         "provider",
					Status:        200,
					LatencyMs:     4123,
					TtfbMs:        i64p(21),
					TtftMs:        i64p(280),
					SessionID:     strp("ses_1"),
					AgentID:       strp("agt_1"),
					ParentAgentID: strp("par_1"),
					Usage: &domain.RouteUsage{
						"input_tokens":                float64(120),
						"output_tokens":               float64(45),
						"cache_read_input_tokens":     float64(7),
						"cache_creation_input_tokens": float64(9),
					},
					Headers:     &map[string]string{"request-id": "req_01"},
					PromptHash:  strp("hash1"),
					PromptID:    strp("pr_1"),
					PrevRequest: strp("req_00"),
					TurnIndex:   i64p(3),
					Fallback:    strp("claude-sonnet-5-5"),
					Utilization: &domain.UnifiedUtilization{
						FiveHour: &domain.UnifiedWindow{Used: 0.25},
						SevenDay: &domain.UnifiedWindow{Used: 0.5},
					},
				},
				PriceUSD: 0.0042,
			},
			Transcript: &TranscriptFacts{
				RequestID:   "req_01",
				PromptID:    "pr_from_transcript",
				QuerySource: "user",
				StopReason:  "end_turn",
				Usage:       domain.RouteUsage{"input_tokens": float64(11), "output_tokens": float64(5)},
			},
			OTel: &OTelFacts{
				RequestID:      "req_01",
				DurationMs:     4200,
				TtftMs:         i64p(300),
				HarnessCostUSD: f64p(0.0123),
				Usage:          domain.RouteUsage{"input_tokens": float64(999), "output_tokens": float64(888)},
			},
		}
		want := Row{
			ID:             "req_01",
			AnthropicID:    "req_01",
			StartedAt:      time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC).Add(123 * time.Millisecond).UnixMilli(),
			TtfbMs:         i64p(21),
			TtftMs:         i64p(280),
			DurationMs:     4123,
			Lane:           "provider:zai",
			Harness:        "claude",
			Model:          "glm-5.3-flash",
			FallbackFrom:   "claude-sonnet-5-5",
			Upstream:       "api.z.ai",
			SessionID:      "ses_1",
			AgentID:        "agt_1",
			ParentAgentID:  "par_1",
			PromptID:       "pr_1",
			TurnIndex:      i64p(3),
			PrevRequest:    "req_00",
			QuerySource:    "user",
			Outcome:        "ok",
			Status:         200,
			StopReason:     "end_turn",
			Input:          120,
			Output:         45,
			CacheRead:      7,
			CacheWrite:     9,
			Headers:        map[string]string{"request-id": "req_01"},
			Provider:       "Z.ai",
			Via:            strp("zai"),
			NodeID:         "ses_1/agt_1",
			UsageSource:    "router",
			PriceUSD:       0.0042,
			HarnessCostUSD: f64p(0.0123),
			Ratelimit5h:    f64p(0.25),
			Ratelimit7d:    f64p(0.5),
			PromptHash:     "hash1",
			InTranscript:   true,
		}
		got, ok := Reconcile(sources)
		if !ok {
			t.Fatal("reconcile refused sources that know the request")
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("row =\n %+v\nwant\n %+v", got, want)
		}
	})

	t.Run("route only", func(t *testing.T) {
		got, ok := Reconcile(Sources{
			Route: &RouteFacts{
				Event: domain.RouteEvent{
					Ts:         "2026-10-25T09:30:00.500Z",
					Event:      domain.EventRoute,
					Plugin:     "kimi",
					Model:      "kimi-k2",
					Upstream:   "api.moonshot.ai",
					Route:      "provider",
					Status:     200,
					LatencyMs:  890,
					Headers:    &map[string]string{"request-id": "req_02"},
					Usage:      &domain.RouteUsage{"input_tokens": int64(30), "output_tokens": int64(12)},
					PromptHash: strp("hash2"),
					PromptID:   strp("pr_2"),
				},
				PriceUSD: 0.0011,
			},
		})
		want := Row{
			ID:          "req_02",
			AnthropicID: "req_02",
			StartedAt:   time.Date(2026, 10, 25, 9, 30, 0, 0, time.UTC).Add(500 * time.Millisecond).UnixMilli(),
			DurationMs:  890,
			Lane:        "provider:kimi",
			Harness:     "claude",
			Model:       "kimi-k2",
			Upstream:    "api.moonshot.ai",
			SessionID:   "outside-a-session",
			AgentID:     "main",
			PromptID:    "pr_2",
			Outcome:     "ok",
			Status:      200,
			Input:       30,
			Output:      12,
			Headers:     map[string]string{"request-id": "req_02"},
			Provider:    "Moonshot",
			Via:         strp("kimi"),
			NodeID:      "outside-a-session/main",
			UsageSource: "router",
			PriceUSD:    0.0011,
			PromptHash:  "hash2",
		}
		if !ok {
			t.Fatal("reconcile refused a route source")
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("row =\n %+v\nwant\n %+v", got, want)
		}
	})

	t.Run("otel and transcript without a route", func(t *testing.T) {
		got, ok := Reconcile(Sources{
			Transcript: &TranscriptFacts{
				RequestID:   "req_03",
				PromptID:    "pr_3",
				QuerySource: "subagent",
				StopReason:  "tool_use",
				Usage:       domain.RouteUsage{"input_tokens": float64(500)},
			},
			OTel: &OTelFacts{
				RequestID:      "req_03",
				DurationMs:     2100,
				TtftMs:         i64p(140),
				HarnessCostUSD: f64p(0.008),
				Usage:          domain.RouteUsage{"input_tokens": float64(480), "output_tokens": int64(90)},
			},
		})
		want := Row{
			ID:             "req_03",
			AnthropicID:    "req_03",
			TtftMs:         i64p(140),
			DurationMs:     2100,
			Harness:        "claude",
			SessionID:      "outside-a-session",
			AgentID:        "main",
			PromptID:       "pr_3",
			QuerySource:    "subagent",
			StopReason:     "tool_use",
			Input:          480,
			Output:         90,
			Provider:       "other",
			UsageSource:    "otel",
			HarnessCostUSD: f64p(0.008),
			InTranscript:   true,
		}
		if !ok {
			t.Fatal("reconcile refused the otel and transcript sources")
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("row =\n %+v\nwant\n %+v", got, want)
		}
	})

	t.Run("no sources", func(t *testing.T) {
		row, ok := Reconcile(Sources{})
		if ok {
			t.Errorf("reconcile answered %#v for empty sources, want false", row)
		}
	})

	t.Run("a refused route", func(t *testing.T) {
		got, ok := Reconcile(Sources{
			Route: &RouteFacts{
				Event: domain.RouteEvent{
					Ts:         "2026-10-25T14:10:00Z",
					Event:      domain.EventRoute,
					Plugin:     "zai",
					Model:      "glm-5.3-flash",
					Route:      "refused",
					Status:     400,
					Error:      strp("insufficient credits"),
					PromptHash: strp("hash5"),
				},
			},
		})
		want := Row{
			ID:         "hash5:2026-10-25T14:10:00Z",
			StartedAt:  time.Date(2026, 10, 25, 14, 10, 0, 0, time.UTC).UnixMilli(),
			Lane:       "refused",
			Harness:    "claude",
			Model:      "glm-5.3-flash",
			SessionID:  "outside-a-session",
			AgentID:    "main",
			Outcome:    "refused",
			Status:     400,
			ErrorCode:  "insufficient credits",
			Provider:   "Z.ai",
			Via:        strp("zai"),
			NodeID:     "outside-a-session/main",
			PromptHash: "hash5",
		}
		if !ok {
			t.Fatal("reconcile refused a route source")
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("row =\n %+v\nwant\n %+v", got, want)
		}
	})
}

// TestReconcileStartsARowWhenItsTranscriptCarriesTheTs: a request no route saw starts when
// its transcript says; the route's own start wins over the transcript's, and a transcript
// without a ts starts nothing.
func TestReconcileStartsARowWhenItsTranscriptCarriesTheTs(t *testing.T) {
	routeStart := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC).UnixMilli()
	transcript := func(ts int64) *TranscriptFacts { return &TranscriptFacts{RequestID: "req_ts", Ts: ts} }
	route := &RouteFacts{Event: domain.RouteEvent{
		Ts:      "2026-10-25T12:00:00Z",
		Event:   domain.EventRoute,
		Model:   "glm-5.3",
		Route:   "provider",
		Status:  200,
		Headers: &map[string]string{"request-id": "req_ts"},
	}}
	cases := []struct {
		name    string
		sources Sources
		want    int64
	}{
		{"a transcript-only request starts when its transcript says", Sources{Transcript: transcript(1730000000000)}, 1730000000000},
		{"a route's start wins over the transcript's ts", Sources{Route: route, Transcript: transcript(1730000000000)}, routeStart},
		{"a transcript without a ts starts nothing", Sources{Transcript: transcript(0)}, 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			row, ok := Reconcile(testCase.sources)
			if !ok {
				t.Fatal("reconcile refused sources that know the request")
			}
			if row.StartedAt != testCase.want {
				t.Errorf("StartedAt = %d, want %d", row.StartedAt, testCase.want)
			}
		})
	}
}

// TestReconcileBuildsARowFromHistoryAlone: a history.db row no live source filed still
// builds a row — the lane read from its route and via, the session and agent defaults the
// request rule applies, and the failure told by status and error code: a row whose stop
// reason is its status text keeps none.
func TestReconcileBuildsARowFromHistoryAlone(t *testing.T) {
	got, ok := Reconcile(Sources{History: &HistoryFacts{
		ID:         "route:s1:main:glm-5.3:1787400000000:429",
		Route:      "provider",
		Via:        strp("zai"),
		Status:     "429",
		StopReason: "429",
		Error:      "rate limited",
	}})
	want := Row{
		ID:         "route:s1:main:glm-5.3:1787400000000:429",
		Lane:       "provider:zai",
		Harness:    "claude",
		SessionID:  shared.OutsideSession,
		AgentID:    "main",
		Outcome:    "http_error",
		Status:     429,
		ErrorCode:  "rate limited",
		Provider:   "other",
		Via:        strp("zai"),
		StopReason: "",
	}
	if !ok {
		t.Fatal("reconcile refused a history source")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("row =\n %+v\nwant\n %+v", got, want)
	}
}

// TestReconcileReadsAHistoryCopy: the copy of a transcript request history stored carries
// its tokens and stop reason and the fact it came from a transcript, and names no lane or
// outcome — history never stored those for it.
func TestReconcileReadsAHistoryCopy(t *testing.T) {
	got, ok := Reconcile(Sources{History: &HistoryFacts{
		ID:             "msg_sub1",
		StopReason:     "end_turn",
		Input:          10,
		Output:         5,
		FromTranscript: true,
	}})
	want := Row{
		ID:           "msg_sub1",
		Harness:      "claude",
		SessionID:    shared.OutsideSession,
		AgentID:      "main",
		StopReason:   "end_turn",
		Input:        10,
		Output:       5,
		Provider:     "other",
		UsageSource:  "history",
		InTranscript: true,
	}
	if !ok {
		t.Fatal("reconcile refused a history source")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("row =\n %+v\nwant\n %+v", got, want)
	}
}

// TestReconcileTakesTheRequestIDHeaderFromHistory: the request-id header the row's headers
// JSON held becomes the row's AnthropicID; the id the facts file under stays its own.
func TestReconcileTakesTheRequestIDHeaderFromHistory(t *testing.T) {
	got, ok := Reconcile(Sources{History: &HistoryFacts{
		ID:      "route:s1:main:glm-5.3:1787400001000:200",
		Headers: map[string]string{"request-id": "req_h1"},
	}})
	if !ok {
		t.Fatal("reconcile refused a history source")
	}
	if got.AnthropicID != "req_h1" {
		t.Errorf("AnthropicID = %q, want req_h1", got.AnthropicID)
	}
	if got.ID != "route:s1:main:glm-5.3:1787400001000:200" {
		t.Errorf("ID = %q, want the facts' own id", got.ID)
	}
}

// TestLiveFactsRuleAndHistoryFillsTheRest: a transcript and the history row of one call
// build one row — the transcript's tokens, session and id rule as a group, history fills
// only what the live source never spoke for.
func TestLiveFactsRuleAndHistoryFillsTheRest(t *testing.T) {
	got, ok := Reconcile(Sources{
		Transcript: &TranscriptFacts{
			RequestID:  "req_t1",
			StopReason: "end_turn",
			SessionID:  "s1",
			Usage:      domain.RouteUsage{"input_tokens": float64(7)},
		},
		History: &HistoryFacts{
			ID:     "req_t1",
			Route:  "provider",
			Via:    strp("zai"),
			Status: "200",
			Speed:  strp("fast"),
			Input:  99,
			NodeID: "n1",
		},
	})
	want := Row{
		ID:           "req_t1",
		AnthropicID:  "req_t1",
		Lane:         "provider:zai",
		Harness:      "claude",
		SessionID:    "s1",
		AgentID:      "main",
		Outcome:      "ok",
		Status:       200,
		StopReason:   "end_turn",
		Input:        7,
		Speed:        strp("fast"),
		Provider:     "other",
		Via:          strp("zai"),
		NodeID:       "n1",
		UsageSource:  "transcript",
		InTranscript: true,
	}
	if !ok {
		t.Fatal("reconcile refused the transcript and history sources")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("row =\n %+v\nwant\n %+v", got, want)
	}
}
