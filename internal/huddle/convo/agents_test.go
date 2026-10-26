package convo

// The agents list (agents.go): one session's agents read from its transcript and the meta files
// beside it, and the transcript of any one of them found by id.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeLines writes one transcript of the given lines, each newline-terminated.
func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeAgentFixture writes one session in Claude Code's layout: the main transcript s.jsonl
// with s/subagents beside it holding a1 (a finished depth-1 worker), b2 (a running depth-2
// worker), c3 (a subagent whose meta file is missing) and f4 (a /btw fork). It answers the main
// transcript's path.
func writeAgentFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	main := filepath.Join(dir, "s.jsonl")
	writeLines(t, main, []string{
		`{"type":"user","uuid":"u1","timestamp":"2026-10-26T10:00:01.000Z","message":{"role":"user","content":"run the team"}}`,
		`{"type":"assistant","uuid":"a0","timestamp":"2026-10-26T10:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Spawning workers."}]}}`,
	})
	sub := filepath.Join(dir, "s", "subagents")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(sub, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeLines(t, filepath.Join(sub, "agent-a1.jsonl"), []string{
		`{"type":"user","uuid":"au1","timestamp":"2026-10-26T10:00:05.000Z","isSidechain":true,"message":{"role":"user","content":"scan the files"}}`,
		`{"type":"assistant","uuid":"aa1","timestamp":"2026-10-26T10:00:06.000Z","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"Scanned."}]}}`,
	})
	write("agent-a1.meta.json", `{"agentType":"general-purpose","description":"Worker A","toolUseId":"toolu_1","spawnDepth":1,"requestShape":"background","requestNonInteractive":true,"model":"opus","effort":"medium","finished":true}`)
	writeLines(t, filepath.Join(sub, "agent-b2.jsonl"), []string{
		`{"type":"user","uuid":"bu1","timestamp":"2026-10-26T10:00:07.000Z","isSidechain":true,"message":{"role":"user","content":"tab work"}}`,
	})
	write("agent-b2.meta.json", `{"agentType":"general-purpose","description":"Worker B","toolUseId":"toolu_2","spawnDepth":2,"requestShape":"background","requestNonInteractive":true,"model":"haiku","effort":"medium","finished":false}`)
	writeLines(t, filepath.Join(sub, "agent-c3.jsonl"), []string{
		`{"type":"user","uuid":"cu1","timestamp":"2026-10-26T10:00:08.000Z","isSidechain":true,"message":{"role":"user","content":"no meta file"}}`,
	})
	writeLines(t, filepath.Join(sub, "agent-f4.jsonl"), []string{
		`{"type":"user","uuid":"fu1","timestamp":"2026-10-26T10:00:09.000Z","isSidechain":true,"message":{"role":"user","content":"a side question"}}`,
	})
	write("agent-f4.meta.json", `{"agentType":"fork","isFork":true,"spawnDepth":1}`)
	return main
}

func TestAgentsListTheMainThenItsSubagentsThenItsForks(t *testing.T) {
	main := writeAgentFixture(t)
	agents, err := Agents(context.Background(), main)
	if err != nil {
		t.Fatalf("agents: %v", err)
	}
	want := []Agent{
		{ID: MainAgent, Type: "main", Running: true, StartedAt: "2026-10-26T10:00:01.000Z"},
		{ID: "a1", Type: "general-purpose", Description: "Worker A", Depth: 1, Running: false, Model: "opus", StartedAt: "2026-10-26T10:00:05.000Z"},
		{ID: "c3", Type: "subagent", Depth: 1, Running: false, StartedAt: "2026-10-26T10:00:08.000Z"},
		{ID: "b2", Type: "general-purpose", Description: "Worker B", Depth: 2, Running: true, Model: "haiku", StartedAt: "2026-10-26T10:00:07.000Z"},
		{ID: "f4", Type: "fork", Depth: 1, Running: true, SideChat: true, StartedAt: "2026-10-26T10:00:09.000Z"},
	}
	if len(agents) != len(want) {
		t.Fatalf("got %d agents, want %d:\n%+v", len(agents), len(want), agents)
	}
	for i := range want {
		if agents[i] != want[i] {
			t.Errorf("agent %d:\ngot  %+v\nwant %+v", i, agents[i], want[i])
		}
	}
}

