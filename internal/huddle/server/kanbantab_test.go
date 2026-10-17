// The kanban tab as the served server runs it: the boards of the registry's sessions' working
// dirs answer under /api/kanban on huddle's own port, and only a request carrying huddle's
// credential gets in — without one the prefix refuses like every other /api path.
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	kanban "github.com/MuhmdRaouf/huddle/internal/kanban/server"
)

func TestTheKanbanTabNeedsHuddlesCredential(t *testing.T) {
	// two sessions whose working dirs sit in two repos holding boards
	home := t.TempDir()
	repos := []struct{ name, session string }{
		{"alpha", "s-alpha"}, {"beta", "s-beta"},
	}
	for _, r := range repos {
		repo := filepath.Join(home, r.name)
		if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := board.Init(context.Background(), filepath.Join(repo, ".agents", "kanban"), "", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	srv := startServer(t, nil)
	for _, r := range repos {
		report := `{"session_id":"` + r.session + `","cwd":"` + filepath.Join(home, r.name) +
			`","last_event":"UserPromptSubmit","last_event_at":"2026-10-17T09:00:00.000Z"}`
		if code, out := srv.post(t, "/api/registry/report", report, nil); code != 200 {
			t.Fatalf("report the %s session: %d %s", r.name, code, out)
		}
	}

	// no credential: the prefix refuses like every other /api path
	code, _, body, err := srv.request(context.Background(), http.MethodGet, "/api/kanban/boards", "", nil)
	if err != nil || code != 401 {
		t.Fatalf("the uncredentialed boards = %d (%v), want 401", code, err)
	}
	if parsed := bodyMap(t, body); parsed["signin"] != true {
		t.Fatalf("the uncredentialed boards = %s, want huddle's sign-in answer", body)
	}

	// with the root credential both boards answer, sorted by name
	code, _, body = srv.get(t, "/api/kanban/boards", nil)
	if code != 200 {
		t.Fatalf("the credentialed boards = %d %s, want 200", code, body)
	}
	var listed struct {
		Boards []struct {
			Key  string `json:"key"`
			Root string `json:"root"`
			Name string `json:"name"`
		} `json:"boards"`
	}
	if err := json.Unmarshal([]byte(body), &listed); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if len(listed.Boards) != 2 {
		t.Fatalf("boards = %s, want both repos' boards", body)
	}
	if listed.Boards[0].Name != "alpha" || listed.Boards[1].Name != "beta" {
		t.Fatalf("boards = %s, want alpha before beta", body)
	}
	for _, b := range listed.Boards {
		wantKey := kanban.KeyOf(filepath.Join(home, b.Name, ".agents", "kanban"))
		if b.Key != wantKey {
			t.Errorf("board %s keys %s, want %s", b.Name, b.Key, wantKey)
		}
		// each board's own API answers through the prefix on huddle's port
		code, _, boardBody := srv.get(t, "/api/kanban/b/"+b.Key+"/api/board", nil)
		if code != 200 || !strings.Contains(boardBody, "/.agents/kanban") || !strings.Contains(boardBody, `"project":"`+b.Name+`"`) {
			t.Errorf("board %s through the prefix = %d %s, want its snapshot", b.Name, code, boardBody)
		}
	}
}

func TestTheKanbanTabRunsTheFlowAndWritesTheAuditRow(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "alpha")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(repo, ".agents", "kanban")
	if _, err := board.Init(context.Background(), root, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	card := "---\nid: FLW-1\ntitle: \"one card\"\ntype: docs\ncluster: kanban\n" +
		"summary: \"one\"\n---\n\n# FLW-1\n\nBody text.\n"
	if err := os.WriteFile(filepath.Join(root, "ready", "FLW-1.md"), []byte(card), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := startServer(t, nil)
	report := `{"session_id":"s-flow","cwd":"` + repo +
		`","last_event":"UserPromptSubmit","last_event_at":"2026-10-17T09:00:00.000Z"}`
	if code, out := srv.post(t, "/api/registry/report", report, nil); code != 200 {
		t.Fatalf("report the session: %d %s", code, out)
	}
	key := kanban.KeyOf(root)

	// the flow command runs under huddle's credential, like every /api write
	code, out := srv.post(t, "/api/kanban/b/"+key+"/flow", `{"command":"pull","actor":"w1","id":"FLW-1"}`, nil)
	if code != 200 || !strings.Contains(out, "in-progress") {
		t.Fatalf("the flow pull = %d %s, want 200 with the card's new path", code, out)
	}
	code, _, body := srv.get(t, "/api/kanban/b/"+key+"/api/board", nil)
	if code != 200 || !strings.Contains(body, `"column":"in-progress"`) {
		t.Fatalf("the board after the pull = %d %s, want the card in in-progress", code, body)
	}

	// one audit row names the write: who ran it, the command, the card and that the board took it
	_, _, body = srv.get(t, "/api/audit", nil)
	var row map[string]any
	for _, entry := range bodyMap(t, body)["entries"].([]any) {
		if candidate, _ := entry.(map[string]any); candidate["action"] == "kanban.pull" {
			row = candidate
		}
	}
	if row == nil {
		t.Fatalf("no kanban.pull row in the audit %s", body)
	}
	if row["actor"] != "owner" || row["target"] != key+"/FLW-1" {
		t.Errorf("the kanban.pull row = %v, want the owner writing FLW-1 on board %s", row, key)
	}
	if detail, _ := row["detail"].(map[string]any); detail["ok"] != true {
		t.Errorf("the kanban.pull detail = %v, want ok true", row["detail"])
	}
}
