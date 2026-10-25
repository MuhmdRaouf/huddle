// The spool reader's cases, plus the round trip with the lines the routers themselves write.
package ingest_test

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/spool"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/routerhealth"
)

// nowMs is the pinned now the window cases run at.
func nowMs() int64 {
	return time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC).UnixMilli()
}

// writeSpool drops one dated spool file under the env's state dir.
func writeSpool(t *testing.T, env domain.EnvLookup, name string, lines []string) string {
	t.Helper()
	return writeText(t, filepath.Join(env["RADAR_HOME"], "spool", name), strings.Join(lines, "\n")+"\n")
}

func TestListSpoolFiles(t *testing.T) {
	t.Run("returns nothing when the spool dir does not exist yet", func(t *testing.T) {
		e := makeEnv(t)
		if got := ingest.ListSpoolFiles(t.Context(), e.env, nowMs(), 7); len(got) != 0 {
			t.Errorf("ListSpoolFiles(no dir) = %v, want none", got)
		}
	})

	t.Run("keeps datestamp files inside the window, oldest first, as full paths", func(t *testing.T) {
		e := makeEnv(t)
		writeSpool(t, e.env, "2026-01-14.jsonl", []string{"{}"})
		writeSpool(t, e.env, "2026-01-08.jsonl", []string{"{}"})
		writeSpool(t, e.env, "2026-01-07.jsonl", []string{"{}"}) // exactly cutoff - 1 day: still kept
		got := ingest.ListSpoolFiles(t.Context(), e.env, nowMs(), 7)
		want := []string{
			filepath.Join(e.state, "spool", "2026-01-07.jsonl"),
			filepath.Join(e.state, "spool", "2026-01-08.jsonl"),
			filepath.Join(e.state, "spool", "2026-01-14.jsonl"),
		}
		if !slices.Equal(got, want) {
			t.Errorf("ListSpoolFiles = %v, want %v", got, want)
		}
	})

	t.Run("drops files older than the window and names that are not datestamps", func(t *testing.T) {
		e := makeEnv(t)
		writeSpool(t, e.env, "2026-01-06.jsonl", []string{"{}"})
		writeSpool(t, e.env, "2025-12-25.jsonl", []string{"{}"})
		writeSpool(t, e.env, "notes.txt", []string{"{}"})
		writeSpool(t, e.env, "yesterday.jsonl", []string{"{}"})
		if got := ingest.ListSpoolFiles(t.Context(), e.env, nowMs(), 7); len(got) != 0 {
			t.Errorf("ListSpoolFiles = %v, want none", got)
		}
	})

	t.Run("honours a custom day window", func(t *testing.T) {
		e := makeEnv(t)
		writeSpool(t, e.env, "2026-01-12.jsonl", []string{"{}"})
		writeSpool(t, e.env, "2026-01-01.jsonl", []string{"{}"})
		got := ingest.ListSpoolFiles(t.Context(), e.env, nowMs(), 3)
		want := []string{filepath.Join(e.state, "spool", "2026-01-12.jsonl")}
		if !slices.Equal(got, want) {
			t.Errorf("ListSpoolFiles(3 days) = %v, want %v", got, want)
		}
	})
}

