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
		{ID: MainAgent, Type: "main", Running: true, StartedAt: "2026-10-26T10:00:01.000Z", LastText: "Spawning workers."},
		{ID: "a1", Type: "general-purpose", Description: "Worker A", Depth: 1, Running: false, Model: "opus", StartedAt: "2026-10-26T10:00:05.000Z", LastText: "Scanned.", ToolUseID: "toolu_1", Parent: MainAgent},
		{ID: "c3", Type: "subagent", Depth: 1, Running: false, StartedAt: "2026-10-26T10:00:08.000Z", Parent: MainAgent},
		{ID: "b2", Type: "general-purpose", Description: "Worker B", Depth: 2, Running: true, Model: "haiku", StartedAt: "2026-10-26T10:00:07.000Z", ToolUseID: "toolu_2", Parent: MainAgent},
		{ID: "f4", Type: "fork", Depth: 1, Running: true, SideChat: true, StartedAt: "2026-10-26T10:00:09.000Z", Parent: MainAgent},
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

// writeMetaFixture writes one session with one subagent whose meta file holds body, and answers
// the main transcript's path.
func writeMetaFixture(t *testing.T, meta string) string {
	t.Helper()
	dir := t.TempDir()
	main := filepath.Join(dir, "s.jsonl")
	writeLines(t, main, []string{
		`{"type":"user","uuid":"u1","timestamp":"2026-10-26T10:00:01.000Z","message":{"role":"user","content":"one helper"}}`,
	})
	sub := filepath.Join(dir, "s", "subagents")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	writeLines(t, filepath.Join(sub, "agent-a1.jsonl"), []string{
		`{"type":"user","uuid":"au1","timestamp":"2026-10-26T10:00:02.000Z","isSidechain":true,"message":{"role":"user","content":"one subagent"}}`,
	})
	if err := os.WriteFile(filepath.Join(sub, "agent-a1.meta.json"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	return main
}

// agentRowOf answers the one Agent tool call a page carries.
func agentRowOf(t *testing.T, page Page, where string) Item {
	t.Helper()
	for _, item := range page.Items {
		if item.Kind == KindToolUse && item.Tool == "Agent" {
			return item
		}
	}
	t.Fatalf("%s holds no Agent tool call", where)
	return Item{}
}

// TestAgentsLinkToTheAgentRowThatStartedThem reads the version fixtures: a1 names the Agent call
// the main's transcript holds, and the nested b2 names the Agent call inside a1's own transcript
// and carries a1 as its parent.
func TestAgentsLinkToTheAgentRowThatStartedThem(t *testing.T) {
	for _, version := range []string{"2.1.303", "2.1.304", "2.1.305", "2.1.306", "2.1.307", "2.1.308", "2.1.309"} {
		t.Run(version, func(t *testing.T) {
			main := filepath.Join("testdata", "cc", version, "agents-nested", "session.jsonl")
			agents, err := Agents(context.Background(), main)
			if err != nil {
				t.Fatalf("agents: %v", err)
			}
			var a1, b2 Agent
			for _, agent := range agents {
				switch agent.ID {
				case "a1":
					a1 = agent
				case "b2":
					b2 = agent
				}
			}
			if a1.ID == "" || b2.ID == "" {
				t.Fatalf("agents = %+v, want a1 and b2", agents)
			}
			mainPage, err := Read(context.Background(), main, 0)
			if err != nil {
				t.Fatalf("read the main transcript: %v", err)
			}
			row := agentRowOf(t, mainPage, "the main transcript")
			if a1.ToolUseID != row.ToolUseID {
				t.Errorf("a1's tool_use_id = %q, want the Agent call's %q", a1.ToolUseID, row.ToolUseID)
			}
			if b2.Parent != "a1" {
				t.Errorf("b2's parent = %q, want a1", b2.Parent)
			}
			a1Path, ok := AgentPath(main, "a1")
			if !ok {
				t.Fatalf("AgentPath answered no transcript for a1")
			}
			a1Page, err := ReadAgent(context.Background(), a1Path, 0)
			if err != nil {
				t.Fatalf("read a1's transcript: %v", err)
			}
			nested := agentRowOf(t, a1Page, "a1's transcript")
			if b2.ToolUseID != nested.ToolUseID {
				t.Errorf("b2's tool_use_id = %q, want a1's Agent call's %q", b2.ToolUseID, nested.ToolUseID)
			}
		})
	}
}

// A subagent the owner stopped lists as stopped and not running, even though its meta file names
// no finished.
func TestAStoppedSubagentIsNotRunning(t *testing.T) {
	main := writeMetaFixture(t, `{"agentType":"general-purpose","description":"Stopped worker","toolUseId":"toolu_5","spawnDepth":1,"stoppedByUser":true}`)
	agents, err := Agents(context.Background(), main)
	if err != nil {
		t.Fatalf("agents: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("got %d agents, want the main and one subagent:\n%+v", len(agents), agents)
	}
	sub := agents[1]
	if !sub.StoppedByUser || sub.Running {
		t.Errorf("the stopped subagent = %+v, want stopped_by_user and not running", sub)
	}
}

// A meta file that names no Agent call still lists its subagent: an empty tool_use_id and the
// main conversation as its parent, so the view groups it under main.
func TestAMetaWithoutALinkStillLists(t *testing.T) {
	main := writeMetaFixture(t, `{"agentType":"general-purpose","description":"A helper","spawnDepth":1,"finished":true}`)
	agents, err := Agents(context.Background(), main)
	if err != nil {
		t.Fatalf("agents: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("got %d agents, want the main and one subagent:\n%+v", len(agents), agents)
	}
	sub := agents[1]
	if sub.ToolUseID != "" || sub.Parent != MainAgent {
		t.Errorf("the unlinked subagent = %+v, want an empty tool_use_id and the main as parent", sub)
	}
}

// lastTextOf answers the agents' last texts keyed by id.
func lastTextOf(t *testing.T, agents []Agent) map[string]string {
	t.Helper()
	last := make(map[string]string, len(agents))
	for _, agent := range agents {
		last[agent.ID] = agent.LastText
	}
	return last
}

// TestAnAgentCarriesItsLastAssistantText: an agent's last text is the last assistant text its
// transcript holds — the main's own, and a subagent's even though a tool call and its answer
// come after the words. A text longer than the fold keeps is cut to 500 bytes on a rune
// boundary, and an agent that has said nothing carries none.
func TestAnAgentCarriesItsLastAssistantText(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "s.jsonl")
	writeLines(t, main, []string{
		`{"type":"user","uuid":"u1","timestamp":"2026-10-26T10:00:01.000Z","message":{"role":"user","content":"run the team"}}`,
		`{"type":"assistant","uuid":"a0","timestamp":"2026-10-26T10:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Spawning the scan."}]}}`,
	})
	sub := filepath.Join(dir, "s", "subagents")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	// the words, then a tool call and its answer after them: the words still win
	writeLines(t, filepath.Join(sub, "agent-t1.jsonl"), []string{
		`{"type":"user","uuid":"tu1","timestamp":"2026-10-26T10:00:03.000Z","isSidechain":true,"message":{"role":"user","content":"scan the files"}}`,
		`{"type":"assistant","uuid":"ta1","timestamp":"2026-10-26T10:00:04.000Z","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"Report written."}]}}`,
		`{"type":"assistant","uuid":"ta2","timestamp":"2026-10-26T10:00:05.000Z","isSidechain":true,"message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_9","name":"Agent","input":{"description":"tab work"}}}]}`,
		`{"type":"user","uuid":"tu2","timestamp":"2026-10-26T10:00:06.000Z","isSidechain":true,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_9","content":"started tab work"}]}}`,
	})
	// a text of 504 bytes: the fold keeps its first 500
	writeLines(t, filepath.Join(sub, "agent-t2.jsonl"), []string{
		`{"type":"assistant","uuid":"tu3","timestamp":"2026-10-26T10:00:07.000Z","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"` + strings.Repeat("x", 504) + `"}]}}`,
	})
	// a three-byte rune the 500th byte would split: the cut keeps whole runes alone
	writeLines(t, filepath.Join(sub, "agent-t3.jsonl"), []string{
		`{"type":"assistant","uuid":"tu4","timestamp":"2026-10-26T10:00:08.000Z","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"` + strings.Repeat("x", 499) + "…" + `"}]}}`,
	})
	// nothing said yet
	writeLines(t, filepath.Join(sub, "agent-t4.jsonl"), []string{
		`{"type":"user","uuid":"tu5","timestamp":"2026-10-26T10:00:09.000Z","isSidechain":true,"message":{"role":"user","content":"only asked so far"}}`,
	})

	agents, err := Agents(context.Background(), main)
	if err != nil {
		t.Fatalf("agents: %v", err)
	}
	last := lastTextOf(t, agents)
	if last[MainAgent] != "Spawning the scan." {
		t.Errorf("the main's last text = %q, want Spawning the scan.", last[MainAgent])
	}
	if last["t1"] != "Report written." {
		t.Errorf("t1's last text = %q, want Report written.", last["t1"])
	}
	if last["t2"] != strings.Repeat("x", 500) {
		t.Errorf("t2's last text = %d bytes, want the first 500", len(last["t2"]))
	}
	if last["t3"] != strings.Repeat("x", 499) {
		t.Errorf("t3's last text = %q, want the 499 whole bytes the rune boundary keeps", last["t3"])
	}
	if last["t4"] != "" {
		t.Errorf("t4's last text = %q, want none", last["t4"])
	}
}

// TestATornTailLineIsSkipped: a last line the writer has not ended yet — no newline behind it —
// is no answer, whether it carries words or not; the complete lines before it stand.
func TestATornTailLineIsSkipped(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "s.jsonl")
	writeLines(t, main, []string{
		`{"type":"user","uuid":"u1","timestamp":"2026-10-26T10:00:01.000Z","message":{"role":"user","content":"run the team"}}`,
	})
	sub := filepath.Join(dir, "s", "subagents")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	// words first, then a torn line that would answer if it were whole
	whole := `{"type":"assistant","uuid":"ta1","timestamp":"2026-10-26T10:00:02.000Z","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"Complete answer."}]}}` + "\n"
	torn := `{"type":"assistant","uuid":"ta2","timestamp":"2026-10-26T10:00:03.000Z","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"Half an answ`
	if err := os.WriteFile(filepath.Join(sub, "agent-t1.jsonl"), []byte(whole+torn), 0o600); err != nil {
		t.Fatal(err)
	}
	// a torn line alone
	tornAlone := `{"type":"assistant","uuid":"tu1","timestamp":"2026-10-26T10:00:04.000Z","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"Torn alone`
	if err := os.WriteFile(filepath.Join(sub, "agent-t2.jsonl"), []byte(tornAlone), 0o600); err != nil {
		t.Fatal(err)
	}

	agents, err := Agents(context.Background(), main)
	if err != nil {
		t.Fatalf("agents: %v", err)
	}
	last := lastTextOf(t, agents)
	if last["t1"] != "Complete answer." {
		t.Errorf("t1's last text = %q, want Complete answer.", last["t1"])
	}
	if last["t2"] != "" {
		t.Errorf("t2's last text = %q, want none", last["t2"])
	}
}

// TestAHugeTranscriptIsReadByItsTailOnly: the last 64 KiB are all a last text reads, the same
// window a StartedAt reads from the front. Words only a whole-file read would find are no
// answer; the tail window's own words are.
func TestAHugeTranscriptIsReadByItsTailOnly(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "s.jsonl")
	writeLines(t, main, []string{
		`{"type":"user","uuid":"u1","timestamp":"2026-10-26T10:00:01.000Z","message":{"role":"user","content":"run the team"}}`,
	})
	sub := filepath.Join(dir, "s", "subagents")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	huge := `{"type":"assistant","uuid":"ta1","timestamp":"2026-10-26T10:00:02.000Z","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"` + strings.Repeat("x", 2*64*1024) + `"}]}}`
	// one huge line alone: the window's first line is its torn tail, and no complete line is left
	if err := os.WriteFile(filepath.Join(sub, "agent-t1.jsonl"), []byte(huge+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// the same huge line, then lines the tail window holds whole
	writeLines(t, filepath.Join(sub, "agent-t2.jsonl"), []string{
		huge,
		`{"type":"user","uuid":"tu1","timestamp":"2026-10-26T10:00:03.000Z","isSidechain":true,"message":{"role":"user","content":"and then?"}}`,
		`{"type":"assistant","uuid":"ta2","timestamp":"2026-10-26T10:00:04.000Z","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"Tail words."}]}}`,
	})

	agents, err := Agents(context.Background(), main)
	if err != nil {
		t.Fatalf("agents: %v", err)
	}
	last := lastTextOf(t, agents)
	if last["t1"] != "" {
		t.Errorf("t1's last text = %q, want none: its words the window cannot hold whole", last["t1"])
	}
	if last["t2"] != "Tail words." {
		t.Errorf("t2's last text = %q, want Tail words.", last["t2"])
	}
}
