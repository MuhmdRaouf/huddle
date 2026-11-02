package hub

import (
	"context"
	"testing"
	"time"
)

func TestSessionSpansRunFromTheFirstEventToTheEnd(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	for _, rep := range []Report{
		{SessionID: "s-done", LastEvent: "SessionStart", LastEventAt: "2026-10-16T08:00:00.000Z"},
		{SessionID: "s-done", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T08:10:00.000Z"},
		{SessionID: "s-done", LastEvent: "SessionEnd", LastEventAt: "2026-10-16T08:30:00.000Z"},
		{SessionID: "s-live", LastEvent: "SessionStart", LastEventAt: "2026-10-16T09:00:00.000Z"},
		{SessionID: "s-live", LastEvent: "Stop", LastEventAt: "2026-10-16T09:05:00.000Z"},
	} {
		if err := db.Report(ctx, rep); err != nil {
			t.Fatalf("report %s: %v", rep.SessionID, err)
		}
	}
	spans, err := db.SessionSpans(ctx)
	if err != nil {
		t.Fatalf("spans: %v", err)
	}
	at := func(s string) time.Time { return parseEventTime(s) }
	want := map[string]SessionSpan{
		"s-done": {SessionID: "s-done", Start: at("2026-10-16T08:00:00.000Z"), End: at("2026-10-16T08:30:00.000Z")},
		"s-live": {SessionID: "s-live", Start: at("2026-10-16T09:00:00.000Z")},
	}
	if len(spans) != len(want) {
		t.Fatalf("spans = %+v, want %d", spans, len(want))
	}
	for _, span := range spans {
		w := want[span.SessionID]
		if !span.Start.Equal(w.Start) || !span.End.Equal(w.End) {
			t.Errorf("span %s = %s to %s, want %s to %s", span.SessionID, span.Start, span.End, w.Start, w.End)
		}
	}
}

func TestSessionSpansKeepASessionWhoseEventsWereAllSwept(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	for _, rep := range []Report{
		{SessionID: "s-old", LastEvent: "SessionStart", LastEventAt: "2026-10-01T08:00:00.000Z"},
		{SessionID: "s-old", LastEvent: "SessionEnd", LastEventAt: "2026-10-01T09:00:00.000Z"},
	} {
		if err := db.Report(ctx, rep); err != nil {
			t.Fatalf("report: %v", err)
		}
	}
	if n, err := db.PruneEvents(ctx, time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)); err != nil || n != 2 {
		t.Fatalf("prune: %d rows (%v), want both events cut", n, err)
	}
	spans, err := db.SessionSpans(ctx)
	if err != nil {
		t.Fatalf("spans: %v", err)
	}
	if len(spans) != 1 {
		t.Fatalf("spans = %+v, want the swept session's span", spans)
	}
	if want := parseEventTime("2026-10-01T08:00:00.000Z"); !spans[0].Start.Equal(want) {
		t.Errorf("start = %s, want the stored start %s", spans[0].Start, want)
	}
	if want := parseEventTime("2026-10-01T09:00:00.000Z"); !spans[0].End.Equal(want) {
		t.Errorf("end = %s, want %s", spans[0].End, want)
	}
}
