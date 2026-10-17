package ingest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/routerhealth"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// The contract with the routers that write radar's spool: the lines a router appends and
// the records radar's parser must read out of each one. The fixtures live in
// testdata/contract and the cc-plugins repository keeps an identical copy, so every byte
// of a change is reviewed twice.

// contractFixtureDir is the fixtures' directory, relative to this package.
const contractFixtureDir = "../../../testdata/contract"

// The moment every fixture line names, as the router wrote it and as epoch ms.
const (
	contractTs   = "2026-10-07T09:00:00.000Z"
	contractTsMs = int64(1791363600000)
)

// The one session, agent, parent and prompt the fixture lines name: fixed ids, so the
// bytes only change when the schema does.
const (
	contractSessionID  = "b1c0ffee-0000-4000-8000-000000000001"
	contractAgentID    = "b1c0ffee-0000-4000-8000-000000000002"
	contractParentID   = "b1c0ffee-0000-4000-8000-000000000003"
	contractPromptHash = "5f1b1c4d2e3f4a5b6c7d8e9f0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d"
)

// contractFixtureLines reads one fixture's lines, in order.
func contractFixtureLines(t *testing.T, name string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(contractFixtureDir, name))
	if err != nil {
		t.Fatalf("reading testdata/contract/%s: %v", name, err)
	}
	text := strings.TrimRight(string(data), "\n")
	if text == "" {
		t.Fatalf("testdata/contract/%s holds no lines", name)
	}
	return strings.Split(text, "\n")
}

// contractTextField is a line's value for a key when it is a string, else "".
func contractTextField(line shared.SpoolLine, key string) string {
	value := line.Field(key)
	if value == nil || value.Kind() != jsfmt.KindString {
		return ""
	}
	return value.Text()
}

