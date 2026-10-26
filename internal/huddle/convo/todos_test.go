package convo

// The todo reader (todos.go): the todo list a transcript ends with, its TodoWrite calls
// replacing it and its task calls growing it. Every test writes its own transcript into a temp
// dir; the line shapes are Claude Code 2.1.303's, the contract the reader reads.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// useLine writes one assistant line holding one tool call, its input as compact JSON.
func useLine(uuid, id, name, input string) string {
	return fmt.Sprintf(`{"type":"assistant","uuid":%q,"timestamp":"2026-10-26T10:00:00.000Z","message":{"role":"assistant","content":[{"type":"tool_use","id":%q,"name":%q,"input":%s}]}}`, uuid, id, name, input)
}

// resultLine writes one user line answering one tool call with its text.
func resultLine(uuid, useID, text string) string {
	return fmt.Sprintf(`{"type":"user","uuid":%q,"timestamp":"2026-10-26T10:00:01.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":%q}]}}`, uuid, useID, text)
}

// sidechainLine writes one subagent's assistant line holding one tool call.
func sidechainLine(uuid, id, name, input string) string {
	return fmt.Sprintf(`{"type":"assistant","uuid":%q,"isSidechain":true,"timestamp":"2026-10-26T10:00:02.000Z","message":{"role":"assistant","content":[{"type":"tool_use","id":%q,"name":%q,"input":%s}]}}`, uuid, id, name, input)
}

