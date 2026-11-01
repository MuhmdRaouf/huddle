package report

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const sentinel = "SENTINEL-0x42"

var t0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

// fakeStores holds what the real stores hold, text included, and hands the builder only the
// structure the small source interfaces ask for.
type fakeStores struct {
	requests    []storedRequest
	sessions    []Session
	tools       []storedTool
	compactions []Compaction
	subagents   []SubagentRun
	asks        []storedAsk
	err         error
}

type storedRequest struct {
	Request
	Prompt, Response string
}

type storedTool struct {
	ToolRun
	Input, Output string
}

type storedAsk struct {
	Ask
	Command string
}

func (f *fakeStores) Requests(context.Context, Window) ([]Request, error) {
	var out []Request
	for _, r := range f.requests {
		out = append(out, r.Request)
	}
	return out, f.err
}

func (f *fakeStores) Sessions(context.Context, Window) ([]Session, error) {
	return f.sessions, f.err
}

func (f *fakeStores) ToolRuns(context.Context, Window) ([]ToolRun, error) {
	var out []ToolRun
	for _, r := range f.tools {
		out = append(out, r.ToolRun)
	}
	return out, f.err
}

func (f *fakeStores) Compactions(context.Context, Window) ([]Compaction, error) {
	return f.compactions, f.err
}

func (f *fakeStores) Subagents(context.Context, Window) ([]SubagentRun, error) {
	return f.subagents, f.err
}

func (f *fakeStores) Asks(context.Context, Window) ([]Ask, error) {
	var out []Ask
	for _, a := range f.asks {
		out = append(out, a.Ask)
	}
	return out, f.err
}

func (f *fakeStores) sources() Sources {
	return Sources{Requests: f, Tools: f, Transcripts: f, Asks: f}
}

func at(d time.Duration) time.Time { return t0.Add(d) }

func req(session, model, provider string, d time.Duration, durMs int64, status int, tok Tokens, cost float64) storedRequest {
	return storedRequest{
		Request: Request{Session: session, Model: model, Provider: provider, At: at(d), DurationMs: durMs, Status: status, Tokens: tok, CostUSD: cost},
		Prompt:  "prompt " + sentinel, Response: "response " + sentinel,
	}
}

func tool(session, name, key string, d time.Duration, durMs int64, ok bool) storedTool {
	return storedTool{
		ToolRun: ToolRun{Session: session, Tool: name, InputKey: key, At: at(d), DurationMs: durMs, OK: ok},
		Input:   "input " + sentinel, Output: "output " + sentinel,
	}
}

func ask(session, decision string, d, wait time.Duration) storedAsk {
	a := Ask{Session: session, Decision: decision, CreatedAt: at(d)}
	if decision != "" {
		a.DecidedAt = at(d + wait)
	}
	return storedAsk{Ask: a, Command: "rm -rf " + sentinel}
}

// fixture is three sessions in two repos, every text field carrying the sentinel.
func fixture() *fakeStores {
	return &fakeStores{
		requests: []storedRequest{
			req("s1", "opus-5.5", "anthropic", 0, 2000, 200, Tokens{Input: 100, Output: 50, CacheRead: 10, CacheWrite: 5}, 0.5),
			req("s1", "opus-5.5", "anthropic", time.Minute, 100, 429, Tokens{}, 0),
			req("s2", "glm-5.3-flash", "zai", 0, 1000, 200, Tokens{Input: 200, Output: 20}, 0.1),
			req("s2", "glm-5.3-flash", "zai", 2*time.Minute, 3000, 503, Tokens{}, 0),
			req("s2", "glm-5.3-flash", "zai", 5*time.Minute, 0, 0, Tokens{}, 0),
		},
		sessions: []Session{
			{ID: "s1", Repo: "/Users/x/" + sentinel + "/proj-a", Start: t0, End: at(2 * time.Hour)},
			{ID: "s2", Repo: "/Users/x/" + sentinel + "/proj-b", Start: t0},
			{ID: "s3", Repo: "/Users/x/" + sentinel + "/proj-a/", Start: t0, End: at(40 * time.Minute)},
		},
		tools: []storedTool{
			tool("s1", "Read", "k1", 30*time.Second, 100, true),
			tool("s1", "Read", "k1", 31*time.Second, 150, true),
			tool("s1", "Read", "k2", 32*time.Second, 50, true),
			tool("s1", "Bash", "k3", 33*time.Second, 1000, true),
			tool("s1", "Bash", "k3", 34*time.Second, 500, false),
			tool("s2", "Bash", "k3", 3*time.Minute, 200, true),
			tool("s2", "Bash", "", 3*time.Minute, 10, true),
			tool("s2", "Bash", "", 3*time.Minute, 10, true),
		},
		compactions: []Compaction{{Session: "s1", At: at(time.Minute)}, {Session: "s1", At: at(2 * time.Minute)}, {Session: "s2", At: at(time.Minute)}},
		subagents: []SubagentRun{
			{Session: "s1", Started: t0, Stopped: at(time.Minute)},
			{Session: "s1", Started: t0},
			{Session: "s2", Started: t0, Stopped: at(time.Minute)},
		},
		asks: []storedAsk{
			ask("s1", "allow", 0, 10*time.Second),
			ask("s1", "deny", time.Minute, 90*time.Second),
			ask("s2", "allow", 0, 45*time.Second),
			ask("s2", "", 0, 0),
		},
	}
}