func TestAgentsAnswerTheMainAloneWithoutASubagentsFolder(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "s.jsonl")
	writeLines(t, main, []string{
		`{"type":"user","uuid":"u1","timestamp":"2026-10-26T10:00:01.000Z","message":{"role":"user","content":"on my own"}}`,
	})
	agents, err := Agents(context.Background(), main)
	if err != nil {
		t.Fatalf("agents: %v", err)
	}
	want := Agent{ID: MainAgent, Type: "main", Running: true, StartedAt: "2026-10-26T10:00:01.000Z"}
	if len(agents) != 1 || agents[0] != want {
		t.Errorf("agents = %+v, want %+v alone", agents, want)
	}
}

func TestAgentsLeaveAnUnreadableFirstLineWithoutAStartedAt(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "s.jsonl")
	writeLines(t, main, []string{
		`{"type":"user","uuid":"u1","timestamp":"2026-10-26T10:00:01.000Z","message":{"role":"user","content":"run the team"}}`,
	})
	sub := filepath.Join(dir, "s", "subagents")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	// a first line the writer has not ended yet
	if err := os.WriteFile(filepath.Join(sub, "agent-t1.jsonl"),
		[]byte(`{"type":"user","uuid":"tu1","timestamp":"2026-10-26T10:00:02.000Z"`), 0o600); err != nil {
		t.Fatal(err)
	}
	// a first line longer than the window a StartedAt may read
	if err := os.WriteFile(filepath.Join(sub, "agent-t2.jsonl"),
		[]byte(`{"type":"user","uuid":"tu2","timestamp":"2026-10-26T10:00:03.000Z","message":{"role":"user","content":"`+strings.Repeat("x", 2*64*1024)+`"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	agents, err := Agents(context.Background(), main)
	if err != nil {
		t.Fatalf("agents: %v", err)
	}
	if len(agents) != 3 {
		t.Fatalf("got %d agents, want the main and two subagents:\n%+v", len(agents), agents)
	}
	for _, agent := range agents[1:] {
		if agent.StartedAt != "" {
			t.Errorf("agent %s started at %q, want an empty StartedAt", agent.ID, agent.StartedAt)
		}
	}
}

func TestAgentPathAnswersTheTranscriptOfAnAgentId(t *testing.T) {
	main := writeAgentFixture(t)
	a1 := filepath.Join(filepath.Dir(main), "s", "subagents", "agent-a1.jsonl")

	if got, ok := AgentPath(main, MainAgent); !ok || got != main {
		t.Errorf("the main answered %q, %v; want %q, true", got, ok, main)
	}
	if got, ok := AgentPath(main, "a1"); !ok || got != a1 {
		t.Errorf("a1 answered %q, %v; want %q, true", got, ok, a1)
	}
}

func TestAgentPathRefusesWhatIsNotAnAgentId(t *testing.T) {
	main := writeAgentFixture(t)
	for _, id := range []string{"../x", "a/b", ".", "", "zz"} {
		if got, ok := AgentPath(main, id); ok {
			t.Errorf("AgentPath(%q) answered %q, want false", id, got)
		}
	}
}

// A session whose reports never named a transcript has no subagents folder: no id may resolve
// against the hub's own working directory.
func TestAgentPathRefusesAnAgentOfNoTranscript(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "subagents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "subagents", "agent-a1.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if got, ok := AgentPath("", "a1"); ok {
		t.Fatalf("AgentPath(\"\", a1) answered %q, want false", got)
	}
}
