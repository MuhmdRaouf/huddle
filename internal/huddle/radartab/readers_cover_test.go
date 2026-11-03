package radartab

// The tab's readers over a recorded owner and over Claude Code's transcript tree: what each one
// answers for a body it cannot read, for a session registry's rows and for the transcripts on
// disk.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// proxyingTab answers a tab reading a fake standalone radar that answers every path with body.
func proxyingTab(t *testing.T, body string) *Tab {
	t.Helper()
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(owner.Close)
	return &Tab{target: strings.TrimPrefix(owner.URL, "http://"), client: owner.Client()}
}

// TestEveryReaderAnswersNothingForABodyItCannotRead: an owner that answers JSON of the wrong
// shape gives no requests, no output total, no runs, no tool calls, no running sessions and no
// facts — never a half-read answer.
func TestEveryReaderAnswersNothingForABodyItCannotRead(t *testing.T) {
	tab := proxyingTab(t, `{"requests":1,"events":1,"tools":1,"sessions":1,"session":1}`)
	ctx, since := context.Background(), time.Now().Add(-time.Hour)

	if got := tab.RequestsSince(ctx, since); len(got) != 0 {
		t.Errorf("RequestsSince = %+v, want none", got)
	}
	if got, ok := tab.OutputTokensSince(ctx, "s1", since); ok || got != 0 {
		t.Errorf("OutputTokensSince = (%d, %v), want (0, false)", got, ok)
	}
	if got := tab.SubagentsSince(ctx, "s1", since); len(got) != 0 {
		t.Errorf("SubagentsSince = %+v, want none", got)
	}
	if got := tab.ToolRunsOf(ctx, "s1"); len(got) != 0 {
		t.Errorf("ToolRunsOf = %+v, want none", got)
	}
	if got := tab.RunningSessions(ctx); len(got) != 0 {
		t.Errorf("RunningSessions = %+v, want none", got)
	}
	if got := tab.AgentFactsOf(ctx, "s1"); got == nil || len(got) != 0 {
		t.Errorf("AgentFactsOf = %+v, want an empty map", got)
	}
	if got := tab.FactsOf(ctx, "s1"); got.CostUSD != nil || got.Provider != nil || got.Name != nil {
		t.Errorf("FactsOf = %+v, want every fact nil", got)
	}
}

// TestEveryReaderAnswersNothingWhenTheOwnerIsGone: a recorded owner nobody answers for leaves
// every reader empty-handed, and the tab-less reads keep their "not ok" signal.
func TestEveryReaderAnswersNothingWhenTheOwnerIsGone(t *testing.T) {
	tab := &Tab{target: "127.0.0.1:1", client: &http.Client{Timeout: probeTimeout}}
	ctx, since := context.Background(), time.Now().Add(-time.Hour)

	if got, ok := tab.OutputTokensSince(ctx, "s1", since); ok || got != 0 {
		t.Errorf("OutputTokensSince = (%d, %v), want (0, false)", got, ok)
	}
	if got := tab.SubagentsSince(ctx, "", since); len(got) != 0 {
		t.Errorf("SubagentsSince = %+v, want none", got)
	}
	if got := tab.ToolRunsOf(ctx, "s1"); len(got) != 0 {
		t.Errorf("ToolRunsOf = %+v, want none", got)
	}
	if got := tab.RunningSessions(ctx); len(got) != 0 {
		t.Errorf("RunningSessions = %+v, want none", got)
	}
}

// TestRequestsSinceLeavesOutAStatusThatIsNotANumber: a request whose recorded status is not an
// HTTP status number says nothing about a lane and stays out beside the readable ones.
func TestRequestsSinceLeavesOutAStatusThatIsNotANumber(t *testing.T) {
	tab := proxyingTab(t, `{"requests":[
		{"provider":"Moonshot","ts":2000,"status":"timeout"},
		{"provider":"Moonshot","ts":1000,"status":"503"}]}`)
	requests := tab.RequestsSince(context.Background(), time.UnixMilli(0))
	if len(requests) != 1 || requests[0].Status != 503 || requests[0].Provider != "Moonshot" {
		t.Fatalf("the requests = %+v, want the one Moonshot 503", requests)
	}
}

// TestAgentFactsOfKeepsTheFirstRowWhenTheCallCountsTie: two spellings of one agent with the same
// number of calls leave the first row in the answer standing.
func TestAgentFactsOfKeepsTheFirstRowWhenTheCallCountsTie(t *testing.T) {
	tab := proxyingTab(t, `{"session":{"agents":[
		{"id":"agent-a1","kind":"subagent","requests":2,"tokens":{"output":11}},
		{"id":"a1","kind":"subagent","requests":2,"tokens":{"output":99}}]},"requests":[]}`)
	facts := tab.AgentFactsOf(context.Background(), "s1")
	if len(facts) != 1 || facts["a1"].OutputTokens != 11 {
		t.Fatalf("the facts = %+v, want a1 with the first row's 11 output tokens", facts)
	}
}

// TestFactsOfSumsTheCostOfEveryPricedAgent: a session's cost is the sum over the agents that
// carry a price; an agent without one adds nothing, and the provider follows the session's model.
func TestFactsOfSumsTheCostOfEveryPricedAgent(t *testing.T) {
	tab := proxyingTab(t, `{"session":{"model":"glm-5.3","name":"Fix the router","agents":[
		{"costUsd":0.25},{"costUsd":null},{"costUsd":0.5}]}}`)
	facts := tab.FactsOf(context.Background(), "s1")
	if facts.CostUSD == nil || *facts.CostUSD != 0.75 {
		t.Fatalf("the cost = %v, want 0.75", facts.CostUSD)
	}
	if facts.Provider == nil || *facts.Provider != "Z.ai" {
		t.Errorf("the provider = %v, want Z.ai", facts.Provider)
	}
	if facts.Name == nil || *facts.Name != "Fix the router" {
		t.Errorf("the name = %v, want Fix the router", facts.Name)
	}
}