func build(t *testing.T, f *fakeStores, w Window) (Structure, *Labels) {
	t.Helper()
	s, labels, err := Build(context.Background(), f.sources(), w)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return s, labels
}

func TestSentinelNeverReachesPayload(t *testing.T) {
	s, labels := build(t, fixture(), LastDays(t0, 7))
	payload, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), sentinel) {
		t.Fatalf("the payload carries the sentinel: %s", payload)
	}
	if strings.Contains(string(payload), "/Users") || strings.Contains(string(payload), "proj-") {
		t.Fatalf("the payload carries a repo path: %s", payload)
	}
	for _, label := range []string{`"repo-1"`, `"repo-2"`} {
		if !strings.Contains(string(payload), label) {
			t.Errorf("the payload has no %s", label)
		}
	}
	paths := labels.Repos()
	if len(paths) != 2 || !strings.Contains(paths[0].Value, "proj-a") || !strings.Contains(paths[1].Value, "proj-b") {
		t.Fatalf("the local label map = %v, want proj-a then proj-b", paths)
	}
}

func TestBuildTotalsModelsAndSessions(t *testing.T) {
	s, _ := build(t, fixture(), LastDays(t0, 7))
	if s.Totals.Sessions != 3 || s.Totals.Requests != 5 {
		t.Fatalf("totals = %+v, want 3 sessions and 5 requests", s.Totals)
	}
	if want := (Tokens{Input: 300, Output: 70, CacheRead: 10, CacheWrite: 5}); s.Totals.Tokens != want {
		t.Fatalf("total tokens = %+v, want %+v", s.Totals.Tokens, want)
	}
	if s.Totals.CostUSD != 0.6 {
		t.Fatalf("total cost = %v, want 0.6", s.Totals.CostUSD)
	}
	wantModels := []ModelRow{
		{Model: "glm-5.3-flash", Provider: "zai", Requests: 3, Tokens: Tokens{Input: 200, Output: 20}, CostUSD: 0.1},
		{Model: "opus-5.5", Provider: "anthropic", Requests: 2, Tokens: Tokens{Input: 100, Output: 50, CacheRead: 10, CacheWrite: 5}, CostUSD: 0.5},
	}
	if len(s.Models) != 2 || s.Models[0] != wantModels[0] || s.Models[1] != wantModels[1] {
		t.Fatalf("models = %+v, want %+v", s.Models, wantModels)
	}
	wantSessions := []SessionRow{
		{Label: "session-2", Repo: "repo-2", Requests: 3, Tokens: Tokens{Input: 200, Output: 20}, CostUSD: 0.1, ToolCalls: 3, Subagents: 1},
		{Label: "session-1", Repo: "repo-1", Requests: 2, Tokens: Tokens{Input: 100, Output: 50, CacheRead: 10, CacheWrite: 5}, CostUSD: 0.5, ToolCalls: 5, Subagents: 2},
		{Label: "session-3", Repo: "repo-1"},
	}
	if len(s.Sessions) != 3 {
		t.Fatalf("sessions = %+v, want 3 rows", s.Sessions)
	}
	for i, want := range wantSessions {
		if s.Sessions[i] != want {
			t.Errorf("session row %d = %+v, want %+v", i, s.Sessions[i], want)
		}
	}
}

