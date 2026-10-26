package server

// The agents routes (agents.go): one session's agents listed from its transcript's subagents
// folder, and one agent's conversation paged from a byte cursor like the session's own. Every
// test runs its own server child against its own transcript files.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// the subagent transcripts the fixture writes beside its session, and the meta files that
// describe them: a1 a finished depth-1 worker, b2 a running depth-2 one, c3 with no meta file,
// f4 a /btw fork
var (
	agentA1Body = joinLines([]string{
		`{"type":"user","uuid":"au1","timestamp":"2026-10-26T10:00:05.000Z","isSidechain":true,"message":{"role":"user","content":"scan the files"}}`,
		`{"type":"assistant","uuid":"aa1","timestamp":"2026-10-26T10:00:06.000Z","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"Scanned."}]}}`,
	})
	agentA1Meta    = `{"agentType":"general-purpose","description":"Worker A","toolUseId":"toolu_1","spawnDepth":1,"requestShape":"background","requestNonInteractive":true,"model":"opus","effort":"medium","finished":true}`
	agentB2Body    = joinLines([]string{`{"type":"user","uuid":"bu1","timestamp":"2026-10-26T10:00:07.000Z","isSidechain":true,"message":{"role":"user","content":"tab work"}}`})
	agentB2Meta    = `{"agentType":"general-purpose","description":"Worker B","toolUseId":"toolu_2","spawnDepth":2,"requestShape":"background","requestNonInteractive":true,"model":"haiku","effort":"medium","finished":false}`
	agentC3Body    = joinLines([]string{`{"type":"user","uuid":"cu1","timestamp":"2026-10-26T10:00:08.000Z","isSidechain":true,"message":{"role":"user","content":"no meta file"}}`})
	agentF4Body    = joinLines([]string{`{"type":"user","uuid":"fu1","timestamp":"2026-10-26T10:00:09.000Z","isSidechain":true,"message":{"role":"user","content":"a side question"}}`})
	agentF4Meta    = `{"agentType":"fork","isFork":true,"spawnDepth":1}`
	agentFixtureID = "s"
)

// writeAgentSession writes one session transcript with its subagents beside it, Claude Code's
// layout, and answers the main transcript's path.
func writeAgentSession(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	main := filepath.Join(dir, agentFixtureID+".jsonl")
	if err := os.WriteFile(main, []byte(joinLines([]string{convoPromptLine, convoAnswerLine})), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, agentFixtureID, "subagents")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"agent-a1.jsonl":     agentA1Body,
		"agent-a1.meta.json": agentA1Meta,
		"agent-b2.jsonl":     agentB2Body,
		"agent-b2.meta.json": agentB2Meta,
		"agent-c3.jsonl":     agentC3Body,
		"agent-f4.jsonl":     agentF4Body,
		"agent-f4.meta.json": agentF4Meta,
	} {
		if err := os.WriteFile(filepath.Join(sub, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return main
}

// agentsOf reads one agents answer's list.
func agentsOf(t *testing.T, body string) []map[string]any {
	t.Helper()
	raw, _ := bodyMap(t, body)["agents"].([]any)
	agents := make([]map[string]any, 0, len(raw))
	for _, one := range raw {
		agents = append(agents, one.(map[string]any))
	}
	return agents
}

func TestTheAgentsRouteListsASessionAndItsAgents(t *testing.T) {
	srv := startServer(t, nil)
	reportConvoSession(t, srv, "s-1", writeAgentSession(t))

	code, _, body := srv.get(t, "/api/sessions/s-1/agents", nil)
	if code != 200 {
		t.Fatalf("agents: %d %s", code, body)
	}
	agents := agentsOf(t, body)
	if len(agents) != 5 {
		t.Fatalf("got %d agents, want 5: %s", len(agents), body)
	}
	ids := make([]string, 0, len(agents))
	for _, agent := range agents {
		ids = append(ids, agent["id"].(string))
	}
	want := []string{"main", "a1", "c3", "b2", "f4"}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("agents = %v, want %v", ids, want)
		}
	}
	main, a1, b2, f4 := agents[0], agents[1], agents[3], agents[4]
	if main["type"] != "main" || main["depth"] != float64(0) || main["running"] != true {
		t.Errorf("the main = %v, want type main, depth 0, running", main)
	}
	if a1["type"] != "general-purpose" || a1["description"] != "Worker A" || a1["depth"] != float64(1) ||
		a1["running"] != false || a1["model"] != "opus" {
		t.Errorf("a1 = %v, want the meta file's worker", a1)
	}
	if b2["depth"] != float64(2) || b2["running"] != true {
		t.Errorf("b2 = %v, want a running depth-2 worker", b2)
	}
	if f4["side_chat"] != true {
		t.Errorf("f4 = %v, want a side chat", f4)
	}

	// a session whose reports never named a transcript answers none
	reportConvoSession(t, srv, "s-bare", "")
	code, _, body = srv.get(t, "/api/sessions/s-bare/agents", nil)
	if code != 200 {
		t.Fatalf("agents without a transcript: %d %s", code, body)
	}
	if agents := agentsOf(t, body); len(agents) != 0 {
		t.Errorf("agents without a transcript = %s, want none", body)
	}

	// so does a session whose transcript the disk no longer has
	reportConvoSession(t, srv, "s-gone", filepath.Join(t.TempDir(), "missing.jsonl"))
	code, _, body = srv.get(t, "/api/sessions/s-gone/agents", nil)
	if code != 200 {
		t.Fatalf("agents over a missing file: %d %s", code, body)
	}
	if agents := agentsOf(t, body); len(agents) != 0 {
		t.Errorf("agents over a missing file = %s, want none", body)
	}

	// a session the registry does not know
	code, _, body = srv.get(t, "/api/sessions/s-none/agents", nil)
	if code != 404 || !strings.Contains(body, "no such session") {
		t.Errorf("unknown session: %d %s, want 404 naming it", code, body)
	}
}

