package convo

// The turn reader: what a transcript says about the session's current turn — when the turn
// started, whether it has ended, and the permission mode in force. It reads its own lines so a
// transcript of any size costs one byte scan per line, and the footer counts the elapsed time
// itself from the start it is given.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
)

// TurnFacts is what a transcript says about the session's current turn.
type TurnFacts struct {
	StartedAt      string `json:"started_at,omitempty"`      // the last typed prompt's timestamp
	Ended          bool   `json:"ended"`                     // a turn_duration line follows that prompt
	DurationMS     int64  `json:"duration_ms,omitempty"`     // that line's durationMs, once ended
	PermissionMode string `json:"permission_mode,omitempty"` // the last permission-mode line's mode
}

// turnLine is one transcript line's shape as the turn reader reads it: the kind of turn, its
// timestamp, and the fields that say a turn ended or name the permission mode. IsMeta marks a
// line the harness injected and IsSidechain a subagent's line; neither is a prompt the owner
// typed. The message content stays raw, decoded only for a user line.
type turnLine struct {
	Type           string `json:"type"`
	Subtype        string `json:"subtype"`
	Timestamp      string `json:"timestamp"`
	DurationMS     int64  `json:"durationMs"`
	PermissionMode string `json:"permissionMode"`
	IsMeta         bool   `json:"isMeta"`
	IsSidechain    bool   `json:"isSidechain"`
	Message        struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// Turn reads one transcript and answers its current turn's facts: the start is the last typed
// prompt's timestamp, a turn_duration line after that prompt ends the turn, and the last
// permission-mode line's mode is the mode in force. It reads complete lines only — a torn last
// line is not read — a meta or sidechain line never starts a turn, and a transcript with no
// prompt answers no start and an unended turn.
func Turn(ctx context.Context, path string) (TurnFacts, error) {
	file, err := os.Open(path)
	if err != nil {
		return TurnFacts{}, err
	}
	defer file.Close()
	facts := TurnFacts{}
	reader := bufio.NewReader(file)
	for {
		if err := ctx.Err(); err != nil {
			return TurnFacts{}, err
		}
		raw, err := reader.ReadBytes('\n')
		if len(raw) > 0 && raw[len(raw)-1] == '\n' {
			if holdsTurnMark(raw) {
				foldTurnLine(raw, &facts)
			}
			continue
		}
		// a short read without its newline is a torn last line: the file's end, or a line
		// still being written, and neither is read
		if err != nil && !errors.Is(err, io.EOF) {
			return TurnFacts{}, err
		}
		break
	}
	return facts, nil
}

// foldTurnLine decodes one complete transcript line and folds its turn changes in: a typed
// prompt starts the turn over, a turn_duration line ends it, and a permission-mode line names
// the mode in force.
func foldTurnLine(raw []byte, facts *TurnFacts) {
	var line turnLine
	if json.Unmarshal(raw, &line) != nil {
		return
	}
	switch {
	case line.Type == "permission-mode":
		if line.PermissionMode != "" {
			facts.PermissionMode = line.PermissionMode
		}
	case line.Type == "system" && line.Subtype == "turn_duration":
		// an end names a turn only when a prompt started one
		if facts.StartedAt != "" {
			facts.Ended = true
			facts.DurationMS = line.DurationMS
		}
	case line.Type == KindUser && isTypedPrompt(line):
		facts.StartedAt = line.Timestamp
		facts.Ended = false
		facts.DurationMS = 0
	}
}

// isTypedPrompt answers whether the user line is a prompt the owner typed: a string content
// with words, or a block array holding a text block with words and no tool_result — a line
// answering a tool call is the tool's answer, not a prompt.
func isTypedPrompt(line turnLine) bool {
	if line.IsMeta || line.IsSidechain {
		return false
	}
	var prompt string
	if err := json.Unmarshal(line.Message.Content, &prompt); err == nil {
		return strings.TrimSpace(prompt) != ""
	}
	var blocks []contentBlock
	if err := json.Unmarshal(line.Message.Content, &blocks); err != nil {
		return false
	}
	words := false
	for _, block := range blocks {
		if block.Type == "tool_result" {
			return false
		}
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			words = true
		}
	}
	return words
}

// holdsTurnMark answers whether the line's bytes could carry a turn change: a user line, a
// system line, or a permission-mode line. Anything else is skipped before any decode.
func holdsTurnMark(raw []byte) bool {
	return bytes.Contains(raw, []byte(`"user"`)) ||
		bytes.Contains(raw, []byte(`"system"`)) ||
		bytes.Contains(raw, []byte(`"permission-mode"`))
}
