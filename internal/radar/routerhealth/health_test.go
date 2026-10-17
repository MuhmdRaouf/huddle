// The router health reader's own cases: one per exported function, every branch.
package routerhealth_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/routerhealth"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// parseLine reads one spool line from its JSON text, the shape the spool reader hands over.
func parseLine(t *testing.T, text string) shared.SpoolLine {
	t.Helper()
	var line shared.SpoolLine
	if err := json.Unmarshal([]byte(text), &line); err != nil {
		t.Fatalf("parse %s: %v", text, err)
	}
	return line
}

func TestIsRouterEventLine(t *testing.T) {
	t.Run("names itself in event", func(t *testing.T) {
		line := parseLine(t, `{"ts":"2026-01-01T00:00:00.000Z","event":"router.event","plugin":"zai"}`)
		if !routerhealth.IsRouterEventLine(line) {
			t.Error("IsRouterEventLine(event: router.event) = false, want true")
		}
	})

	t.Run("names itself in kind", func(t *testing.T) {
		line := parseLine(t, `{"ts":"2026-01-01T00:00:00.000Z","event":"fallback","kind":"router.event","plugin":"zai"}`)
		if !routerhealth.IsRouterEventLine(line) {
			t.Error("IsRouterEventLine(kind: router.event) = false, want true")
		}
	})

	t.Run("anything else is not one", func(t *testing.T) {
		line := parseLine(t, `{"ts":"2026-01-01T00:00:00.000Z","event":"route","plugin":"zai"}`)
		if routerhealth.IsRouterEventLine(line) {
			t.Error("IsRouterEventLine(route) = true, want false")
		}
	})
}

