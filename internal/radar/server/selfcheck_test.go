package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/history"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// selfCheckAnswer is the self-check route's body as the dashboard reads it.
type selfCheckAnswer struct {
	Day         string          `json:"day"`
	Radar       selfCheckCounts `json:"radar"`
	Transcripts selfCheckCounts `json:"transcripts"`
	DriftPct    *float64        `json:"driftPct"`
}

type selfCheckCounts struct {
	Requests   int64 `json:"requests"`
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
}

// selfCheckLine is one assistant transcript line of the call messageID names.
func selfCheckLine(ts time.Time, messageID string, input, output, cacheRead, cacheWrite int64) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"assistant","sessionId":"s1","message":{"id":%q,`+
		`"model":"claude-opus-5-5","usage":{"input_tokens":%d,"output_tokens":%d,`+
		`"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d}}}`+"\n",
		ts.UTC().Format(time.RFC3339Nano), messageID, input, output, cacheRead, cacheWrite)
}

func writeSelfCheckTranscript(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func selfCheckOf(t *testing.T, check *SelfCheck) selfCheckAnswer {
	t.Helper()
	answer, err := check.Answer(context.Background())
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if answer.Status != 200 {
		t.Fatalf("status = %d, want 200", answer.Status)
	}
	var body selfCheckAnswer
	decodeBody(t, answer, &body)
	return body
}

func TestSelfCheckAnswersBothSumsAndTheDrift(t *testing.T) {
	at := time.Date(2026, 10, 25, 9, 30, 0, 0, time.UTC)
	clockAt := at
	now := func() time.Time { return clockAt }
	projects := filepath.Join(t.TempDir(), "projects")
	transcript := filepath.Join(projects, "-repo", "s1.jsonl")

	h := openTestHistory(t)
	if err := h.UpsertNode(history.NodeUpsert{ID: "s1/main", Kind: shared.NodeMain, SessionID: "s1", AgentID: "main"}); err != nil {
		t.Fatalf("upsert the node: %v", err)
	}
	put := func(id string, ts time.Time, tokens shared.Tokens) {
		record := shared.RequestRecord{ID: id, SessionID: "s1", AgentID: "main", Model: "claude-opus-5-5", Ts: ts.UnixMilli(), Tokens: tokens}
		if err := h.PutRequest("s1/main", record); err != nil {
			t.Fatalf("put the request %s: %v", id, err)
		}
	}
	// Radar holds one of today's two calls, and one of yesterday's the UTC day leaves out
	put("msg_a", at.Add(-time.Hour), shared.Tokens{Input: 100, Output: 20, CacheRead: 1000, CacheWrite: 50})
	put("old", at.Add(-10*time.Hour), shared.Tokens{Input: 5})
	// the transcripts: msg_a written as three block lines, and msg_b Radar never saw
	text := ""
	for range 3 {
		text += selfCheckLine(at.Add(-time.Hour), "msg_a", 100, 20, 1000, 50)
	}
	text += selfCheckLine(at.Add(-time.Minute), "msg_b", 100, 0, 0, 0)
	writeSelfCheckTranscript(t, transcript, text)

	check := NewSelfCheck(h, []string{projects}, now)
	body := selfCheckOf(t, check)
	if body.Day != "2026-10-25" {
		t.Errorf("day = %q, want 2026-10-25", body.Day)
	}
	wantRadar := selfCheckCounts{Requests: 1, Input: 100, Output: 20, CacheRead: 1000, CacheWrite: 50}
	wantTranscripts := selfCheckCounts{Requests: 2, Input: 200, Output: 20, CacheRead: 1000, CacheWrite: 50}
	if body.Radar != wantRadar || body.Transcripts != wantTranscripts {
		t.Fatalf("radar = %+v, transcripts = %+v; want %+v and %+v", body.Radar, body.Transcripts, wantRadar, wantTranscripts)
	}
	// requests and input both miss by half: the worst counter is the drift
	if body.DriftPct == nil || *body.DriftPct != 50 {
		t.Fatalf("driftPct = %v, want 50", body.DriftPct)
	}

	t.Run("answers from its cache for 60 seconds", func(t *testing.T) {
		writeSelfCheckTranscript(t, transcript, text+selfCheckLine(at, "msg_c", 1, 0, 0, 0))
		clockAt = at.Add(59 * time.Second)
		if cached := selfCheckOf(t, check); cached.Transcripts.Requests != 2 {
			t.Errorf("within 60 s: transcripts.requests = %d, want the cached 2", cached.Transcripts.Requests)
		}
		clockAt = at.Add(61 * time.Second)
		if fresh := selfCheckOf(t, check); fresh.Transcripts.Requests != 3 {
			t.Errorf("after 60 s: transcripts.requests = %d, want the fresh 3", fresh.Transcripts.Requests)
		}
	})

	t.Run("no transcripts today leaves the drift null", func(t *testing.T) {
		empty := NewSelfCheck(h, []string{filepath.Join(t.TempDir(), "none")}, now)
		body := selfCheckOf(t, empty)
		if body.DriftPct != nil || body.Transcripts != (selfCheckCounts{}) {
			t.Errorf("driftPct = %v, transcripts = %+v; want null and zeros", body.DriftPct, body.Transcripts)
		}
	})
}