// writeTranscript writes one transcript of the given lines, each newline-terminated.
func writeTranscript(t *testing.T, lines ...string) string {
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

// appendRaw adds text to the transcript at path exactly as given, newline or not.
func appendRaw(t *testing.T, path, text string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(text); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

// todosOf reads one transcript's todo list for an assertion.
func todosOf(t *testing.T, path string) []Todo {
	t.Helper()
	todos, err := Todos(context.Background(), path)
	if err != nil {
		t.Fatalf("todos: %v", err)
	}
	return todos
}

// same answers whether the list read equals the one wanted, entry for entry.
func same(got, want []Todo) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestTodosAnswersEveryEntryOfAListOverTheCutSize(t *testing.T) {
	// sixty entries of about 150-byte contents: an input the conversation reader cuts at 4096
	// bytes, and one the todo reader must still answer whole
	entries := make([]string, 0, 60)
	for i := 0; i < 60; i++ {
		entries = append(entries, fmt.Sprintf(`{"content":"entry %d %s","status":"pending","activeForm":"working on entry %d"}`, i, strings.Repeat("detail ", 20), i))
	}
	input := `{"todos":[` + strings.Join(entries, ",") + `]}`
	if len(input) <= 8192 {
		t.Fatalf("the fixture input is %d bytes, want one over 8 KiB", len(input))
	}
	path := writeTranscript(t, useLine("a0", "toolu-0", "TodoWrite", input))

	todos := todosOf(t, path)
	if len(todos) != 60 {
		t.Fatalf("todos = %d entries, want all 60 the list held", len(todos))
	}
	first, last := todos[0], todos[59]
	wantFirst := Todo{ID: "0", Content: "entry 0 " + strings.Repeat("detail ", 20), Status: "pending", ActiveForm: "working on entry 0"}
	wantLast := Todo{ID: "59", Content: "entry 59 " + strings.Repeat("detail ", 20), Status: "pending", ActiveForm: "working on entry 59"}
	if first != wantFirst || last != wantLast {
		t.Errorf("first = %+v, last = %+v, want %+v and %+v", first, last, wantFirst, wantLast)
	}
}

func TestTodosAnswersTheListTheLastTodoWriteWrote(t *testing.T) {
	three := `[{"content":"write the tests","status":"in_progress","activeForm":"writing the tests"},{"content":"run the gates","status":"pending","activeForm":"running the gates"},{"content":"report","status":"completed"}]`
	two := `[{"content":"write the tests","status":"completed"},{"content":"report","status":"in_progress","activeForm":"reporting"}]`
	path := writeTranscript(t,
		useLine("a0", "toolu-0", "TodoWrite", `{"todos":`+three+`}`),
		useLine("a1", "toolu-1", "TodoWrite", `{"todos":`+two+`}`),
	)
	todos := todosOf(t, path)
	want := []Todo{
		{ID: "0", Content: "write the tests", Status: "completed"},
		{ID: "1", Content: "report", Status: "in_progress", ActiveForm: "reporting"},
	}
	if !same(todos, want) {
		t.Errorf("todos = %+v, want %+v", todos, want)
	}
}

func TestTodosFollowsTaskCreatesAndUpdates(t *testing.T) {
	path := writeTranscript(t,
		useLine("a0", "toolu-0", "TaskCreate", `{"subject":"ship the binary","description":"build and release","activeForm":"shipping the binary"}`),
		resultLine("u0", "toolu-0", "Task #7 created successfully: ship the binary"),
		useLine("a1", "toolu-1", "TaskCreate", `{"subject":"review the diff","activeForm":"reviewing the diff"}`),
		resultLine("u1", "toolu-1", "Task #9 created successfully: review the diff"),
		// a third create whose result never landed names no id, so it stays out
		useLine("a2", "toolu-2", "TaskCreate", `{"subject":"write the notes"}`),
	)
	want := []Todo{
		{ID: "7", Content: "ship the binary", Status: "pending", ActiveForm: "shipping the binary"},
		{ID: "9", Content: "review the diff", Status: "pending", ActiveForm: "reviewing the diff"},
	}
	if todos := todosOf(t, path); !same(todos, want) {
		t.Fatalf("todos = %+v, want %+v", todos, want)
	}

	// a TaskUpdate that completes the first leaves the second as it is
	appendRaw(t, path, useLine("a3", "toolu-3", "TaskUpdate", `{"taskId":"7","status":"completed","activeForm":"shipping the binary"}`)+"\n")
	completed := []Todo{
		{ID: "7", Content: "ship the binary", Status: "completed", ActiveForm: "shipping the binary"},
		{ID: "9", Content: "review the diff", Status: "pending", ActiveForm: "reviewing the diff"},
	}
	if todos := todosOf(t, path); !same(todos, completed) {
		t.Fatalf("todos = %+v, want %+v", todos, completed)
	}

	// a TaskUpdate with status deleted removes its entry from the list
	appendRaw(t, path, useLine("a4", "toolu-4", "TaskUpdate", `{"taskId":"9","status":"deleted"}`)+"\n")
	remaining := completed[:1]
	if todos := todosOf(t, path); !same(todos, remaining) {
		t.Fatalf("todos = %+v, want %+v", todos, remaining)
	}
}

func TestTodosSkipsSidechainTornAndQuietLines(t *testing.T) {
	mine := []Todo{{ID: "0", Content: "mine", Status: "pending"}}
	path := writeTranscript(t,
		useLine("a0", "toolu-0", "TodoWrite", `{"todos":[{"content":"mine","status":"pending"}]}`),
		// a subagent's own TodoWrite is not this conversation's list
		sidechainLine("s0", "toolu-s0", "TodoWrite", `{"todos":[{"content":"the subagent's","status":"in_progress"}]}`),
	)
	if todos := todosOf(t, path); !same(todos, mine) {
		t.Fatalf("todos = %+v, want %+v", todos, mine)
	}

	// a torn last line is not read: the half line that just landed changes nothing
	appendRaw(t, path, `{"type":"assistant","uuid":"a1","timestamp":"2026-10-26T10:00:03.000Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu-t","name":"TodoWrite","input":{"todos":[]}}`)
	if todos := todosOf(t, path); !same(todos, mine) {
		t.Fatalf("todos over a torn line = %+v, want %+v", todos, mine)
	}

	// a transcript whose lines hold no todo calls answers an empty list, never nil
	quiet := writeTranscript(t, `{"type":"user","uuid":"u9","timestamp":"2026-10-26T10:00:00.000Z","message":{"role":"user","content":"run the tests"}}`)
	todos := todosOf(t, quiet)
	if todos == nil || len(todos) != 0 {
		t.Errorf("quiet transcript = %+v, want an empty list", todos)
	}
}

func TestTodosAnswersTheOsErrorForAMissingFile(t *testing.T) {
	_, err := Todos(context.Background(), filepath.Join(t.TempDir(), "missing.jsonl"))
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a missing file: err = %v, want the os error", err)
	}
}
