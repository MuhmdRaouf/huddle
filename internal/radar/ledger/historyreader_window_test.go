package ledger

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/radar/history"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// The history reader's window reads: the session and subagent nodes, the tool calls and the
// compaction events of a window, all of it with no page cap.

func int64Ptr(n int64) *int64 { return &n }

// windowHistory writes a history.db with two main nodes, one subagent, a job, 1300 tool calls
// from 1000 up, and a compaction, a prompt and a subagent-stop event.
func windowHistory(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "history.db")
	h, err := history.OpenHistory(file, nil)
	if err != nil {
		t.Fatal(err)
	}
	nodes := []history.NodeUpsert{
		{ID: "n1", Kind: shared.NodeMain, SessionID: "s1", AgentID: "main", Repo: strp("/work/a"), Cwd: strp("/work/a/sub"),
			StartedAt: int64Ptr(1_000), EndedAt: history.EndAt(5_000)},
		{ID: "n2", Kind: shared.NodeMain, SessionID: "s2", AgentID: "main", Cwd: strp("/work/b"),
			StartedAt: int64Ptr(2_000)},
		{ID: "n3", Kind: shared.NodeMain, SessionID: "old", AgentID: "main", StartedAt: int64Ptr(10), EndedAt: history.EndAt(20)},
		{ID: "n4", Kind: shared.NodeSubagent, SessionID: "s1", AgentID: "a1", ParentID: strp("n1"),
			StartedAt: int64Ptr(1_500), EndedAt: history.EndAt(1_900)},
		{ID: "n5", Kind: shared.NodeJob, SessionID: "job-1", AgentID: "main", StartedAt: int64Ptr(1_500)},
	}
	for _, n := range nodes {
		if err := h.UpsertNode(n); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 1300; i++ {
		tool := shared.ToolCallRecord{
			ID: fmt.Sprintf("t%05d", i), SessionID: "s1", Name: "Bash", StartedAt: 1_000 + int64(i),
			DurationMs: int64Ptr(7), Ok: i%2 == 0, InputKey: strp("k"),
		}
		if err := h.PutTool("n1", tool); err != nil {
			t.Fatal(err)
		}
	}
	events := []shared.EventRecord{
		{Seq: 1, Ts: 1_100, Kind: "PreCompact", SessionID: strp("s1")},
		{Seq: 2, Ts: 1_200, Kind: "UserPromptSubmit", SessionID: strp("s1")},
		{Seq: 3, Ts: 9_000, Kind: "PreCompact", SessionID: strp("s1")},
	}
	for _, e := range events {
		if err := h.AddEvent(nil, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestHistoryReaderReadsTheSessionsOfAWindow(t *testing.T) {
	r := openHistoryReader(t, windowHistory(t))
	got, err := r.SessionNodes(context.Background(), 1_000, 4_000, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []NodeFact{
		{SessionID: "s1", Kind: "main", Repo: "/work/a", StartedAt: 1_000, EndedAt: 5_000},
		{SessionID: "s2", Kind: "main", Repo: "/work/b", StartedAt: 2_000},
		{SessionID: "s1", Kind: "subagent", StartedAt: 1_500, EndedAt: 1_900},
	}
	if len(got) != len(want) {
		t.Fatalf("read %d nodes %+v, want the two main sessions and the subagent", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("node %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestHistoryReaderReadsEveryToolCallOfAWindow(t *testing.T) {
	r := openHistoryReader(t, windowHistory(t))
	got, err := r.ToolFacts(context.Background(), 1_000, 2_999, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1300 {
		t.Fatalf("read %d tool calls, want all 1300", len(got))
	}
	if got[0] != (ToolFact{Session: "s1", Name: "Bash", InputKey: "k", StartedAt: 1_000, DurationMs: 7, OK: true}) {
		t.Errorf("first tool call = %+v", got[0])
	}
	early, err := r.ToolFacts(context.Background(), 1_000, 1_099, "")
	if err != nil || len(early) != 100 {
		t.Fatalf("a 100 ms window read %d calls (%v), want 100", len(early), err)
	}
}

func TestHistoryReaderReadsOnlyCompactionEvents(t *testing.T) {
	r := openHistoryReader(t, windowHistory(t))
	got, err := r.Compactions(context.Background(), 1_000, 2_000, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (CompactionFact{Session: "s1", At: 1_100}) {
		t.Fatalf("compactions = %+v, want the one PreCompact inside the window", got)
	}
}