// TestRunningSessionsNamesOnlyTheLiveSessionsWithAWorkingDir: a session is running when radar
// marks it live; the outside session, an external one, a subagent's own session and one with no
// working dir are left out.
func TestRunningSessionsNamesOnlyTheLiveSessionsWithAWorkingDir(t *testing.T) {
	tab := proxyingTab(t, `{"sessions":[
		{"id":"s-live","cwd":"/w/app","live":true},
		{"id":"s-ended","cwd":"/w/old","live":false},
		{"id":"s-ext","cwd":"/w/ext","live":true,"external":true},
		{"id":"s-child","cwd":"/w/child","live":true,"parentSessionId":"s-live"},
		{"id":"s-nowhere","live":true},
		{"id":"s-empty","cwd":"","live":true},
		{"id":"s-live-2","cwd":"/w/api","live":true}]}`)
	got := tab.RunningSessions(context.Background())
	want := []RunningSession{{ID: "s-live", Cwd: "/w/app"}, {ID: "s-live-2", Cwd: "/w/api"}}
	if len(got) != len(want) {
		t.Fatalf("the running sessions = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("running session %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestTheRootPathReachesTheOwnersRoot: a read of the bare prefix goes to a standalone owner's
// root, and a mount with no client of its own still reads.
func TestTheRootPathReachesTheOwnersRoot(t *testing.T) {
	var seen string
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Path
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(owner.Close)
	tab := &Tab{target: strings.TrimPrefix(owner.URL, "http://")}
	if _, ok := tab.RadarAnswer(context.Background(), Prefix); !ok {
		t.Fatal("the owner's root did not answer")
	}
	if seen != "/" {
		t.Errorf("the owner saw %q, want /", seen)
	}
}

// TestRadarAnswerRefusesAPathThatIsNotAURL: neither a proxying nor an owning mount reads a path
// that no request can be built for.
func TestRadarAnswerRefusesAPathThatIsNotAURL(t *testing.T) {
	bad := "/api/summary\x7f"
	proxying := &Tab{target: "127.0.0.1:1"}
	if _, ok := proxying.RadarAnswer(context.Background(), bad); ok {
		t.Error("a proxying mount answered an unbuildable path")
	}
	env := tempEnv(t)
	owning, _, _ := mountMux(t, env, MountOptions{IntervalMs: quietMs, InsightsTickMs: quietMs})
	if !owning.Owns {
		t.Fatal("the mount did not take the lock")
	}
	if _, ok := owning.RadarAnswer(context.Background(), bad); ok {
		t.Error("an owning mount answered an unbuildable path")
	}
}

// transcriptTab answers a tab whose Claude config dir holds the given project dirs.
func transcriptTab(t *testing.T) (*Tab, string) {
	t.Helper()
	root := t.TempDir()
	return &Tab{env: domain.EnvLookup{"CLAUDE_CONFIG_DIR": root, "HOME": root}}, filepath.Join(root, "projects")
}

func writeTranscript(t *testing.T, projects, project, id string, modified time.Time) {
	t.Helper()
	dir := filepath.Join(projects, project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(file, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, modified, modified); err != nil {
		t.Fatal(err)
	}
}

// TestTranscriptTimesAnswersTheNewestChangePerSession: each named session answers the newest
// modification time among its transcripts across the project dirs; a session with no transcript
// is absent, and files that are not the named transcripts are never read.
func TestTranscriptTimesAnswersTheNewestChangePerSession(t *testing.T) {
	tab, projects := transcriptTab(t)
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	writeTranscript(t, projects, "-w-app", "s1", base)
	writeTranscript(t, projects, "-w-api", "s1", base.Add(10*time.Minute))
	writeTranscript(t, projects, "-w-app", "s2", base.Add(5*time.Minute))
	writeTranscript(t, projects, "-w-app", "other", base.Add(30*time.Minute))
	if err := os.WriteFile(filepath.Join(projects, "stray.jsonl"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// a directory named like a wanted transcript is no transcript
	if err := os.MkdirAll(filepath.Join(projects, "-w-app", "s3.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := tab.TranscriptTimes([]string{"s1", "s2", "s3", "s-none"})
	if len(got) != 2 {
		t.Fatalf("the transcript times = %+v, want s1 and s2 alone", got)
	}
	if !got["s1"].Equal(base.Add(10 * time.Minute)) {
		t.Errorf("s1 = %s, want the newer of its two transcripts %s", got["s1"], base.Add(10*time.Minute))
	}
	if !got["s2"].Equal(base.Add(5 * time.Minute)) {
		t.Errorf("s2 = %s, want %s", got["s2"], base.Add(5*time.Minute))
	}
}

// TestTranscriptTimesAnswersNothingWithoutIdsOrATree: no ids, or a Claude config dir with no
// projects tree, answers an empty map rather than failing.
func TestTranscriptTimesAnswersNothingWithoutIdsOrATree(t *testing.T) {
	tab, _ := transcriptTab(t)
	if got := tab.TranscriptTimes(nil); got == nil || len(got) != 0 {
		t.Errorf("no ids = %+v, want an empty map", got)
	}
	if got := tab.TranscriptTimes([]string{"s1"}); got == nil || len(got) != 0 {
		t.Errorf("no projects tree = %+v, want an empty map", got)
	}
}
