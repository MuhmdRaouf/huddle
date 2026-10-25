package ingest_test

// Anthropic's unified limits reach the summary: the newest route line that carried them wins,
// and a later line without them leaves them as they were.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRouteLinesFeedTheSummaryTheirUnifiedUtilization(t *testing.T) {
	t.Parallel()
	limits := func(fiveHour, sevenDay float64) map[string]any {
		return map[string]any{
			"five_hour": map[string]any{"used": fiveHour, "resets_at": 1792951800},
			"seven_day": map[string]any{"used": sevenDay, "resets_at": 1793030400},
		}
	}
	route := func(ts int64, utilization map[string]any) map[string]any {
		line := map[string]any{
			"ts": iso(ts), "event": "route", "plugin": "huddle-router", "session_id": "s9",
			"model": "claude-opus-5-5", "upstream": "api.anthropic.com", "route": "anthropic",
			"status": 200, "latency_ms": 40,
		}
		if utilization != nil {
			line["utilization"] = utilization
		}
		return line
	}
	e := makeEnv(t)
	// the newest line that carried the headers sits first in the file; the line after it in
	// time carried none
	writeText(t, spoolFilePath(t, e.env), jsonl(t,
		route(watchT1, limits(0.42, 0.61)),
		route(watchT0, limits(0.30, 0.55)),
		route(watchT1+500, nil),
	))
	st := newStore()
	manualWatcher(t, e.env, st)

	data, err := json.Marshal(st.Summary())
	if err != nil {
		t.Fatal(err)
	}
	want := `"utilization":{"five_hour":{"used":0.42,"resets_at":1792951800},"seven_day":{"used":0.61,"resets_at":1793030400}}`
	if !strings.Contains(string(data), want) {
		t.Errorf("the summary = %s, want it to carry the newest utilization %s", data, want)
	}
}

func TestTheSummaryCarriesNoUtilizationBeforeAnAnthropicAnswer(t *testing.T) {
	t.Parallel()
	e := makeEnv(t)
	writeText(t, spoolFilePath(t, e.env), jsonl(t, map[string]any{
		"ts": iso(watchT1), "event": "route", "session_id": "s9", "model": "glm-5.3",
		"upstream": "http://127.0.0.1:8787", "route": "provider", "status": 200, "latency_ms": 42,
	}))
	st := newStore()
	manualWatcher(t, e.env, st)
	data, err := json.Marshal(st.Summary())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "utilization") {
		t.Errorf("the summary = %s, want no utilization before an Anthropic answer carried one", data)
	}
}
