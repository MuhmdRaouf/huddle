package main

// `huddle insights run` end to end: the real binary reads a seeded ledger and history read-only,
// builds the structure, asks the lane huddle-router serves once, and writes the run to the home.
// The lane is a fake on a free port named by HUDDLE_ROUTER_PORT; no real provider is called.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/insights/report"
	"github.com/MuhmdRaouf/huddle/internal/radar/history"
	"github.com/MuhmdRaouf/huddle/internal/radar/ledger"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

const insightsSentinel = "SENTINEL-0x42"

// insightsLane is a fake huddle-router lane: it counts the requests it gets and keeps their bodies.
type insightsLane struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (l *insightsLane) calls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.bodies)
}

func (l *insightsLane) last(t *testing.T) []byte {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.bodies) == 0 {
		t.Fatal("the lane got no request")
	}
	return l.bodies[len(l.bodies)-1]
}

// insightsWorld is a temp home, radar home and router home, and a fake lane on a free port.
type insightsWorld struct {
	env  map[string]string
	home string
	lane *insightsLane
	ctx  context.Context
}

func newInsightsWorld(t *testing.T, enabled ...string) *insightsWorld {
	t.Helper()
	lane := &insightsLane{}
	port := ports.Free(t)
	listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("listen for the fake lane: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		lane.mu.Lock()
		lane.bodies = append(lane.bodies, body)
		lane.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"report text"}],"usage":{"input_tokens":4321,"output_tokens":123}}`)
	}))
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)

	routerHome := t.TempDir()
	list, err := json.Marshal(map[string][]string{"providers": enabled})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(routerHome, "providers.json"), list, 0o600); err != nil {
		t.Fatal(err)
	}
	env := argvLayer(argvBase(t), map[string]string{
		"HUDDLE_ROUTER_HOME": routerHome,
		"HUDDLE_ROUTER_PORT": strconv.Itoa(port),
	})
	return &insightsWorld{env: env, home: env["HUDDLE_HOME"], lane: lane, ctx: context.Background()}
}

func (w *insightsWorld) lookup() map[string]string { return isoenv.Lookup(argvEntries(w.env)) }

// seedFew files three requests whose captured text, repo path and tool input carry the sentinel.
func (w *insightsWorld) seedFew(t *testing.T) {
	t.Helper()
	st, err := ledger.Open(paths.LedgerPath(w.ctx, w.lookup()))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	what := "prompt: " + insightsSentinel
	for i := 0; i < 3; i++ {
		if err := st.Upsert(w.ctx, ledger.Row{
			ID: fmt.Sprintf("r-%d", i), StartedAt: now.Add(-time.Duration(i+1) * time.Hour).UnixMilli(), DurationMs: 90, Status: 200,
			Model: "glm-5.3", Provider: "zai", SessionID: "s-a", Input: 5_000, Output: 200, PriceUSD: 0.002, What: &what,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	h, err := history.OpenHistory(paths.HistoryPath(w.ctx, w.lookup()), nil)
	if err != nil {
		t.Fatal(err)
	}
	repo, started := "/Users/me/"+insightsSentinel+"/huddle", now.Add(-2*time.Hour).UnixMilli()
	if err := h.UpsertNode(history.NodeUpsert{ID: "n-a", Kind: shared.NodeMain, SessionID: "s-a", AgentID: "main", Repo: &repo, StartedAt: &started}); err != nil {
		t.Fatal(err)
	}
	if err := h.PutTool("n-a", shared.ToolCallRecord{ID: "t-1", SessionID: "s-a", Name: "Bash", StartedAt: started, Ok: true}); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
}

// seedMany files one request for each of count sessions, spread over the last six days: the
// structure of that many sessions is far over the 150k-token cap.
func (w *insightsWorld) seedMany(t *testing.T, count int) {
	t.Helper()
	st, err := ledger.Open(paths.LedgerPath(w.ctx, w.lookup()))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for n := 0; n < count; n++ {
		started := now.Add(-time.Hour - time.Duration(n%6)*24*time.Hour).UnixMilli()
		if err := st.Upsert(w.ctx, ledger.Row{
			ID: fmt.Sprintf("r-%05d", n), StartedAt: started, DurationMs: 90, Status: 200, Model: "glm-5.3", Provider: "zai",
			SessionID: fmt.Sprintf("s-%05d", n), Input: int64(1000 + n), Output: 200, PriceUSD: 0.002,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

// runs reads the run files the process wrote under the home.
func (w *insightsWorld) runs(t *testing.T) []report.Run {
	t.Helper()
	dir := filepath.Join(w.home, "insights", "runs")
	runs, err := report.NewRunStore(dir).List(10)
	if err != nil {
		t.Fatalf("list the runs: %v", err)
	}
	for _, r := range runs {
		info, err := os.Stat(filepath.Join(dir, r.ID+".json"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("run file %s is %v (%v), want mode 0600", r.ID, info, err)
		}
	}
	return runs
}

// laneRequest is the Anthropic-shaped body the run sends.
type laneRequest struct {
	Model    string `json:"model"`
	System   string `json:"system"`
	Messages []struct {
		Content string `json:"content"`
	} `json:"messages"`
}

func decodeLaneRequest(t *testing.T, raw []byte) laneRequest {
	t.Helper()
	var req laneRequest
	if err := json.Unmarshal(raw, &req); err != nil || len(req.Messages) != 1 {
		t.Fatalf("the lane request is %.200s (%v), want one message", raw, err)
	}
	return req
}

func TestE2EInsightsRunUsesTheLane(t *testing.T) {
	t.Run("one call carries the structure and nothing of the session text", func(t *testing.T) {
		w := newInsightsWorld(t, "zai")
		w.seedFew(t)
		if n := w.lane.calls(); n != 0 {
			t.Fatalf("the lane counted %d calls before the run, want 0", n)
		}

		code, out, errOut := argvRun(t, w.env, t.TempDir(), "insights", "run")
		if code != 0 {
			t.Fatalf("insights run exited %d: %s %s", code, out, errOut)
		}
		if n := w.lane.calls(); n != 1 {
			t.Fatalf("the lane counted %d calls after the run, want exactly 1", n)
		}
		raw := w.lane.last(t)
		if strings.Contains(string(raw), insightsSentinel) || strings.Contains(string(raw), "/Users") {
			t.Errorf("the request carries session text or a path: %.300s", raw)
		}
		req := decodeLaneRequest(t, raw)
		var payload struct {
			Totals struct{ Sessions, Requests int } `json:"totals"`
		}
		if err := json.Unmarshal([]byte(req.Messages[0].Content), &payload); err != nil || payload.Totals.Requests != 3 || payload.Totals.Sessions != 1 {
			t.Errorf("the message is %.200s (%v), want the structure of 3 requests in 1 session", req.Messages[0].Content, err)
		}
		if req.Model != "glm-5.3" {
			t.Errorf("model = %q, want GLM's default glm-5.3", req.Model)
		}
		runs := w.runs(t)
		if len(runs) != 1 || runs[0].Status != report.RunDone || runs[0].Answer != "report text" ||
			runs[0].Provider != "zai" || runs[0].InputTokens != 4321 || runs[0].OutputTokens != 123 {
			t.Errorf("runs = %+v, want one done run with the lane's answer and usage", runs)
		}
	})

	t.Run("a 300k-token window folds into days and sends at most 150k", func(t *testing.T) {
		const sessions = 7000
		row, err := json.Marshal(report.SessionRow{Label: "session-7000", Requests: 1, Tokens: report.Tokens{Input: 8000, Output: 200}, CostUSD: 0.002})
		if err != nil {
			t.Fatal(err)
		}
		if whole := sessions * report.EstimateTokens(row); whole < 300_000 {
			t.Fatalf("the whole structure is about %d tokens, want a window of at least 300k", whole)
		}
		w := newInsightsWorld(t, "zai")
		w.seedMany(t, sessions)

		code, out, errOut := argvRun(t, w.env, t.TempDir(), "insights", "run")
		if code != 0 {
			t.Fatalf("insights run exited %d: %s %s", code, out, errOut)
		}
		if n := w.lane.calls(); n != 1 {
			t.Fatalf("the lane counted %d calls, want exactly 1", n)
		}
		req := decodeLaneRequest(t, w.lane.last(t))
		sent := report.EstimateTokens([]byte(req.System + req.Messages[0].Content))
		if sent > report.MaxInputTokens {
			t.Errorf("the run sent about %d tokens, want at most %d", sent, report.MaxInputTokens)
		}
		var payload struct {
			Totals struct{ Sessions int } `json:"totals"`
			Days   []struct{ Day string } `json:"days"`
		}
		if err := json.Unmarshal([]byte(req.Messages[0].Content), &payload); err != nil || payload.Totals.Sessions != sessions || len(payload.Days) != 6 {
			t.Errorf("the payload has %d sessions and %d days (%v), want all %d in the totals and 6 days", payload.Totals.Sessions, len(payload.Days), err, sessions)
		}
	})

	t.Run("a run with no provider but Anthropic is refused before any call", func(t *testing.T) {
		w := newInsightsWorld(t)
		w.seedFew(t)
		code, _, errOut := argvRun(t, w.env, t.TempDir(), "insights", "run")
		if code == 0 || !strings.Contains(errOut, "No provider is enabled") {
			t.Errorf("exit %d, stderr %q; want a refusal that says no provider is enabled", code, errOut)
		}
		if n := w.lane.calls(); n != 0 {
			t.Errorf("the lane counted %d calls, want 0", n)
		}
		if runs := w.runs(t); len(runs) != 1 || runs[0].Status != report.RunFailed {
			t.Errorf("runs = %+v, want one failed run", runs)
		}
	})

	t.Run("a Claude model is refused before any call", func(t *testing.T) {
		w := newInsightsWorld(t, "zai")
		w.seedFew(t)
		code, _, errOut := argvRun(t, w.env, t.TempDir(), "insights", "run", "--model", "claude-opus-5.5")
		if code == 0 || errOut == "" {
			t.Errorf("exit %d, stderr %q; want a refusal", code, errOut)
		}
		if n := w.lane.calls(); n != 0 {
			t.Errorf("the lane counted %d calls, want 0", n)
		}
	})
}
