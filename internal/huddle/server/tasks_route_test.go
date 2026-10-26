package server

// The session tasks route (tasks_route.go): one GET naming a session's own todo list, read from
// the transcript its reports named, beside the channel tasks its member owns. Every test runs
// its own server child against its own transcript and its own channels.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// reportTasksSession registers one live session in a channel as a member; a path names its
// transcript.
func reportTasksSession(t *testing.T, srv *childServer, id, channel, member, path string) {
	t.Helper()
	body := fmt.Sprintf(`{"session_id":%q,"machine":"box","last_event":"UserPromptSubmit","last_event_at":%q`, id, isoAgo(time.Second))
	if channel != "" {
		body += fmt.Sprintf(`,"channel":%q,"member":%q`, channel, member)
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
	reportTasksSession(t, srv, "s-1", "build", "api", path)

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
	reportTasksSession(t, srv, "s-lone", "", "", path)

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
	reportTasksSession(t, srv, "s-gone", "vanished", "api", path)

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