// TestContractSpoolLinesParse reads every fixture line through radar's parser and holds
// each one against the record the parser must produce: the route line's exact model,
// status, latency and token counters, the capture's prompt, and every health line's kind,
// reason and model.
func TestContractSpoolLinesParse(t *testing.T) {
	t.Run("spool-route.jsonl", func(t *testing.T) {
		lines := contractFixtureLines(t, "spool-route.jsonl")
		if len(lines) != 4 {
			t.Fatalf("spool-route.jsonl holds %d lines, want 4", len(lines))
		}
		parsed := make([]shared.SpoolLine, len(lines))
		for i, line := range lines {
			record, ok := ingest.ParseSpoolLine(line)
			if !ok {
				t.Fatalf("line %d did not parse: %s", i, line)
			}
			if record.Ts != contractTs {
				t.Errorf("line %d ts = %q, want %s", i, record.Ts, contractTs)
			}
			parsed[i] = record
		}
		start, routed, capture, stop := parsed[0], parsed[1], parsed[2], parsed[3]

		if start.Event != "router" || contractTextField(start, "state") != "start" ||
			contractTextField(start, "plugin") != "zai" || start.Field("port").EncodeJSON() != "18787" {
			t.Errorf("line 1 = event %q state %q plugin %q port %s, want the router coming up on 18787 as zai",
				start.Event, contractTextField(start, "state"), contractTextField(start, "plugin"), start.Field("port").EncodeJSON())
		}
		if stop.Event != "router" || contractTextField(stop, "state") != "stop" {
			t.Errorf("line 4 = event %q state %q, want the router stopping", stop.Event, contractTextField(stop, "state"))
		}
		if capture.Event != "capture" || capture.PromptHash == nil || *capture.PromptHash != contractPromptHash ||
			capture.Gz == nil || *capture.Gz == "" {
			t.Errorf("line 3 = event %q hash %v gz %v, want the routed prompt's capture",
				capture.Event, capture.PromptHash, capture.Gz)
		}

		// the route line's record: the exact model, status, latency and tokens the router
		// wrote, under the session, agent and parent the line named
		if routed.Event != "route" || routed.Model == nil || *routed.Model != "glm-5.3" {
			t.Fatalf("line 2 = event %q model %v, want a route line on glm-5.3", routed.Event, routed.Model)
		}
		ts, ok := jsfmt.ParseISO(routed.Ts)
		if !ok || ts.UnixMilli() != contractTsMs {
			t.Fatalf("line 2 ts %q = %d ms, want %d", routed.Ts, ts.UnixMilli(), contractTsMs)
		}
		record := ingest.RouteRequest(routed, *routed.Model, ts.UnixMilli())
		wantID := "route:" + contractSessionID + ":" + contractAgentID + ":glm-5.3:1791363600000:200"
		if record.ID != wantID {
			t.Errorf("route record id = %q, want %q (model, status and ts live in the id)", record.ID, wantID)
		}
		if record.StopReason != nil {
			t.Errorf("route record stop reason = %q, want none: 200 is a success", *record.StopReason)
		}
		if record.LatencyMs == nil || *record.LatencyMs != 4123 {
			t.Errorf("route record latency = %v, want 4123", record.LatencyMs)
		}
		wantTokens := shared.Tokens{Input: 4, Output: 212, CacheRead: 13598}
		if record.Tokens != wantTokens {
			t.Errorf("route record tokens = %+v, want %+v", record.Tokens, wantTokens)
		}
		if record.Ts != contractTsMs {
			t.Errorf("route record ts = %d, want %d", record.Ts, contractTsMs)
		}
		if record.Upstream != "https://api.z.ai/api/anthropic" {
			t.Errorf("route record upstream = %q, want the provider base URL", record.Upstream)
		}
		if record.Route == nil || *record.Route != "provider" || record.Via == nil || *record.Via != "zai" {
			t.Errorf("route record route %v via %v, want provider via zai", record.Route, record.Via)
		}
		if record.Provider != "Z.ai" {
			t.Errorf("route record provider = %q, want Z.ai", record.Provider)
		}
		if record.ParentAgentID == nil || *record.ParentAgentID != contractParentID ||
			record.PromptHash == nil || *record.PromptHash != contractPromptHash {
			t.Errorf("route record parent %v hash %v, want %s and %s",
				record.ParentAgentID, record.PromptHash, contractParentID, contractPromptHash)
		}
	})

	t.Run("spool-health.jsonl", func(t *testing.T) {
		lines := contractFixtureLines(t, "spool-health.jsonl")
		if len(lines) != 5 {
			t.Fatalf("spool-health.jsonl holds %d lines, want 5", len(lines))
		}
		type want struct {
			event  shared.RouterEventKind
			reason string
			model  string // "" when the line named none
		}
		table := []want{
			{shared.EventFallback, "dial tcp 1.2.3.4:443: i/o timeout: served by the passthrough", ""},
			{shared.EventRefusal, "upstream 401: invalid api key", "glm-5.3"},
			{shared.EventRateLimited, "upstream 429: rate limit reached", "glm-5.3"},
			{shared.EventBudgetStop, "budget stop for provider:zai", "glm-5.3"},
			{shared.EventRestart, "front silent for 5s: replaced", ""},
		}
		for i, line := range lines {
			parsed, ok := ingest.ParseSpoolLine(line)
			if !ok {
				t.Fatalf("line %d did not parse: %s", i+1, line)
			}
			if parsed.Ts != contractTs {
				t.Errorf("line %d ts = %q, want %s: the epoch-ms ts reads as ISO", i+1, parsed.Ts, contractTs)
			}
			event := routerhealth.ParseRouterEvent(parsed)
			if event == nil {
				t.Fatalf("line %d became no router event: %s", i+1, line)
			}
			expected := table[i]
			if event.Ts != contractTsMs || event.Plugin != "zai" || event.Event != expected.event || event.Reason != expected.reason {
				t.Errorf("line %d record = %+v, want ts %d, plugin zai, event %s, reason %q",
					i+1, *event, contractTsMs, expected.event, expected.reason)
			}
			if expected.model == "" && event.Model != nil {
				t.Errorf("line %d model = %q, want none", i+1, *event.Model)
			}
			if expected.model != "" && (event.Model == nil || *event.Model != expected.model) {
				t.Errorf("line %d model = %v, want %s", i+1, event.Model, expected.model)
			}
		}
	})
}
