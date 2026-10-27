package server

// The session tasks route (tasks_route.go): one GET naming a session's own todo list, read from
// the transcript its reports named, beside the channel tasks its member owns. Every test runs
// its own server child against its own transcript and its own channels.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/kanbantab"
)

// reportTasksSession registers one live session in a channel as a member; a cwd names its
// working directory and a path its transcript.
func reportTasksSession(t *testing.T, srv *childServer, id, channel, member, cwd, path string) {
	t.Helper()
	body := fmt.Sprintf(`{"session_id":%q,"machine":"box","last_event":"UserPromptSubmit","last_event_at":%q`, id, isoAgo(time.Second))
	if channel != "" {
		body += fmt.Sprintf(`,"channel":%q,"member":%q`, channel, member)
	}
	if cwd != "" {
		body += fmt.Sprintf(`,"cwd":%q`, cwd)
	}
	if path != "" {
		body += fmt.Sprintf(`,"transcript_path":%q`, path)
	}
	body += "}"
	code, out := srv.post(t, "/api/registry/report", body, nil)
	if code != 200 {
		t.Fatalf("report %s: %d %s", id, code, out)
	}
}

// the one TodoWrite the transcript fixtures hold: two entries, the first in progress
const tasksTodoLine = `{"type":"assistant","uuid":"a1","timestamp":"2026-10-26T10:00:01.000Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu-1","name":"TodoWrite","input":{"todos":[{"content":"write the tests","status":"in_progress","activeForm":"writing the tests"},{"content":"report","status":"pending"}]}}]}}`