func TestTheAgentConversationRouteAnswersOneAgent(t *testing.T) {
	srv := startServer(t, nil)
	reportConvoSession(t, srv, "s-1", writeAgentSession(t))

	code, _, body := srv.get(t, "/api/sessions/s-1/agents/a1/conversation", nil)
	if code != 200 {
		t.Fatalf("a1's conversation: %d %s", code, body)
	}
	items := itemsOf(t, body)
	if len(items) != 2 || items[0]["text"] != "scan the files" || items[1]["kind"] != "assistant" {
		t.Errorf("a1's items = %v, want its prompt then its answer", items)
	}
	if next := nextOf(t, body); next != int64(len(agentA1Body)) {
		t.Errorf("a1's next = %d, want the end of its file (%d)", next, len(agentA1Body))
	}

	// the main agent reads what the session's own conversation answers
	code, _, agentBody := srv.get(t, "/api/sessions/s-1/agents/main/conversation", nil)
	if code != 200 {
		t.Fatalf("the main agent's conversation: %d %s", code, agentBody)
	}
	code, _, sessionBody := srv.get(t, "/api/sessions/s-1/conversation", nil)
	if code != 200 {
		t.Fatalf("the session's conversation: %d %s", code, sessionBody)
	}
	if agentBody != sessionBody {
		t.Errorf("the main agent's conversation = %s, want what the session's own answers: %s",
			agentBody, sessionBody)
	}

	// an agent transcript the disk lost answers the empty page at the cursor
	reportConvoSession(t, srv, "s-gone", filepath.Join(t.TempDir(), "missing.jsonl"))
	code, _, body = srv.get(t, "/api/sessions/s-gone/agents/main/conversation", nil)
	if code != 200 {
		t.Fatalf("a missing agent transcript: %d %s", code, body)
	}
	if items := itemsOf(t, body); len(items) != 0 || nextOf(t, body) != 0 {
		t.Errorf("a missing agent transcript's page = %s, want an empty page at 0", body)
	}

	// an id no agent answers, and one that is not an agent id at all
	for _, agent := range []string{"zz", "..%2Fx"} {
		code, _, body := srv.get(t, "/api/sessions/s-1/agents/"+agent+"/conversation", nil)
		if code != 404 {
			t.Errorf("agent %q: %d %s, want 404", agent, code, body)
		}
	}

	// a bad cursor is refused like the session's own
	code, _, body = srv.get(t, "/api/sessions/s-1/agents/a1/conversation?after=yesterday", nil)
	if code != 400 || !strings.Contains(body, "after") {
		t.Errorf("a bad cursor: %d %s, want 400 naming after", code, body)
	}
}

func TestTheAgentsRoutesDemandACredential(t *testing.T) {
	srv := startServer(t, nil)
	for _, path := range []string{
		"/api/sessions/s-1/agents",
		"/api/sessions/s-1/agents/a1/conversation",
	} {
		code, _, body, err := srv.request(context.Background(), http.MethodGet, path, "", map[string]string{})
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		if code != 401 {
			t.Errorf("GET %s without a token: %d %s, want 401", path, code, body)
		}
	}
}