func TestParseSpoolLine(t *testing.T) {
	t.Run("accepts an object with string ts and event", func(t *testing.T) {
		got, ok := ingest.ParseSpoolLine(`{"ts":"2026-01-01T00:00:00.000Z","event":"Stop"}`)
		if !ok {
			t.Fatal("ParseSpoolLine rejected a well-formed line")
		}
		if got.Ts != "2026-01-01T00:00:00.000Z" || got.Event != "Stop" {
			t.Errorf("ts/event = %q/%q", got.Ts, got.Event)
		}
		if got.SessionID != nil || got.Model != nil || got.Reason != nil || len(got.Extra) != 0 {
			t.Errorf("line carried more than ts and event: %+v extra=%v", got, got.Extra)
		}
	})

	t.Run("rejects missing fields, arrays, scalars and broken JSON", func(t *testing.T) {
		for name, line := range map[string]string{
			"no event":        `{"event":"Stop"}`,
			"no ts":           `{"ts":"2026-01-01T00:00:00.000Z"}`,
			"ts not a string": `{"ts":true,"event":"Stop"}`,
			"an array":        "[1,2]",
			"a string":        `"line"`,
			"broken json":     "{not json",
			"empty":           "",
		} {
			if _, ok := ingest.ParseSpoolLine(line); ok {
				t.Errorf("ParseSpoolLine(%s) accepted the line", name)
			}
		}
	})

	t.Run("reads an epoch-millisecond ts (a router may stamp one) as ISO", func(t *testing.T) {
		got, ok := ingest.ParseSpoolLine(`{"ts":1767225600000,"event":"router.event"}`)
		if !ok {
			t.Fatal("ParseSpoolLine rejected an epoch-ms line")
		}
		if got.Ts != "2026-01-01T00:00:00.000Z" {
			t.Errorf("ts = %q, want 2026-01-01T00:00:00.000Z", got.Ts)
		}
	})

	t.Run("keeps unknown fields for forward compatibility", func(t *testing.T) {
		got, ok := ingest.ParseSpoolLine(`{"ts":"2026-01-01T00:00:00.000Z","event":"route","wat":true}`)
		if !ok {
			t.Fatal("ParseSpoolLine rejected a line with an unknown field")
		}
		if string(got.Extra["wat"]) != "true" {
			t.Errorf("wat = %v, want true", got.Extra["wat"])
		}
	})

	t.Run("keeps the usage counters a mistyped counter does not touch", func(t *testing.T) {
		got, ok := ingest.ParseSpoolLine(`{"ts":"2026-01-01T00:00:00.000Z","event":"route",` +
			`"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":"x"}}`)
		if !ok {
			t.Fatal("ParseSpoolLine rejected a line with one odd usage counter")
		}
		if got.Usage == nil || got.Usage.InputTokens == nil || *got.Usage.InputTokens != 10 ||
			got.Usage.OutputTokens == nil || *got.Usage.OutputTokens != 5 {
			t.Fatalf("usage = %+v, want input 10 and output 5 kept", got.Usage)
		}
		if got.Usage.CacheReadInputTokens != nil {
			t.Errorf("cache_read_input_tokens = %d, want unset", *got.Usage.CacheReadInputTokens)
		}
	})

	t.Run("keeps every key in the line's own order, a numeric status included", func(t *testing.T) {
		raw := `{"ts":"2026-01-01T00:00:00.000Z","event":"route","plugin":"zai","model":"glm-5.3",` +
			`"status":200,"future_field":{"a":[1,2]},"latency_ms":150}`
		got, ok := ingest.ParseSpoolLine(raw)
		if !ok {
			t.Fatal("ParseSpoolLine rejected a route line with a numeric status")
		}
		if got.Status != nil {
			t.Errorf("status = %q, want absent: a number is not the string the field holds", *got.Status)
		}
		payload, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if string(payload) != raw {
			t.Errorf("payload = %s, want the line as read: %s", payload, raw)
		}
	})

	t.Run("an epoch ts reads as ISO in place, the rest of the line keeping its order", func(t *testing.T) {
		got, ok := ingest.ParseSpoolLine(`{"kind":"router.event","plugin":"zai","event":"refusal",` +
			`"reason":"no key set","model":null,"ts":1767225600000}`)
		if !ok {
			t.Fatal("ParseSpoolLine rejected an epoch-ms line")
		}
		payload, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		want := `{"kind":"router.event","plugin":"zai","event":"refusal",` +
			`"reason":"no key set","model":null,"ts":"2026-01-01T00:00:00.000Z"}`
		if string(payload) != want {
			t.Errorf("payload = %s, want %s", payload, want)
		}
	})
}