func TestBuildRequestOutcomesAndTiming(t *testing.T) {
	s, _ := build(t, fixture(), LastDays(t0, 7))
	want := RequestStats{
		Count:      5,
		Outcomes:   Outcomes{OK: 2, RateLimited: 1, ServerError: 1, NoAnswer: 1},
		DurationMs: Timing{Count: 4, MeanMs: 1525, P95Ms: 3000, MaxMs: 3000},
	}
	if s.Requests != want {
		t.Fatalf("requests = %+v, want %+v", s.Requests, want)
	}
	if s.Blocked.Retries != 1 {
		t.Fatalf("retries = %d, want 1", s.Blocked.Retries)
	}
}

func TestBuildToolRowsRankByCalls(t *testing.T) {
	s, _ := build(t, fixture(), LastDays(t0, 7))
	want := []ToolRow{
		{Name: "Bash", Calls: 5, Failures: 1, TotalMs: 1720, MaxMs: 1000},
		{Name: "Read", Calls: 3, TotalMs: 300, MaxMs: 150},
	}
	if len(s.Tools) != 2 || s.Tools[0] != want[0] || s.Tools[1] != want[1] {
		t.Fatalf("tools = %+v, want %+v", s.Tools, want)
	}
	if s.Blocked.FailedTools != 1 {
		t.Fatalf("failed tools = %d, want 1", s.Blocked.FailedTools)
	}
}

func TestBuildAsksCountWaitsAndDenials(t *testing.T) {
	s, _ := build(t, fixture(), LastDays(t0, 7))
	want := AskStats{
		Count: 4, Answered: 3, Unanswered: 1, Waited: 2,
		TotalWaitMs: 145000, LongestWaitMs: 90000, LongestWaitSession: "session-1",
	}
	if s.Blocked.Asks != want {
		t.Fatalf("asks = %+v, want %+v", s.Blocked.Asks, want)
	}
	if s.Waste.Denied != 1 {
		t.Fatalf("denied = %d, want 1", s.Waste.Denied)
	}
}

func TestBuildCountsRereadsApartFromRepeatedCalls(t *testing.T) {
	s, _ := build(t, fixture(), LastDays(t0, 7))
	if s.Blocked.ReReads != 1 {
		t.Fatalf("re-reads = %d, want 1 (Read k1 twice in one session)", s.Blocked.ReReads)
	}
	if s.Waste.RepeatedCalls != 1 {
		t.Fatalf("repeated calls = %d, want 1 (Bash k3 twice in s1; the same key in s2 and empty keys do not count)", s.Waste.RepeatedCalls)
	}
}

func TestBuildCountsCompactionsSubagentsAndIdleSessions(t *testing.T) {
	s, _ := build(t, fixture(), LastDays(t0, 7))
	if s.Waste.Compactions != 3 {
		t.Fatalf("compactions = %d, want 3", s.Waste.Compactions)
	}
	if want := (Subagents{Count: 3, Unfinished: 1}); s.Subagents != want {
		t.Fatalf("subagents = %+v, want %+v", s.Subagents, want)
	}
	// s1 is quiet from 1 min to 2 h (119 min) and s3 for its whole 40 min.
	if s.Waste.IdleSessions != 2 || s.Waste.IdleMs != 159*60*1000 {
		t.Fatalf("idle = %d sessions, %d ms; want 2 sessions, %d ms", s.Waste.IdleSessions, s.Waste.IdleMs, 159*60*1000)
	}
}

func TestBuildNamesTheWindow(t *testing.T) {
	week, _ := build(t, fixture(), LastDays(t0, 7))
	if week.Window != (WindowView{Days: 7}) {
		t.Fatalf("window = %+v, want 7 days", week.Window)
	}
	one, _ := build(t, fixture(), OneSession("s2"))
	if one.Window != (WindowView{Session: "session-2"}) {
		t.Fatalf("window = %+v, want session-2", one.Window)
	}
}

func TestBuildAnEmptyWindowMarshalsEmptyLists(t *testing.T) {
	s, _ := build(t, &fakeStores{}, LastDays(t0, 7))
	payload, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "null") {
		t.Fatalf("the payload has a null: %s", payload)
	}
}

func TestBuildStopsOnASourceError(t *testing.T) {
	boom := errors.New("store closed")
	f := fixture()
	f.err = boom
	if _, _, err := Build(context.Background(), f.sources(), LastDays(t0, 7)); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	a, _ := build(t, fixture(), LastDays(t0, 7))
	b, _ := build(t, fixture(), LastDays(t0, 7))
	pa, _ := json.Marshal(a)
	pb, _ := json.Marshal(b)
	if string(pa) != string(pb) {
		t.Fatalf("two builds differ:\n%s\n%s", pa, pb)
	}
}