func TestParseRouterEvent(t *testing.T) {
	t.Run("reads the kind from event when kind names the line", func(t *testing.T) {
		line := parseLine(t, `{"ts":"2026-01-01T00:00:00.000Z","event":"fallback","kind":"router.event",`+
			`"plugin":"zai","reason":"passthrough served it"}`)
		event := routerhealth.ParseRouterEvent(line)
		if event == nil {
			t.Fatal("ParseRouterEvent = nil for a health line")
		}
		want := shared.RouterEventRecord{
			Ts:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli(),
			Plugin: "zai", Event: shared.EventFallback, Reason: "passthrough served it",
		}
		if *event != want {
			t.Errorf("event = %+v, want %+v", event, want)
		}
	})

	t.Run("reads the kind from kind, type, action or name when event names the line", func(t *testing.T) {
		for key, kind := range map[string]shared.RouterEventKind{
			"kind":   shared.EventRateLimited,
			"type":   shared.EventBudgetStop,
			"action": shared.EventRestart,
			"name":   shared.EventRefusal,
		} {
			line := parseLine(t, `{"ts":"2026-01-01T00:00:00.000Z","event":"router.event","plugin":"kimi",`+
				`"`+key+`":"`+string(kind)+`"}`)
			event := routerhealth.ParseRouterEvent(line)
			if event == nil || event.Event != kind {
				t.Errorf("kind from %s = %v, want %s", key, event, kind)
			}
		}
	})

	t.Run("takes the first key that names a known kind: event, then kind, then type, action, name", func(t *testing.T) {
		eventFirst := parseLine(t, `{"ts":"2026-01-01T00:00:00.000Z","event":"fallback","kind":"router.event",`+
			`"type":"refusal","plugin":"zai"}`)
		if event := routerhealth.ParseRouterEvent(eventFirst); event == nil || event.Event != shared.EventFallback {
			t.Errorf("event-key kind = %v, want fallback (the event key wins)", event)
		}
		kindBeforeType := parseLine(t, `{"ts":"2026-01-01T00:00:00.000Z","event":"router.event",`+
			`"kind":"refusal","type":"restart","plugin":"zai"}`)
		if event := routerhealth.ParseRouterEvent(kindBeforeType); event == nil || event.Event != shared.EventRefusal {
			t.Errorf("kind-before-type kind = %v, want refusal", event)
		}
	})

	t.Run("is nil for a line that is not a health event", func(t *testing.T) {
		line := parseLine(t, `{"ts":"2026-01-01T00:00:00.000Z","event":"route","plugin":"zai"}`)
		if event := routerhealth.ParseRouterEvent(line); event != nil {
			t.Errorf("ParseRouterEvent(route line) = %+v, want nil", event)
		}
	})

	t.Run("is nil when no key names a known kind", func(t *testing.T) {
		line := parseLine(t, `{"ts":"2026-01-01T00:00:00.000Z","event":"router.event","kind":"weird","plugin":"zai"}`)
		if event := routerhealth.ParseRouterEvent(line); event != nil {
			t.Errorf("ParseRouterEvent(unknown kind) = %+v, want nil", event)
		}
		line = parseLine(t, `{"ts":"2026-01-01T00:00:00.000Z","event":"router.event","kind":7,"plugin":"zai"}`)
		if event := routerhealth.ParseRouterEvent(line); event != nil {
			t.Errorf("ParseRouterEvent(non-string kind) = %+v, want nil", event)
		}
	})

	t.Run("is nil without a readable ts", func(t *testing.T) {
		noTs := shared.SpoolLine{Event: "router.event", Ts: ""}
		if event := routerhealth.ParseRouterEvent(noTs); event != nil {
			t.Errorf("ParseRouterEvent(empty ts) = %+v, want nil", event)
		}
		notADate := shared.SpoolLine{Event: "router.event", Ts: "not-a-date"}
		if event := routerhealth.ParseRouterEvent(notADate); event != nil {
			t.Errorf("ParseRouterEvent(not a date) = %+v, want nil", event)
		}
	})

	t.Run("is nil without a plugin, or with an empty one", func(t *testing.T) {
		missing := parseLine(t, `{"ts":"2026-01-01T00:00:00.000Z","event":"router.event","kind":"fallback"}`)
		if event := routerhealth.ParseRouterEvent(missing); event != nil {
			t.Errorf("ParseRouterEvent(no plugin) = %+v, want nil", event)
		}
		empty := parseLine(t, `{"ts":"2026-01-01T00:00:00.000Z","event":"router.event","kind":"fallback","plugin":""}`)
		if event := routerhealth.ParseRouterEvent(empty); event != nil {
			t.Errorf("ParseRouterEvent(empty plugin) = %+v, want nil", event)
		}
	})

	t.Run("cuts a long reason at 200 UTF-16 units", func(t *testing.T) {
		line := parseLine(t, `{"ts":"2026-01-01T00:00:00.000Z","event":"router.event","kind":"fallback",`+
			`"plugin":"zai","reason":"`+strings.Repeat("𝔸", 150)+`"}`)
		event := routerhealth.ParseRouterEvent(line)
		if event == nil {
			t.Fatal("ParseRouterEvent = nil")
		}
		// 150 astral letters are 300 UTF-16 units; the cut keeps 100 of them.
		if want := strings.Repeat("𝔸", 100); event.Reason != want {
			t.Errorf("reason is %d UTF-16 units, want 200", sharedUTF16Len(event.Reason))
		}
	})

	t.Run("keeps a model only when the line names one", func(t *testing.T) {
		named := parseLine(t, `{"ts":"2026-01-01T00:00:00.000Z","event":"router.event","kind":"fallback",`+
			`"plugin":"zai","model":"glm-4.7"}`)
		if event := routerhealth.ParseRouterEvent(named); event == nil || event.Model == nil || *event.Model != "glm-4.7" {
			t.Errorf("model = %v, want glm-4.7", named)
		}
		empty := parseLine(t, `{"ts":"2026-01-01T00:00:00.000Z","event":"router.event","kind":"fallback",`+
			`"plugin":"zai","model":""}`)
		if event := routerhealth.ParseRouterEvent(empty); event == nil || event.Model != nil {
			t.Errorf("model = %v, want nil for an empty one", event)
		}
		absent := parseLine(t, `{"ts":"2026-01-01T00:00:00.000Z","event":"router.event","kind":"fallback","plugin":"zai"}`)
		if event := routerhealth.ParseRouterEvent(absent); event == nil || event.Model != nil {
			t.Errorf("model = %v, want nil when absent", event)
		}
	})
}

// sharedUTF16Len counts a string's UTF-16 units, the units the reason cut counts.
func sharedUTF16Len(s string) int {
	units := 0
	for _, r := range s {
		units++
		if r > 0xFFFF {
			units++
		}
	}
	return units
}

