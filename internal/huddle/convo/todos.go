package convo

// The todo reader: the todo list a session's transcript ends with — the last TodoWrite's list,
// grown by the TaskCreates whose results named their ids and shaped by the TaskUpdates. It
// reads its own lines so a call's input is never cut and a transcript of any size costs one
// byte scan per line.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strconv"
)

// Todo is one entry of a session's own todo list.
type Todo struct {
	ID         string `json:"id"`      // TaskCreate's id, or the TodoWrite entry's index as text
	Content    string `json:"content"` // the subject or the content
	Status     string `json:"status"`  // pending, in_progress or completed
	ActiveForm string `json:"active_form,omitempty"`
}

// taskCreatedID answers the id a TaskCreate's tool_result names: "Task #7 created
// successfully: …".
var taskCreatedID = regexp.MustCompile(`^Task #([^ ]+) created successfully`)

// todoMarks are the byte strings a line must hold for the todo reader to decode it: the three
// task calls' names and the tool_result text that names a created task's id. A line holding
// none of them cannot carry a todo change, so it is skipped before any decode.
var todoMarks = [...][]byte{
	[]byte(`"TodoWrite"`),
	[]byte(`"TaskCreate"`),
	[]byte(`"TaskUpdate"`),
	[]byte(`created successfully`),
}

// todoWriteInput is one TodoWrite call's input: the whole list, replacing whatever stood.
type todoWriteInput struct {
	Todos []struct {
		Content    string `json:"content"`
		Status     string `json:"status"`
		ActiveForm string `json:"activeForm"`
	} `json:"todos"`
}

// taskCreateInput is one TaskCreate call's input.
type taskCreateInput struct {
	Subject    string `json:"subject"`
	ActiveForm string `json:"activeForm"`
}

// taskUpdateInput is one TaskUpdate call's input; the pointer fields tell an absent field from
// an empty one.
type taskUpdateInput struct {
	TaskID     string  `json:"taskId"`
	Subject    *string `json:"subject"`
	ActiveForm *string `json:"activeForm"`
	Status     *string `json:"status"`
}

// taskCreate is one TaskCreate still waiting for the tool_result that names its id.
type taskCreate struct {
	todo Todo
	use  string
}

// Todos reads one transcript and answers the todo list it ends with: the last TodoWrite's
// list, then every TaskCreate whose result named its id, in creation order, with the
// TaskUpdates applied. It reads complete lines only — a torn last line is not read — a
// subagent's lines are not this conversation's list, and a transcript with no todo calls
// answers an empty list.
func Todos(ctx context.Context, path string) ([]Todo, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	todos := []Todo{}
	pending := []taskCreate{}
	reader := bufio.NewReader(file)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := reader.ReadBytes('\n')
		if len(raw) > 0 && raw[len(raw)-1] == '\n' {
			if holdsTodoMark(raw) {
				foldTodoLine(raw, &todos, &pending)
			}
			continue
		}
		// a short read without its newline is a torn last line: the file's end, or a line
		// still being written, and neither is read
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		break
	}
	for _, task := range pending {
		if task.todo.ID != "" {
			todos = append(todos, task.todo)
		}
	}
	return todos, nil
}

// holdsTodoMark answers whether the line's bytes hold any of todoMarks.
func holdsTodoMark(raw []byte) bool {
	for _, mark := range todoMarks {
		if bytes.Contains(raw, mark) {
			return true
		}
	}
	return false
}

// foldTodoLine decodes one complete transcript line and folds its todo changes in: a TodoWrite
// replaces the list whole, a TaskCreate waits for the result that names its id, a TaskUpdate
// shapes or removes its entry, and a tool_result names the created one. The calls are decoded
// from the raw line, where no input is ever cut.
func foldTodoLine(raw []byte, todos *[]Todo, pending *[]taskCreate) {
	var line transcriptLine
	if json.Unmarshal(raw, &line) != nil {
		return
	}
	if line.IsSidechain || line.IsMeta {
		return
	}
	var blocks []contentBlock
	if json.Unmarshal(line.Message.Content, &blocks) != nil {
		return
	}
	switch line.Type {
	case KindAssistant:
		for _, block := range blocks {
			if block.Type != "tool_use" {
				continue
			}
			switch block.Name {
			case "TodoWrite":
				var in todoWriteInput
				if json.Unmarshal(block.Input, &in) != nil {
					continue // a call whose input never parsed names no list to hold
				}
				*todos = (*todos)[:0]
				for i, entry := range in.Todos {
					*todos = append(*todos, Todo{
						ID:         strconv.Itoa(i),
						Content:    entry.Content,
						Status:     entry.Status,
						ActiveForm: entry.ActiveForm,
					})
				}
				*pending = (*pending)[:0] // the list is replaced whole: nothing older survives
			case "TaskCreate":
				var in taskCreateInput
				if json.Unmarshal(block.Input, &in) != nil {
					continue
				}
				*pending = append(*pending, taskCreate{
					todo: Todo{Content: in.Subject, Status: "pending", ActiveForm: in.ActiveForm},
					use:  block.ID,
				})
			case "TaskUpdate":
				var in taskUpdateInput
				if json.Unmarshal(block.Input, &in) != nil {
					continue
				}
				applyTaskUpdate(todos, *pending, in)
			}
		}
	case KindUser:
		for _, block := range blocks {
			if block.Type == "tool_result" {
				fillCreatedID(*pending, block.ToolUseID, resultText(block.Content))
			}
		}
	}
}

// fillCreatedID names the created entry whose TaskCreate the tool_result answers.
func fillCreatedID(pending []taskCreate, useID, text string) {
	if useID == "" {
		return
	}
	for i := range pending {
		if pending[i].use == useID && pending[i].todo.ID == "" {
			if got := taskCreatedID.FindStringSubmatch(text); got != nil {
				pending[i].todo.ID = got[1]
			}
		}
	}
}

// applyTaskUpdate shapes one entry of the list, or removes it when its status is deleted. A
// created entry updates too: its result usually lands before its first update.
func applyTaskUpdate(todos *[]Todo, pending []taskCreate, in taskUpdateInput) {
	if in.TaskID == "" {
		return
	}
	list := *todos
	for i := range list {
		if list[i].ID != in.TaskID {
			continue
		}
		if in.Status != nil && *in.Status == "deleted" {
			*todos = append(list[:i], list[i+1:]...)
			return
		}
		if in.Subject != nil {
			list[i].Content = *in.Subject
		}
		if in.ActiveForm != nil {
			list[i].ActiveForm = *in.ActiveForm
		}
		if in.Status != nil {
			list[i].Status = *in.Status
		}
		*todos = list
		return
	}
	for i := range pending {
		if pending[i].todo.ID != in.TaskID {
			continue
		}
		if in.Status != nil && *in.Status == "deleted" {
			pending[i].todo.ID = "" // unnamed again: the list leaves it out
			return
		}
		if in.Subject != nil {
			pending[i].todo.Content = *in.Subject
		}
		if in.ActiveForm != nil {
			pending[i].todo.ActiveForm = *in.ActiveForm
		}
		if in.Status != nil {
			pending[i].todo.Status = *in.Status
		}
		return
	}
}