func TestReadSpoolLines(t *testing.T) {
	t.Run("reads every parseable line across files in order and skips the rest", func(t *testing.T) {
		e := makeEnv(t)
		writeSpool(t, e.env, "2026-01-01.jsonl", []string{
			`{"ts":"2026-01-01T00:00:01.000Z","event":"SessionStart"}`,
			"garbage",
			`{"ts":"2026-01-01T00:00:02.000Z","event":"Stop"}`,
		})
		writeSpool(t, e.env, "2026-01-02.jsonl", []string{`{"ts":"2026-01-02T00:00:00.000Z","event":"Notification"}`})
		lines := ingest.ReadSpoolLines(ingest.ListSpoolFiles(t.Context(), e.env, nowMs(), 30))
		var events []string
		for _, line := range lines {
			events = append(events, line.Event)
		}
		if !slices.Equal(events, []string{"SessionStart", "Stop", "Notification"}) {
			t.Errorf("events = %v", events)
		}
	})

	t.Run("skips unreadable files without failing the readable ones", func(t *testing.T) {
		e := makeEnv(t)
		writeSpool(t, e.env, "2026-01-02.jsonl", []string{`{"ts":"2026-01-02T00:00:00.000Z","event":"Stop"}`})
		paths := []string{filepath.Join(e.env["RADAR_HOME"], "spool", "2026-01-01.jsonl")}
		paths = append(paths, ingest.ListSpoolFiles(t.Context(), e.env, nowMs(), 30)...)
		if got := ingest.ReadSpoolLines(paths); len(got) != 1 {
			t.Errorf("ReadSpoolLines = %d lines, want 1", len(got))
		}
	})
}

// TestRouterSpoolRoundTrip proves one definition of the spool line format: a line the
// plugin-core routers append through internal/adapters/spool is read back by the radar reader,
// as a route event and as a router health event.
func TestRouterSpoolRoundTrip(t *testing.T) {
	e := makeEnv(t)
	at := time.Date(2026, 1, 15, 9, 30, 0, 0, time.UTC).UnixMilli()
	writer := spool.CreateSpoolWriter(e.env, nil, func() int64 { return at })
	ttfb := int64(180)
	ttft := int64(420)
	writer(domain.RouteEvent{
		Ts: "2026-01-15T09:30:00.000Z", Event: domain.EventRoute, Plugin: "zai", Model: "glm-4.7",
		Upstream: "https://api.z.ai/api/anthropic", Route: "provider", Status: 200, LatencyMs: 150,
		TtfbMs: &ttfb, TtftMs: &ttft,
	})
	writer(domain.HealthEvent("zai", domain.HealthRefusal, "no key set", "", at))

	files := ingest.ListSpoolFiles(t.Context(), e.env, nowMs(), 7)
	if len(files) != 1 || filepath.Base(files[0]) != "2026-01-15.jsonl" {
		t.Fatalf("spool files = %v, want the one dated file", files)
	}
	lines := ingest.ReadSpoolLines(files)
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2 (the route line and the health line)", len(lines))
	}

	route := lines[0]
	if route.Event != "route" || route.Model == nil || *route.Model != "glm-4.7" {
		t.Errorf("route line = %+v", route)
	}
	if route.LatencyMs == nil || *route.LatencyMs != 150 {
		t.Errorf("route latency_ms = %v, want 150", route.LatencyMs)
	}
	if route.TtfbMs == nil || *route.TtfbMs != 180 || route.TtftMs == nil || *route.TtftMs != 420 {
		t.Errorf("route ttfb_ms/ttft_ms = %v/%v, want 180/420", route.TtfbMs, route.TtftMs)
	}
	record := ingest.RouteRequest(route, "glm-4.7", at)
	if record.TtfbMs == nil || *record.TtfbMs != 180 || record.TtftMs == nil || *record.TtftMs != 420 {
		t.Errorf("route record ttfb/ttft = %v/%v, want 180/420", record.TtfbMs, record.TtftMs)
	}
	if got := string(route.Extra["plugin"]); got != `"zai"` {
		t.Errorf("route plugin = %s, want zai", got)
	}

	health := routerhealth.ParseRouterEvent(lines[1])
	if health == nil {
		t.Fatalf("the health line the router wrote is not a router event: %+v", lines[1])
	}
	wantAt := time.Date(2026, 1, 15, 9, 30, 0, 0, time.UTC).UnixMilli()
	if health.Ts != wantAt || health.Plugin != "zai" || health.Event != "refusal" ||
		health.Reason != "no key set" || health.Model != nil {
		t.Errorf("health event = %+v", health)
	}
}
