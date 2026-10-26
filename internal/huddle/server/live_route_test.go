package server

// The session live route (live_route.go): one GET naming the session's running subagents and
// the running background shells of the main and of each of them, the live tray above the
// sessions screen's prompt. Every test runs its own server child against its own transcript
// files.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/convo"
	"github.com/MuhmdRaouf/huddle/internal/huddle/radartab"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// the literal agents and shells the tray unit tests lay out: a main, a finished worker a1, a
// running worker b2, an ended and a running shell on the main, a running shell on a1 and one on
// b2
func trayAgents() []convo.Agent {
	return []convo.Agent{
		{ID: convo.MainAgent, Type: "main", Running: true, StartedAt: "2026-10-26T10:00:00.000Z"},
		{ID: "a1", Type: "general-purpose", Description: "Worker A", Depth: 1, StartedAt: "2026-10-26T10:00:01.000Z"},
		{ID: "b2", Type: "general-purpose", Description: "Worker B", Depth: 1, Running: true, StartedAt: "2026-10-26T10:00:07.000Z"},
	}
}

func trayShells() map[string][]convo.Shell {
	return map[string][]convo.Shell{
		convo.MainAgent: {
			{ID: "b4io7vyqm", Command: "go test ./...", Description: "Run the gates", State: "ended", StartedAt: "2026-10-26T10:00:01.000Z"},
			{ID: "k7w2n9x4d1", Command: "mise run check", Description: "Wait for the gates", State: "running", StartedAt: "2026-10-26T10:00:03.000Z"},
		},
		"a1": {
			{ID: "q5f8t3h6j0", Command: "sleep 30", State: "running", StartedAt: "2026-10-26T10:00:04.000Z"},
		},
		"b2": {
			{ID: "a1b2c3d4e5", Command: "rg TODO internal", Description: "Scan the tree", State: "running", StartedAt: "2026-10-26T10:00:05.000Z"},
		},
	}
}

func TestTheLiveTrayListsRunningAgentsAndTheirShells(t *testing.T) {
	answer := liveTray(trayAgents(), trayShells(), nil)

	if len(answer.Agents) != 1 {
		t.Fatalf("agents = %+v, want only the running b2", answer.Agents)
	}
	b2 := answer.Agents[0]
	if b2.ID != "b2" || b2.Type != "general-purpose" || b2.Description != "Worker B" ||
		b2.StartedAt != "2026-10-26T10:00:07.000Z" {
		t.Errorf("the tray's agent = %+v, want the running b2", b2)
	}

	if len(answer.Shells) != 2 {
		t.Fatalf("shells = %+v, want the main's running one then b2's", answer.Shells)
	}
	if got := answer.Shells[0]; got.Agent != convo.MainAgent || got.ID != "k7w2n9x4d1" ||
		got.Command != "mise run check" || got.Description != "Wait for the gates" ||
		got.StartedAt != "2026-10-26T10:00:03.000Z" {
		t.Errorf("first shell = %+v, want the main's running k7w2n9x4d1", got)
	}
	if got := answer.Shells[1]; got.Agent != "b2" || got.ID != "a1b2c3d4e5" ||
		got.Command != "rg TODO internal" {
		t.Errorf("second shell = %+v, want b2's running a1b2c3d4e5", got)
	}
}