func TestHealth(t *testing.T) {
	// event builds one health event at ts; the window cases run over [600, 1000].
	event := func(ts int64, plugin string, kind shared.RouterEventKind) shared.RouterEventRecord {
		return shared.RouterEventRecord{Ts: ts, Plugin: plugin, Event: kind, Reason: "why"}
	}

	t.Run("counts only the events inside the window", func(t *testing.T) {
		events := []shared.RouterEventRecord{
			event(599, "zai", shared.EventFallback),  // before the window: dropped
			event(600, "zai", shared.EventFallback),  // the start itself: dropped
			event(601, "zai", shared.EventFallback),  // in
			event(1000, "zai", shared.EventFallback), // now: in
			event(1001, "zai", shared.EventFallback), // after now: dropped
		}
		health := routerhealth.Health(events, 1000, 400, 4)
		if len(health.Providers) != 1 {
			t.Fatalf("providers = %d, want 1", len(health.Providers))
		}
		if got := health.Providers[0].Counts.Fallback; got != 2 {
			t.Errorf("fallback count = %d, want 2", got)
		}
		if len(health.Recent) != 2 {
			t.Errorf("recent = %d events, want 2", len(health.Recent))
		}
	})

	t.Run("buckets each event by its age, clamping the newest into the last bucket", func(t *testing.T) {
		events := []shared.RouterEventRecord{
			event(650, "zai", shared.EventFallback),  // bucket 0
			event(750, "zai", shared.EventFallback),  // bucket 1
			event(999, "zai", shared.EventFallback),  // bucket 3
			event(1000, "zai", shared.EventFallback), // floor lands past the end: clamped into bucket 3
		}
		health := routerhealth.Health(events, 1000, 400, 4)
		want := []int64{1, 1, 0, 2}
		if len(health.Providers) != 1 {
			t.Fatalf("providers = %d, want 1", len(health.Providers))
		}
		for i, got := range health.Providers[0].Series {
			if got != want[i] {
				t.Errorf("series[%d] = %d, want %d", i, got, want[i])
			}
		}
	})

	t.Run("floors the bucket index when the window does not divide evenly", func(t *testing.T) {
		// window 10 ms over 3 buckets: each bucket is 3⅓ ms wide.
		events := []shared.RouterEventRecord{
			{Ts: 9, Plugin: "zai", Event: shared.EventFallback},  // 9/3⅓ = 2.7 → bucket 2
			{Ts: 10, Plugin: "zai", Event: shared.EventFallback}, // 10/3⅓ = 3 → clamped into bucket 2
		}
		health := routerhealth.Health(events, 10, 10, 3)
		if len(health.Providers) != 1 {
			t.Fatalf("providers = %d, want 1", len(health.Providers))
		}
		want := []int64{0, 0, 2}
		for i, got := range health.Providers[0].Series {
			if got != want[i] {
				t.Errorf("series[%d] = %d, want %d", i, got, want[i])
			}
		}
	})

	t.Run("folds counts and one series row per kind", func(t *testing.T) {
		events := []shared.RouterEventRecord{
			event(601, "zai", shared.EventFallback),
			event(602, "zai", shared.EventRefusal),
			event(603, "zai", shared.EventRateLimited),
			event(604, "zai", shared.EventBudgetStop),
			event(605, "zai", shared.EventRestart),
			event(606, "zai", shared.EventFallback),
		}
		health := routerhealth.Health(events, 1000, 400, 4)
		if len(health.Providers) != 1 {
			t.Fatalf("providers = %d, want 1", len(health.Providers))
		}
		provider := health.Providers[0]
		wantCounts := routerhealth.RouterCounts{Fallback: 2, Refusal: 1, RateLimited: 1, BudgetStop: 1, Restart: 1}
		if provider.Counts != wantCounts {
			t.Errorf("counts = %+v, want %+v", provider.Counts, wantCounts)
		}
		if len(provider.KindSeries) != len(shared.RouterEventKinds) {
			t.Fatalf("kind series rows = %d, want %d", len(provider.KindSeries), len(shared.RouterEventKinds))
		}
		kindTotals := map[shared.RouterEventKind]int64{
			shared.EventFallback: 2, shared.EventRefusal: 1, shared.EventRateLimited: 1,
			shared.EventBudgetStop: 1, shared.EventRestart: 1,
		}
		for i, kind := range shared.RouterEventKinds {
			var got int64
			for _, count := range provider.KindSeries[i] {
				got += count
			}
			if got != kindTotals[kind] {
				t.Errorf("kind series for %s sums to %d, want %d", kind, got, kindTotals[kind])
			}
		}
	})

	t.Run("orders providers busiest first, ties by name", func(t *testing.T) {
		events := []shared.RouterEventRecord{
			event(601, "zai", shared.EventFallback),
			event(602, "kimi", shared.EventRefusal),
			event(603, "kimi", shared.EventRefusal),
			event(604, "deepseek", shared.EventRestart),
		}
		health := routerhealth.Health(events, 1000, 400, 4)
		if len(health.Providers) != 3 {
			t.Fatalf("providers = %d, want 3", len(health.Providers))
		}
		if health.Providers[0].Plugin != "kimi" || health.Providers[1].Plugin != "deepseek" ||
			health.Providers[2].Plugin != "zai" {
			var order []string
			for _, provider := range health.Providers {
				order = append(order, provider.Plugin)
			}
			t.Errorf("provider order = %v, want [kimi deepseek zai]", order)
		}

		tied := []shared.RouterEventRecord{
			event(601, "qwen", shared.EventFallback),
			event(602, "zai", shared.EventFallback),
		}
		health = routerhealth.Health(tied, 1000, 400, 4)
		if health.Providers[0].Plugin != "qwen" || health.Providers[1].Plugin != "zai" {
			t.Errorf("tied order = %s then %s, want alphabetical", health.Providers[0].Plugin, health.Providers[1].Plugin)
		}
	})

	t.Run("breaks total ties the way localeCompare does, not by bytes", func(t *testing.T) {
		tied := []shared.RouterEventRecord{
			event(601, "MiniMax", shared.EventFallback),
			event(602, "minimax", shared.EventFallback),
		}
		health := routerhealth.Health(tied, 1000, 400, 4)
		// one event each: localeCompare weighs case behind the letters, so the lowercase
		// name leads where byte order would put the capital first
		if health.Providers[0].Plugin != "minimax" || health.Providers[1].Plugin != "MiniMax" {
			t.Errorf("tied order = %s then %s, want minimax then MiniMax",
				health.Providers[0].Plugin, health.Providers[1].Plugin)
		}
	})

	t.Run("keeps the newest reason, also on an equal timestamp, and drops an empty one", func(t *testing.T) {
		events := []shared.RouterEventRecord{
			{Ts: 700, Plugin: "zai", Event: shared.EventFallback, Reason: "first"},
			{Ts: 700, Plugin: "zai", Event: shared.EventRefusal, Reason: ""},      // equal ts: the later event wins
			{Ts: 699, Plugin: "zai", Event: shared.EventRestart, Reason: "stale"}, // older: never wins
		}
		health := routerhealth.Health(events, 1000, 400, 4)
		if len(health.Providers) != 1 {
			t.Fatalf("providers = %d, want 1", len(health.Providers))
		}
		provider := health.Providers[0]
		if provider.LastAt == nil || *provider.LastAt != 700 {
			t.Errorf("lastAt = %v, want 700", provider.LastAt)
		}
		if provider.LastReason != nil {
			t.Errorf("lastReason = %v, want nil: the newest reason is empty", *provider.LastReason)
		}

		events = append(events, shared.RouterEventRecord{Ts: 700, Plugin: "zai", Event: shared.EventFallback, Reason: "final"})
		provider = routerhealth.Health(events, 1000, 400, 4).Providers[0]
		if provider.LastReason == nil || *provider.LastReason != "final" {
			t.Errorf("lastReason = %v, want final", provider.LastReason)
		}
	})

	t.Run("caps recent at fifty, newest first", func(t *testing.T) {
		var events []shared.RouterEventRecord
		for i := range 60 {
			events = append(events, event(601+int64(i), "zai", shared.EventFallback))
		}
		health := routerhealth.Health(events, 1000, 400, 4)
		if len(health.Recent) != 50 {
			t.Fatalf("recent = %d events, want 50", len(health.Recent))
		}
		if health.Recent[0].Ts != 660 || health.Recent[49].Ts != 611 {
			t.Errorf("recent spans %d..%d, want 660..611", health.Recent[49].Ts, health.Recent[0].Ts)
		}
	})

	t.Run("echoes the window, the buckets and an empty answer", func(t *testing.T) {
		health := routerhealth.Health(nil, 1000, 86_400_000, 24)
		if health.WindowMs != 86_400_000 || health.Buckets != 24 {
			t.Errorf("window/buckets = %d/%d", health.WindowMs, health.Buckets)
		}
		if len(health.Providers) != 0 || len(health.Recent) != 0 {
			t.Errorf("empty window answered %+v", health)
		}
		encoded, err := json.Marshal(health)
		if err != nil {
			t.Fatal(err)
		}
		if want := `"providers":[],"recent":[]`; !strings.Contains(string(encoded), want) {
			t.Errorf("empty answer = %s, want %s in it", encoded, want)
		}
	})
}
