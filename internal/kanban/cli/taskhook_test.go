package cli

// The TaskCreated hook's board half: a task Claude Code creates becomes one card CC-<short id> in
// cluster "cc tasks" on the project's board, once per task, and every failure is silence.

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
)

// taskPayload is the TaskCreated sample from Claude Code's hooks reference, with one field the
// hook does not read.
const taskPayload = `{"session_id":"abc123","transcript_path":"/nowhere.jsonl","cwd":"/nowhere",` +
	`"hook_event_name":"TaskCreated","task_id":"task-001","task_subject":"Implement user authentication",` +
	`"task_description":"Add login and signup endpoints","teammate_name":"implementer",` +
	`"team_name":"session-a1b2c3d4","unknown_field":[1,2]}`

// taskBoard answers a project dir holding a kanban.db board, and a world whose project dir is it.
func taskBoard(t *testing.T) (string, World) {
	t.Helper()
	project := t.TempDir()
	if _, err := board.InitDB(context.Background(), filepath.Join(project, ".agents", "kanban"), time.Now()); err != nil {
		t.Fatalf("init the board: %v", err)
	}
	env := map[string]string{"CLAUDE_PROJECT_DIR": project}
	w := World{
		Getenv: func(key string) (string, bool) { v, ok := env[key]; return v, ok },
		Cwd:    func() string { return t.TempDir() },
		Now:    func() time.Time { return time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC) },
	}
	return project, w
}

// boardCards lists every card on the project's board.
func boardCards(t *testing.T, project string) []board.Card {
	t.Helper()
	b, closeBoard, err := board.Open(context.Background(), board.Board{Root: filepath.Join(project, ".agents", "kanban")})
	if err != nil {
		t.Fatalf("open the board: %v", err)
	}
	defer closeBoard()
	cards, err := b.List(context.Background(), "")
	if err != nil {
		t.Fatalf("list the board: %v", err)
	}
	return cards
}

func TestTaskCreatedPutsOneCardInClusterCCTasks(t *testing.T) {
	project, w := taskBoard(t)
	TaskCreated(context.Background(), w, []byte(taskPayload))
	// the same task again (a replayed hook) never makes a second card
	TaskCreated(context.Background(), w, []byte(taskPayload))

	cards := boardCards(t, project)
	if len(cards) != 1 {
		t.Fatalf("the board holds %d cards, want 1: %+v", len(cards), cards)
	}
	card := cards[0]
	if !regexp.MustCompile(`^CC-[0-9A-F]{8}$`).MatchString(card.ID) {
		t.Errorf("id = %q, want CC-<8 hex digits>", card.ID)
	}
	if card.ID != TaskCardID("abc123", "task-001") {
		t.Errorf("id = %q, want TaskCardID's %q", card.ID, TaskCardID("abc123", "task-001"))
	}
	if card.Cluster != "cc tasks" {
		t.Errorf("cluster = %q, want cc tasks", card.Cluster)
	}
	if card.Title != "Implement user authentication" {
		t.Errorf("title = %q, want the task's subject", card.Title)
	}
	if card.Column != "backlog" {
		t.Errorf("column = %q, want backlog", card.Column)
	}
	for _, want := range []string{"task-001", "implementer", "Add login and signup endpoints"} {
		if !strings.Contains(card.Summary, want) {
			t.Errorf("summary = %q, want it to name %q", card.Summary, want)
		}
	}
}

func TestTaskCardIDIsShortStableAndPerSession(t *testing.T) {
	a, again, other := TaskCardID("abc123", "1"), TaskCardID("abc123", "1"), TaskCardID("def456", "1")
	if a != again {
		t.Errorf("TaskCardID is not stable: %q then %q", a, again)
	}
	if a == other {
		t.Errorf("two sessions' task 1 share the id %q", a)
	}
}

func TestTaskCreatedKeepsAnAwkwardSubjectOnTheCard(t *testing.T) {
	project, w := taskBoard(t)
	subject := `Say "hi" and it's \done ` + strings.Repeat("x", 120)
	TaskCreated(context.Background(), w,
		[]byte(`{"session_id":"s1","task_id":"7","task_subject":`+jsonString(t, subject)+`}`))
	cards := boardCards(t, project)
	if len(cards) != 1 {
		t.Fatalf("the board holds %d cards, want 1", len(cards))
	}
	if n := len(cards[0].Title); n == 0 || n > 90 {
		t.Errorf("title is %d bytes, want 1-90", n)
	}
	if !strings.HasPrefix(cards[0].Title, "Say ") {
		t.Errorf("title = %q, want it to start with the subject", cards[0].Title)
	}
}

func TestTaskCreatedIsSilentWithNothingToWrite(t *testing.T) {
	for name, payload := range map[string]string{
		"no subject": `{"session_id":"abc123","task_id":"task-001"}`,
		"no task id": `{"session_id":"abc123","task_subject":"x"}`,
		"not JSON":   `<html>`,
		"wrong type": `{"task_subject":42}`,
	} {
		t.Run(name, func(t *testing.T) {
			project, w := taskBoard(t)
			TaskCreated(context.Background(), w, []byte(payload))
			if cards := boardCards(t, project); len(cards) != 0 {
				t.Errorf("the board holds %+v, want nothing", cards)
			}
		})
	}
	t.Run("no board", func(t *testing.T) {
		empty := t.TempDir()
		w := World{
			Getenv: func(key string) (string, bool) {
				if key == "CLAUDE_PROJECT_DIR" {
					return empty, true
				}
				return "", false
			},
			Cwd: func() string { return empty },
			Now: time.Now,
		}
		TaskCreated(context.Background(), w, []byte(taskPayload))
		if matches, _ := filepath.Glob(filepath.Join(empty, "*")); len(matches) != 0 {
			t.Errorf("the hook wrote %v into a project with no board", matches)
		}
	})
}