func TestTheLiveTrayAnswersTwoEmptyArraysOnNothingRunning(t *testing.T) {
	raw, err := json.Marshal(liveTray(nil, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got != `{"agents":[],"shells":[]}` {
		t.Fatalf("the empty tray = %s, want {\"agents\":[],\"shells\":[]}", got)
	}
}

// TestTheLiveTrayCarriesTheRadarFacts: a tray agent radar knows carries its output tokens,
// its last call's time and the newest call's what; an agent radar names no row for keeps none
// of the three keys.
func TestTheLiveTrayCarriesTheRadarFacts(t *testing.T) {
	tokens := int64(12)
	at := time.Date(2026, 10, 26, 10, 0, 9, 0, time.UTC)
	facts := map[string]radartab.AgentFacts{
		"b2": {OutputTokens: tokens, LastAt: at, What: "↳ prompt: scan the tree → 3 files"},
	}
	answer := liveTray(trayAgents(), trayShells(), facts)
	if len(answer.Agents) != 1 {
		t.Fatalf("agents = %+v, want only the running b2", answer.Agents)
	}
	b2 := answer.Agents[0]
	if b2.OutputTokens == nil || *b2.OutputTokens != tokens {
		t.Errorf("b2 output tokens = %v, want %d", b2.OutputTokens, tokens)
	}
	if want := "2026-10-26T10:00:09.000Z"; b2.LastActivity != want {
		t.Errorf("b2 last activity = %q, want %q", b2.LastActivity, want)
	}
	if b2.What != "↳ prompt: scan the tree → 3 files" {
		t.Errorf("b2 what = %q, want the newest call's what", b2.What)
	}

	// radar knows the finished a1 alone: the running b2's object names none of the three keys
	onlyA1 := map[string]radartab.AgentFacts{"a1": {OutputTokens: 5}}
	raw, err := json.Marshal(liveTray(trayAgents(), trayShells(), onlyA1))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Agents []map[string]json.RawMessage `json:"agents"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Agents) != 1 {
		t.Fatalf("agents = %s, want only the running b2", raw)
	}
	for _, key := range []string{"output_tokens", "last_activity", "what"} {
		if _, held := parsed.Agents[0][key]; held {
			t.Errorf("b2's object holds %s, want none of the radar keys when radar names no row: %s", key, raw)
		}
	}
}

// writeLiveSession lays out one live session the way Claude Code writes it: a main transcript
// s-live.jsonl with one running background shell, a finished worker a1 and a running worker b2
// beside it in the subagents folder, and a running shell in b2's own transcript. It answers the
// main transcript's path.
func writeLiveSession(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	tasks := filepath.Join(dir, "s-live", "tasks")
	if err := os.MkdirAll(tasks, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"k7w2n9x4d1.output": "waiting\n",
		"a1b2c3d4e5.output": "scanning\n",
	} {
		if err := os.WriteFile(filepath.Join(tasks, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	main := filepath.Join(dir, "s-live.jsonl")
	mainLines := []string{
		convoPromptLine,
		convoAnswerLine,
		shellsUseLine("s-live", false, "a2", "2026-10-26T10:00:03.000Z", "toolu_2", "mise run check", "Wait for the gates"),
		shellsResultLine("s-live", false, "u2", "2026-10-26T10:00:04.000Z", "toolu_2", backgroundedText(dir, "s-live", "k7w2n9x4d1")),
	}
	if err := os.WriteFile(main, []byte(joinLines(mainLines)), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "s-live", "subagents")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"agent-a1.jsonl":     joinLines([]string{subagentPromptLine, subagentAnswerLine}),
		"agent-a1.meta.json": `{"agentType":"general-purpose","description":"Worker A","spawnDepth":1,"finished":true}`,
		"agent-b2.jsonl": joinLines([]string{
			shellsUseLine("s-live", true, "ba1", "2026-10-26T10:00:05.000Z", "toolu_b2", "rg TODO internal", "Scan the tree"),
			shellsResultLine("s-live", true, "bu1", "2026-10-26T10:00:06.000Z", "toolu_b2", backgroundedText(dir, "s-live", "a1b2c3d4e5")),
		}),
		"agent-b2.meta.json": `{"agentType":"general-purpose","description":"Worker B","spawnDepth":1,"finished":false}`,
	} {
		if err := os.WriteFile(filepath.Join(sub, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return main
}

func TestTheSessionLiveRouteAnswersTheRunningTray(t *testing.T) {
	srv := startServer(t, nil)
	reportConvoSession(t, srv, "s-live", writeLiveSession(t))

	code, _, body := srv.get(t, "/api/sessions/s-live/live", nil)
	if code != 200 {
		t.Fatalf("live: %d %s", code, body)
	}
	answer := bodyMap(t, body)
	rawAgents, _ := answer["agents"].([]any)
	if len(rawAgents) != 1 {
		t.Fatalf("agents = %s, want only the running b2", body)
	}
	b2 := rawAgents[0].(map[string]any)
	if b2["id"] != "b2" || b2["type"] != "general-purpose" || b2["description"] != "Worker B" {
		t.Errorf("b2 = %v, want the meta file's running worker", b2)
	}
	if b2["started_at"] == "" {
		t.Errorf("b2 = %v, want its transcript's first timestamp", b2)
	}
	rawShells, _ := answer["shells"].([]any)
	if len(rawShells) != 2 {
		t.Fatalf("shells = %s, want the main's and b2's running ones", body)
	}
	first := rawShells[0].(map[string]any)
	if first["agent"] != "main" || first["id"] != "k7w2n9x4d1" ||
		first["command"] != "mise run check" || first["description"] != "Wait for the gates" {
		t.Errorf("first shell = %v, want the main's running one", first)
	}
	second := rawShells[1].(map[string]any)
	if second["agent"] != "b2" || second["id"] != "a1b2c3d4e5" || second["command"] != "rg TODO internal" {
		t.Errorf("second shell = %v, want b2's running one", second)
	}
}

func TestTheSessionLiveRouteRefusesAnUnknownSession(t *testing.T) {
	srv := startServer(t, nil)
	code, _, body := srv.get(t, "/api/sessions/no-such/live", nil)
	if code != 404 || !strings.Contains(body, "no such session") {
		t.Errorf("an unknown session = %d %s, want 404 no such session", code, body)
	}
}

// TestTheSessionLiveRouteCarriesTheSubagentRadarFacts: the running subagent's answer names
// what radar knows of it — its output tokens above all — read straight through the live
// session's own transcripts feeding the child's radar tab.
func TestTheSessionLiveRouteCarriesTheSubagentRadarFacts(t *testing.T) {
	// the child's radar tab ingests the transcripts under its own CLAUDE_CONFIG_DIR, so the
	// seed lands through the tab's watcher, the way a live session feeds it
	config := t.TempDir()
	if err := os.MkdirAll(filepath.Join(config, "projects", "huddle", "s-radar", "subagents"), 0o755); err != nil {
		t.Fatalf("make the projects dir: %v", err)
	}
	srv := startServer(t, map[string]string{"CLAUDE_CONFIG_DIR": config})

	now := time.Now().UTC()
	promptLine := func(text string, at time.Time) string {
		return fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":[{"type":"text","text":%q}]}}`,
			jsfmt.ISOTime(at), text)
	}
	replyLine := func(at time.Time, tokens int) string {
		return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"model":"test-model",`+
			`"content":[{"type":"text","text":"working"}],"usage":{"input_tokens":10,"output_tokens":%d}}}`,
			jsfmt.ISOTime(at), tokens)
	}
	land := func(path, body string) {
		t.Helper()
		// the rename lands the whole file at once, the way a real transcript write does
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, []byte(body+"\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		if err := os.Rename(tmp, path); err != nil {
			t.Fatalf("land %s: %v", path, err)
		}
	}
	minute := time.Minute
	land(filepath.Join(config, "projects", "huddle", "s-radar.jsonl"),
		promptLine("start the scan", now.Add(-4*minute)))
	land(filepath.Join(config, "projects", "huddle", "s-radar", "subagents", "agent-w1.jsonl"),
		promptLine("scan the tree", now.Add(-3*minute))+"\n"+
			replyLine(now.Add(-2*minute), 7)+"\n"+
			replyLine(now.Add(-minute), 11))
	if err := os.WriteFile(
		filepath.Join(config, "projects", "huddle", "s-radar", "subagents", "agent-w1.meta.json"),
		[]byte(`{"agentType":"general-purpose","description":"Scanner","spawnDepth":1,"finished":false}`),
		0o644); err != nil {
		t.Fatalf("write the meta file: %v", err)
	}
	reportConvoSession(t, srv, "s-radar", filepath.Join(config, "projects", "huddle", "s-radar.jsonl"))

	// the watcher's pass lands the subagent's replies in radar within a few seconds; the
	// output tokens are the proof they arrived
	deadline := time.Now().Add(10 * time.Second)
	var answer string
	var w1 map[string]any
	for {
		code, _, out := srv.get(t, "/api/sessions/s-radar/live", nil)
		if code != 200 {
			t.Fatalf("live: %d %s", code, out)
		}
		answer = out
		agents, _ := bodyMap(t, out)["agents"].([]any)
		for _, held := range agents {
			agent, _ := held.(map[string]any)
			if agent["id"] == "w1" {
				if _, ok := agent["output_tokens"]; ok {
					w1 = agent
				}
			}
		}
		if w1 != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the subagent's tokens never landed; live = %s", answer)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if got, ok := w1["output_tokens"].(float64); !ok || got != 18 {
		t.Errorf("w1 output tokens = %v, want the two replies' 18", w1["output_tokens"])
	}
	if w1["last_activity"] == "" {
		t.Errorf("w1 = %v, want its newest reply's time", w1)
	}
	if w1["what"] == "" {
		t.Errorf("w1 = %v, want its newest call's what", w1)
	}
}

func TestTheSessionLiveRouteAnswersEmptyWithoutATranscript(t *testing.T) {
	srv := startServer(t, nil)
	reportConvoSession(t, srv, "s-bare", "")
	reportConvoSession(t, srv, "s-gone", filepath.Join(t.TempDir(), "missing.jsonl"))

	for _, id := range []string{"s-bare", "s-gone"} {
		code, _, body := srv.get(t, "/api/sessions/"+id+"/live", nil)
		if code != 200 {
			t.Fatalf("%s live: %d %s", id, code, body)
		}
		if got := strings.TrimSpace(body); got != `{"agents":[],"shells":[]}` {
			t.Errorf("%s live = %s, want the empty tray", id, got)
		}
	}
}

func TestTheSessionLiveRouteDemandsACredential(t *testing.T) {
	srv := startServer(t, nil)
	code, _, body, err := srv.request(bg, http.MethodGet, "/api/sessions/s-1/live", "", map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if code != 401 {
		t.Errorf("without a token: %d %s, want 401", code, body)
	}
}
