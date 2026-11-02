package ingest_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/testkit"
)

// The cost of one route line on the catch-up path. A busy machine's spool holds a route line
// for most calls, and the catch-up reads them all in one pass, so what each line allocates
// and what each record keeps set the hub's resident size while it catches up.

// busyRouteLine is the route line a router writes for call i: the numeric status routers
// write, the usage block, and the request id with, on two calls in five, the other nine
// allow-listed response headers.
func busyRouteLine(t *testing.T, i int) string {
	t.Helper()
	headers := map[string]string{"request-id": fmt.Sprintf("req-busy-%05d", i)}
	if i%5 < 2 {
		headers["content-type"] = "application/json"
		headers["anthropic-ratelimit-requests-limit"] = "1000"
		headers["anthropic-ratelimit-requests-remaining"] = fmt.Sprintf("%03d", 999-i%700)
		headers["anthropic-ratelimit-requests-reset"] = fmt.Sprintf("%d", 1_768_435_200+i)
		headers["anthropic-ratelimit-tokens-limit"] = "12000000"
		headers["anthropic-ratelimit-tokens-remaining"] = fmt.Sprintf("%07d", 11_000_000-i)
		headers["anthropic-ratelimit-tokens-reset"] = fmt.Sprintf("%d", 1_768_435_200+i)
		headers["x-ratelimit-remaining"] = fmt.Sprintf("%03d", 99-i%100)
		headers["x-ratelimit-reset"] = fmt.Sprintf("%d", 1_768_431_600+i)
	}
	text, err := json.Marshal(map[string]any{
		"ts": "2026-01-15T00:00:00.000Z", "event": "route", "session_id": fmt.Sprintf("busy-%02d", i%50),
		"model": "glm-5.3", "plugin": "zai", "route": "direct", "upstream": "https://api.z.ai/api/anthropic",
		"status": 200, "latency_ms": 400 + i%3000, "headers": headers,
		"usage": map[string]any{"input_tokens": 100 + i%97, "output_tokens": 10 + i%37},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(text)
}

// parsedBusyRouteLine is busyRouteLine as the watcher parses it.
func parsedBusyRouteLine(t *testing.T, i int) shared.SpoolLine {
	t.Helper()
	line, ok := ingest.ParseSpoolLine(busyRouteLine(t, i))
	if !ok {
		t.Fatalf("route line %d did not parse", i)
	}
	return line
}

// Judging a route line that answered 200 reads the status the parse kept and allocates
// nothing: the watcher judges each line before it builds the request, and decoding the line
// again for every reader cost a hundred allocations and several KB a line.
func TestJudgingASuccessfulRouteLineAllocatesNothing(t *testing.T) {
	if testkit.RaceEnabled {
		t.Skip("the race detector adds allocations of its own")
	}
	line := parsedBusyRouteLine(t, 0)
	at := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC).UnixMilli()
	allocs := testing.AllocsPerRun(100, func() {
		if _, failed := ingest.RouteFailure(line, at); failed {
			t.Fatal("a 200 answer was judged a failure")
		}
	})
	t.Logf("judging a 200 route line allocates %.0f times", allocs)
	if allocs != 0 {
		t.Errorf("judging a 200 route line allocates %.0f times, want none", allocs)
	}
}
