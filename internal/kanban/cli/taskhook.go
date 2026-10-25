package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
)

// The TaskCreated hook's board half: a task Claude Code creates through TaskCreate becomes one
// backlog card on the project's board, so work a lead splits up on its own shows beside the
// board's cards. The card's id is derived from the session and the task, so a replayed hook
// finds its card already there and writes nothing. No board, a payload without a task, or any
// refusal ends in silence: a hook never costs a session anything.

// taskCluster is the cluster every mirrored task's card sits in. A cluster is one or two
// lowercase words of letters and digits, so the words are "cc tasks".
const taskCluster = "cc tasks"

// taskActor is the actor the board records for a mirrored task's card.
const taskActor = "claude-code"

// taskTitleMax is the board's longest title, in bytes.
const taskTitleMax = 90

// taskInput is the part of Claude Code's TaskCreated payload the board reads; every other field
// is ignored.
type taskInput struct {
	SessionID       string `json:"session_id"`
	Cwd             string `json:"cwd"`
	TaskID          string `json:"task_id"`
	TaskSubject     string `json:"task_subject"`
	TaskDescription string `json:"task_description"`
	TeammateName    string `json:"teammate_name"`
}

// TaskCardID answers the board id of one Claude Code task: CC- and the first 8 hex digits of the
// SHA-256 of the session and task ids. Task ids restart in every session, so the session is part
// of the key.
func TaskCardID(sessionID, taskID string) string {
	sum := sha256.Sum256([]byte(sessionID + "\x00" + taskID))
	return "CC-" + strings.ToUpper(hex.EncodeToString(sum[:4]))
}

// TaskCreated writes the card for the task the payload names onto the board of Claude Code's
// project dir (else the payload's cwd, else the world's), and does nothing when there is no
// board, no task, or the card exists.
func TaskCreated(ctx context.Context, w World, payload []byte) {
	var in taskInput
	if json.Unmarshal(payload, &in) != nil || in.TaskID == "" || strings.TrimSpace(in.TaskSubject) == "" {
		return
	}
	from, ok := w.Getenv("CLAUDE_PROJECT_DIR")
	if !ok || from == "" {
		from = in.Cwd
	}
	if from == "" {
		from = w.Cwd()
	}
	b, err := board.Resolve("", w.Getenv, from)
	if err != nil {
		return
	}
	b, closeBoard, err := board.Open(ctx, b)
	if err != nil {
		return
	}
	defer closeBoard()
	b.Now = w.Now
	_, _ = b.New(ctx, taskTicket(in))
}

// taskTicket is the card one task becomes: its subject as the title, and a summary naming the
// task, who created it and its description.
func taskTicket(in taskInput) board.NewTicket {
	creator := "the lead"
	if in.TeammateName != "" {
		creator = "teammate " + in.TeammateName
	}
	summary := fmt.Sprintf("Claude Code task %s, created by %s in session %s; the session's task list stays its source.",
		in.TaskID, creator, in.SessionID)
	if in.TaskDescription != "" {
		summary += " " + in.TaskDescription
	}
	return board.NewTicket{
		Actor:   taskActor,
		ID:      TaskCardID(in.SessionID, in.TaskID),
		Title:   cutBytes(storable(in.TaskSubject), taskTitleMax),
		Cluster: taskCluster,
		Summary: storable(summary),
	}
}

// storable rewrites text the board's front matter cannot hold: a value carries one quote kind
// and no backslash beside a single quote, so double quotes become single ones and backslashes go.
func storable(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(strings.Join(strings.Fields(text), " "), `"`, "'"), `\`, "")
}

// cutBytes cuts text to at most max bytes on a rune boundary.
func cutBytes(text string, max int) string {
	if len(text) <= max {
		return text
	}
	cut := text[:max]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return strings.TrimSpace(cut)
}