// writeTasksTranscript writes one transcript holding the fixture lines.
func writeTasksTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	whole := ""
	for _, line := range lines {
		whole += line + "\n"
	}
	if err := os.WriteFile(path, []byte(whole), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeTasksBoard writes one board card file at repo/.agents/kanban/<column>/<id>.md, held
// by holder, the folder shape Board.List reads.
func writeTasksBoard(t *testing.T, repo, column, id, holder, title string) {
	t.Helper()
	card := fmt.Sprintf("---\nid: %s\ntitle: %q\ntype: fix\nclaimed_by: %s\n---\n\n# %s\n\nBody text.\n", id, title, holder, id)
	path := filepath.Join(repo, ".agents", "kanban", column, id+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(card), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTheSessionTasksRouteAnswersTodosAndOwnedTasks(t *testing.T) {
	srv := startServer(t, nil)
	if code, out := srv.post(t, "/api/channels", `{"name":"build"}`, nil); code != 200 {
		t.Fatalf("create the channel: %d %s", code, out)
	}
	srv.op(t, "build", "join", "api", "{}")
	srv.op(t, "build", "join", "web", "{}")
	srv.op(t, "build", "task_create", "api", `{"id":"t-1","title":"Ship it","owner":"api"}`)
	srv.op(t, "build", "task_create", "web", `{"id":"t-2","title":"Review it","owner":"web"}`)
	path := writeTasksTranscript(t, tasksTodoLine)
	reportTasksSession(t, srv, "s-1", "build", "api", "", path)

	code, _, body := srv.get(t, "/api/sessions/s-1/tasks", nil)
	if code != 200 {
		t.Fatalf("tasks: %d %s", code, body)
	}
	page := bodyMap(t, body)
	todos, _ := page["todos"].([]any)
	if len(todos) != 2 {
		t.Fatalf("todos = %v, want the two entries the TodoWrite wrote", todos)
	}
	first, _ := todos[0].(map[string]any)
	if first["id"] != "0" || first["content"] != "write the tests" || first["status"] != "in_progress" || first["active_form"] != "writing the tests" {
		t.Errorf("the first todo = %v, want the in-progress entry with id 0", first)
	}
	second, _ := todos[1].(map[string]any)
	if second["id"] != "1" || second["content"] != "report" || second["status"] != "pending" {
		t.Errorf("the second todo = %v, want the pending entry with id 1", second)
	}
	tasks, _ := page["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("tasks = %v, want the one task s-1's member owns", tasks)
	}
	task, _ := tasks[0].(map[string]any)
	if task["id"] != "t-1" || task["title"] != "Ship it" || task["status"] != "todo" || task["channel"] != "build" {
		t.Errorf("the task = %v, want t-1 Ship it on build", task)
	}
}

func TestTheSessionTasksRouteAnswersNoTasksForASessionInNoChannel(t *testing.T) {
	srv := startServer(t, nil)
	path := writeTasksTranscript(t, tasksTodoLine)
	reportTasksSession(t, srv, "s-lone", "", "", "", path)

	code, _, body := srv.get(t, "/api/sessions/s-lone/tasks", nil)
	if code != 200 {
		t.Fatalf("tasks: %d %s", code, body)
	}
	page := bodyMap(t, body)
	todos, _ := page["todos"].([]any)
	if len(todos) != 2 {
		t.Errorf("todos = %v, want the transcript's two entries still answered", todos)
	}
	tasks, _ := page["tasks"].([]any)
	if tasks == nil || len(tasks) != 0 {
		t.Errorf("tasks = %v, want an empty list", tasks)
	}
}

func TestTheSessionTasksRouteAnswersNoTasksForAChannelItDoesNotHold(t *testing.T) {
	srv := startServer(t, nil)
	path := writeTasksTranscript(t, tasksTodoLine)
	reportTasksSession(t, srv, "s-gone", "vanished", "api", "", path)

	code, _, body := srv.get(t, "/api/sessions/s-gone/tasks", nil)
	if code != 200 {
		t.Fatalf("tasks: %d %s", code, body)
	}
	tasks, _ := bodyMap(t, body)["tasks"].([]any)
	if tasks == nil || len(tasks) != 0 {
		t.Errorf("tasks = %v, want an empty list for a channel the hub does not hold", tasks)
	}
}

func TestTheSessionTasksRouteRefusesAnUnknownSession(t *testing.T) {
	srv := startServer(t, nil)
	code, _, body := srv.get(t, "/api/sessions/no-such/tasks", nil)
	if code != 404 || !strings.Contains(body, "no such session") {
		t.Errorf("an unknown session = %d %s, want 404 no such session", code, body)
	}
}

func TestTheSessionTasksRouteNeedsACredential(t *testing.T) {
	srv := startServer(t, nil)
	code, _, body := srv.get(t, "/api/sessions/s-1/tasks", map[string]string{"x-huddle-token": ""})
	if code != 401 {
		t.Errorf("a request without a token = %d %s, want 401", code, body)
	}
}

// writeTasksFeatureCard writes one board card file at repo/.agents/kanban/<column>/<id>.md
// carrying its cluster, holder and author, the shape the feature answer reads. An empty
// holder or author leaves that front matter line out.
func writeTasksFeatureCard(t *testing.T, repo, column, id, cluster, holder, author, title string) {
	t.Helper()
	fields := fmt.Sprintf("id: %s\ntitle: %q\ntype: fix\ncluster: %s\n", id, title, cluster)
	if holder != "" {
		fields += "claimed_by: " + holder + "\n"
	}
	if author != "" {
		fields += "authored_by: " + author + "\n"
	}
	card := "---\n" + fields + "---\n\n# " + id + "\n\nBody text.\n"
	path := filepath.Join(repo, ".agents", "kanban", column, id+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(card), 0o644); err != nil {
		t.Fatal(err)
	}
}

// tasksBoardKey answers the tab's key for the board at repo, the value the answer's board
// field carries.
func tasksBoardKey(t *testing.T, repo string) string {
	t.Helper()
	tab := kanbantab.Mount(context.Background(), kanbantab.Options{
		Roots: func(context.Context) []string { return []string{repo} },
	})
	return tab.BoardKey(context.Background(), filepath.Join(repo, ".agents", "kanban"))
}

func TestTheSessionTasksRouteNamesTheClustersAndBoardOfTheCardsItHolds(t *testing.T) {
	srv := startServer(t, nil)
	if code, out := srv.post(t, "/api/channels", `{"name":"build"}`, nil); code != 200 {
		t.Fatalf("create the channel: %d %s", code, out)
	}
	srv.op(t, "build", "join", "lead.kf", "{}")
	repo := filepath.Join(t.TempDir(), "repo")
	writeTasksFeatureCard(t, repo, "in-progress", "SWR-1", "swr", "lead.kf", "web.kf", "Ship the retry")
	writeTasksFeatureCard(t, repo, "ready", "WEB-2", "web", "lead.kf", "", "Tune the page")
	path := writeTasksTranscript(t, tasksTodoLine)
	reportTasksSession(t, srv, "s-cluster", "build", "lead.kf", repo, path)

	code, _, body := srv.get(t, "/api/sessions/s-cluster/tasks", nil)
	if code != 200 {
		t.Fatalf("tasks: %d %s", code, body)
	}
	page := bodyMap(t, body)
	cards, _ := page["cards"].([]any)
	if len(cards) != 2 {
		t.Fatalf("cards = %v, want the two cards the member holds", cards)
	}
	// the answer keeps the list's order: ready before in-progress
	first, _ := cards[0].(map[string]any)
	if first["id"] != "WEB-2" || first["cluster"] != "web" || first["source"] != "held" {
		t.Errorf("the first card = %v, want WEB-2 in cluster web, held", first)
	}
	second, _ := cards[1].(map[string]any)
	if second["id"] != "SWR-1" || second["cluster"] != "swr" || second["source"] != "held" {
		t.Errorf("the second card = %v, want SWR-1 in cluster swr, held", second)
	}
	if want := tasksBoardKey(t, repo); page["board"] != want {
		t.Errorf("board = %v, want the tab's key for the board root %s", page["board"], want)
	}
}

func TestTheSessionTasksRouteFallsBackToTheCardsItsMemberAuthored(t *testing.T) {
	srv := startServer(t, nil)
	if code, out := srv.post(t, "/api/channels", `{"name":"build"}`, nil); code != 200 {
		t.Fatalf("create the channel: %d %s", code, out)
	}
	srv.op(t, "build", "join", "lead.kf", "{}")
	repo := filepath.Join(t.TempDir(), "repo")
	writeTasksFeatureCard(t, repo, "in-progress", "SWR-9", "swr", "", "lead.kf", "Write the spec")
	writeTasksFeatureCard(t, repo, "ready", "OTHER-4", "web", "web.kf", "web.kf", "Someone else's card")
	path := writeTasksTranscript(t, tasksTodoLine)
	reportTasksSession(t, srv, "s-authored", "build", "lead.kf", repo, path)

	code, _, body := srv.get(t, "/api/sessions/s-authored/tasks", nil)
	if code != 200 {
		t.Fatalf("tasks: %d %s", code, body)
	}
	cards, _ := bodyMap(t, body)["cards"].([]any)
	if len(cards) != 1 {
		t.Fatalf("cards = %v, want exactly the card the member authored", cards)
	}
	card, _ := cards[0].(map[string]any)
	if card["id"] != "SWR-9" || card["cluster"] != "swr" || card["source"] != "authored" {
		t.Errorf("the card = %v, want SWR-9 in cluster swr, authored", card)
	}
}

func TestTheSessionTasksRouteAnswersNoCardsForOnlyAuthoredDoneOnes(t *testing.T) {
	srv := startServer(t, nil)
	if code, out := srv.post(t, "/api/channels", `{"name":"build"}`, nil); code != 200 {
		t.Fatalf("create the channel: %d %s", code, out)
	}
	srv.op(t, "build", "join", "lead.kf", "{}")
	repo := filepath.Join(t.TempDir(), "repo")
	writeTasksFeatureCard(t, repo, "done", "SWR-8", "swr", "", "lead.kf", "Already merged")
	path := writeTasksTranscript(t, tasksTodoLine)
	reportTasksSession(t, srv, "s-doned", "build", "lead.kf", repo, path)

	code, _, body := srv.get(t, "/api/sessions/s-doned/tasks", nil)
	if code != 200 {
		t.Fatalf("tasks: %d %s", code, body)
	}
	cards, _ := bodyMap(t, body)["cards"].([]any)
	if cards == nil || len(cards) != 0 {
		t.Errorf("cards = %v, want an empty list for a done card the member authored", cards)
	}
}

func TestTheSessionTasksRouteAnswersTheBoardKeyWhenItHoldsAndAuthoredNothing(t *testing.T) {
	srv := startServer(t, nil)
	if code, out := srv.post(t, "/api/channels", `{"name":"build"}`, nil); code != 200 {
		t.Fatalf("create the channel: %d %s", code, out)
	}
	srv.op(t, "build", "join", "lead.kf", "{}")
	repo := filepath.Join(t.TempDir(), "repo")
	writeTasksFeatureCard(t, repo, "in-progress", "OTHER-5", "swr", "web.kf", "web.kf", "Not the member's card")
	path := writeTasksTranscript(t, tasksTodoLine)
	reportTasksSession(t, srv, "s-empty", "build", "lead.kf", repo, path)

	code, _, body := srv.get(t, "/api/sessions/s-empty/tasks", nil)
	if code != 200 {
		t.Fatalf("tasks: %d %s", code, body)
	}
	page := bodyMap(t, body)
	cards, _ := page["cards"].([]any)
	if cards == nil || len(cards) != 0 {
		t.Errorf("cards = %v, want an empty list", cards)
	}
	if want := tasksBoardKey(t, repo); page["board"] != want {
		t.Errorf("board = %v, want the tab's key for the board root %s", page["board"], want)
	}
}

func TestTheSessionTasksRouteAnswersTheBoardCardsTheMemberHolds(t *testing.T) {
	srv := startServer(t, nil)
	if code, out := srv.post(t, "/api/channels", `{"name":"build"}`, nil); code != 200 {
		t.Fatalf("create the channel: %d %s", code, out)
	}
	srv.op(t, "build", "join", "api", "{}")
	repo := filepath.Join(t.TempDir(), "repo")
	writeTasksBoard(t, repo, "in-progress", "GATE-1", "api", "Free the gate")
	writeTasksBoard(t, repo, "ready", "OTHER-2", "web", "Someone else's card")
	writeTasksBoard(t, repo, "done", "MINE-3", "api", "Already finished")
	path := writeTasksTranscript(t, tasksTodoLine)
	reportTasksSession(t, srv, "s-cards", "build", "api", repo, path)

	code, _, body := srv.get(t, "/api/sessions/s-cards/tasks", nil)
	if code != 200 {
		t.Fatalf("tasks: %d %s", code, body)
	}
	cards, _ := bodyMap(t, body)["cards"].([]any)
	if len(cards) != 1 {
		t.Fatalf("cards = %v, want exactly the card the member holds in progress", cards)
	}
	card, _ := cards[0].(map[string]any)
	if card["id"] != "GATE-1" || card["title"] != "Free the gate" || card["column"] != "in-progress" {
		t.Errorf("the card = %v, want GATE-1 Free the gate in-progress", card)
	}
}

func TestTheSessionTasksRouteAnswersNoCardsWithoutABoardOfItsOwn(t *testing.T) {
	srv := startServer(t, nil)
	if code, out := srv.post(t, "/api/channels", `{"name":"build"}`, nil); code != 200 {
		t.Fatalf("create the channel: %d %s", code, out)
	}
	srv.op(t, "build", "join", "api", "{}")
	path := writeTasksTranscript(t, tasksTodoLine)
	reportTasksSession(t, srv, "s-nocwd", "build", "api", "", path)
	reportTasksSession(t, srv, "s-noboard", "build", "api", t.TempDir(), path)

	for _, id := range []string{"s-nocwd", "s-noboard"} {
		code, _, body := srv.get(t, "/api/sessions/"+id+"/tasks", nil)
		if code != 200 {
			t.Fatalf("%s tasks: %d %s", id, code, body)
		}
		cards, _ := bodyMap(t, body)["cards"].([]any)
		if cards == nil || len(cards) != 0 {
			t.Errorf("%s cards = %v, want an empty list", id, cards)
		}
	}
}
