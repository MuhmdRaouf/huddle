package router

// The usage tracker: an SSE stream read line by line, a JSON answer buffered under its cap, an
// answer of no known type read as nothing. The pipe comes first — nothing here holds a byte the
// client is waiting on.

import (
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"
)

// gzipOf is one body as a gzipped answer carries it.
func gzipOf(body []byte) ([]byte, error) {
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(body); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func TestNewUsageTrackerPicksTheReaderTheAnswerNeeds(t *testing.T) {
	t.Parallel()
	if NewUsageTracker("text/event-stream", "") == nil {
		t.Fatal("an SSE answer got no tracker")
	}
	if NewUsageTracker("application/json", "") == nil {
		t.Fatal("a JSON answer got no tracker")
	}
	if tracker := NewUsageTracker("text/plain", ""); tracker.Usage() != nil {
		t.Errorf("a plain answer's tracker = %v, want the empty one", tracker.Usage())
	}
}

func TestJSONUsageTrackerReadsTheTopLevelUsage(t *testing.T) {
	t.Parallel()
	tracker := NewUsageTracker("application/json", "")
	tracker.Push([]byte(`{"id":"m1","usage":{"input_tokens":10,"output_tokens":2}}`))
	usage := tracker.Usage()
	if usage == nil || usage["input_tokens"] != float64(10) || usage["output_tokens"] != float64(2) {
		t.Fatalf("usage = %v, want both counters", usage)
	}
	// the answer is read once; a second read is the same
	if again := tracker.Usage(); len(again) != len(usage) {
		t.Errorf("the second read = %v, want the same", again)
	}
}

func TestJSONUsageTrackerDropsAnAnswerPastItsCap(t *testing.T) {
	t.Parallel()
	tracker := NewUsageTracker("application/json", "")
	tracker.Push(bytes.Repeat([]byte("x"), usageLimitBytes+1))
	if usage := tracker.Usage(); usage != nil {
		t.Errorf("usage past the cap = %v, want none", usage)
	}
}

func TestJSONUsageTrackerReadsThroughAnEncoding(t *testing.T) {
	t.Parallel()
	gzipped, err := gzipOf([]byte(`{"usage":{"input_tokens":3}}`))
	if err != nil {
		t.Fatal(err)
	}
	tracker := NewUsageTracker("application/json", "gzip")
	tracker.Push(gzipped)
	if usage := tracker.Usage(); usage == nil || usage["input_tokens"] != float64(3) {
		t.Errorf("usage = %v, want the gzip answer's counters", usage)
	}
}

func TestSSEUsageTrackerReadsTheStream(t *testing.T) {
	t.Parallel()
	stream := "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\n" +
		"event: ping\n\n" +
		"data: not json\n\n" +
		"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":4}}\n\n"
	tracker := NewUsageTracker("text/event-stream", "")
	// the lines are pushed in pieces, the way a passing stream arrives
	tracker.Push([]byte(stream[:40]))
	tracker.Push([]byte(stream[40:]))
	usage := tracker.Usage()
	if usage == nil {
		t.Fatal("the stream carried no usage")
	}
	if usage["input_tokens"] != float64(10) || usage["output_tokens"] != float64(4) {
		t.Errorf("usage = %v, want the start's input and the delta's output", usage)
	}
}

func TestSSEUsageTrackerWithoutAUsageAnswersNone(t *testing.T) {
	t.Parallel()
	tracker := NewUsageTracker("text/event-stream", "")
	tracker.Push([]byte("data: {\"type\":\"ping\"}\n\nevent: done\n\npartial line without a newline"))
	if usage := tracker.Usage(); usage != nil {
		t.Errorf("usage = %v, want nil", usage)
	}
}

func TestNestedUsageReadsOnlyARealUsageObject(t *testing.T) {
	t.Parallel()
	if got := nestedUsage(map[string]any{"usage": map[string]any{"input_tokens": 1.0}}); got == nil || got["input_tokens"] != 1.0 {
		t.Errorf("nestedUsage = %v, want the counters", got)
	}
	if got := nestedUsage(map[string]any{"no": "usage"}); got != nil {
		t.Errorf("nestedUsage = %v, want nil", got)
	}
	if got := nestedUsage("a string"); got != nil {
		t.Errorf("nestedUsage = %v, want nil", got)
	}
}

func TestTopLevelUsageAnswersNoneForABodyThatIsNoJSON(t *testing.T) {
	t.Parallel()
	if usage := topLevelUsage([]byte("not json"), ""); usage != nil {
		t.Errorf("usage = %v, want nil", usage)
	}
}

func TestBodyTapFeedsTheTrackerAsItPasses(t *testing.T) {
	t.Parallel()
	var pushed [][]byte
	source := io.NopCloser(strings.NewReader("first second third"))
	tap := &bodyTap{source: source, push: func(chunk []byte) { pushed = append(pushed, append([]byte(nil), chunk...)) }}
	out, err := io.ReadAll(tap)
	if err != nil || string(out) != "first second third" {
		t.Fatalf("the tap's read = %q, %v", out, err)
	}
	if err := tap.Close(); err != nil {
		t.Errorf("the tap's close: %v", err)
	}
	if len(pushed) == 0 {
		t.Error("the tracker saw nothing")
	}
	joined := strings.Join(ofAll(pushed), "")
	if !strings.Contains(joined, "second") {
		t.Errorf("the tracker saw %q, want the whole body", joined)
	}
}

func ofAll(chunks [][]byte) []string {
	out := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		out = append(out, string(chunk))
	}
	return out
}
