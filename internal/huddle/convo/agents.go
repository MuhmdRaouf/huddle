package convo

// The agents of one session: the main conversation's session plus the subagents and side chats
// its transcript's subagents folder holds, listed from the meta files Claude Code writes beside
// them, and the transcript of any one of them found by id.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// MainAgent is the id of a session's own conversation.
const MainAgent = "main"

// agentIDPattern is what an agent id may look like: the ids Claude Code generates and nothing
// that could name a path outside the subagents folder.
var agentIDPattern = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)

// Agent is one of a session's agents as its transcripts and meta files describe it.
type Agent struct {
	ID            string `json:"id"`   // "main", or the subagent's id
	Type          string `json:"type"` // "main", or the meta file's agentType
	Description   string `json:"description,omitempty"`
	Depth         int    `json:"depth"`   // 0 for the main, else spawnDepth
	Running       bool   `json:"running"` // the main: true; a subagent: !finished && !stoppedByUser
	Model         string `json:"model,omitempty"`
	SideChat      bool   `json:"side_chat,omitempty"`       // a /btw fork: meta agentType "fork" or isFork
	StartedAt     string `json:"started_at,omitempty"`      // the transcript's first line's timestamp
	LastText      string `json:"last_text,omitempty"`       // the transcript's last assistant text, cut to 500 bytes
	ToolUseID     string `json:"tool_use_id,omitempty"`     // the Agent tool call that started the subagent
	Parent        string `json:"parent,omitempty"`          // the meta's parentAgentId, else the main conversation
	StoppedByUser bool   `json:"stopped_by_user,omitempty"` // the owner stopped it
}

// agentMeta is one meta file's shape.
type agentMeta struct {
	AgentType     string `json:"agentType"`
	Description   string `json:"description"`
	SpawnDepth    int    `json:"spawnDepth"`
	Model         string `json:"model"`
	Finished      bool   `json:"finished"`
	IsFork        bool   `json:"isFork"`
	ToolUseID     string `json:"toolUseId"`
	ParentAgentID string `json:"parentAgentId"`
	StoppedByUser bool   `json:"stoppedByUser"`
}

// Agents answers the session whose main transcript is at path: the main first, then every
// subagent in its subagents folder by depth, then by StartedAt, then by id, then every side chat
// in that same order. A subagent whose meta file is missing or unreadable still lists, as a
// plain depth-1 subagent that has finished. A session with no subagents folder answers the main
// alone; a path with no main transcript answers the os error.
func Agents(ctx context.Context, path string) ([]Agent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	agents := []Agent{{
		ID:        MainAgent,
		Type:      "main",
		Running:   true,
		StartedAt: firstTimestamp(path),
		LastText:  lastAssistantText(path),
	}}
	entries, err := os.ReadDir(SubagentsDir(path))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return agents, nil
		}
		return nil, err
	}
	var subs []Agent
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id, ok := agentIDOf(entry)
		if !ok {
			continue
		}
		agent := Agent{ID: id, Type: "subagent", Depth: 1, Parent: MainAgent}
		if meta, err := readMeta(filepath.Join(SubagentsDir(path), "agent-"+id+".meta.json")); err == nil {
			agent.Type = meta.AgentType
			agent.Description = meta.Description
			agent.Depth = meta.SpawnDepth
			agent.Model = meta.Model
			agent.Running = !meta.Finished && !meta.StoppedByUser
			agent.SideChat = meta.AgentType == "fork" || meta.IsFork
			agent.ToolUseID = meta.ToolUseID
			if meta.ParentAgentID != "" {
				agent.Parent = meta.ParentAgentID
			}
			agent.StoppedByUser = meta.StoppedByUser
		}
		transcript := filepath.Join(SubagentsDir(path), entry.Name())
		agent.StartedAt = firstTimestamp(transcript)
		agent.LastText = lastAssistantText(transcript)
		subs = append(subs, agent)
	}
	sort.SliceStable(subs, func(i, j int) bool {
		if subs[i].SideChat != subs[j].SideChat {
			return !subs[i].SideChat
		}
		if subs[i].Depth != subs[j].Depth {
			return subs[i].Depth < subs[j].Depth
		}
		if subs[i].StartedAt != subs[j].StartedAt {
			return subs[i].StartedAt < subs[j].StartedAt
		}
		return subs[i].ID < subs[j].ID
	})
	return append(agents, subs...), nil
}

// AgentPath answers the transcript of one of the session's agents: the main's own path, or the
// subagents file the id names. An id that is not an agent id, one whose transcript the disk does
// not have, and any subagent of a session with no transcript answer false.
func AgentPath(path, id string) (string, bool) {
	if id == MainAgent {
		return path, true
	}
	if path == "" || !agentIDPattern.MatchString(id) {
		return "", false
	}
	transcript := filepath.Join(SubagentsDir(path), "agent-"+id+".jsonl")
	if _, err := os.Stat(transcript); err != nil {
		return "", false
	}
	return transcript, true
}

// SubagentsDir answers the folder a main transcript's subagents live beside: Claude Code lays
// <session id>.jsonl and <session id>/subagents side by side.
func SubagentsDir(path string) string {
	return filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents")
}

// agentIDOf answers the agent id one subagents entry holds: a file named agent-<id>.jsonl. Every
// other entry — a directory, a meta file, a name without an id — is none.
func agentIDOf(entry fs.DirEntry) (string, bool) {
	if entry.IsDir() || !strings.HasPrefix(entry.Name(), "agent-") || !strings.HasSuffix(entry.Name(), ".jsonl") {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), "agent-"), ".jsonl")
	if !agentIDPattern.MatchString(id) {
		return "", false
	}
	return id, true
}

// readMeta reads one subagent's meta file.
func readMeta(path string) (agentMeta, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return agentMeta{}, err
	}
	var meta agentMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return agentMeta{}, err
	}
	return meta, nil
}

// firstTimestamp answers the timestamp of a transcript's first line: the first 64 KiB read, the
// first complete line of them decoded for its timestamp alone. A file whose first line the
// window cannot end — longer than the window, or torn mid-write — answers "".
func firstTimestamp(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	window := make([]byte, 64<<10)
	n, err := io.ReadFull(file, window)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return ""
	}
	end := bytes.IndexByte(window[:n], '\n')
	if end < 0 {
		return ""
	}
	var first struct {
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal(window[:end], &first); err != nil {
		return ""
	}
	return first.Timestamp
}

// maxLastText is the most bytes of one agent's last text kept; a longer one is cut on a rune
// boundary.
const maxLastText = 500

// lastAssistantText answers the text of a transcript's last assistant message: the last 64 KiB
// read, the window's first line dropped when the window starts mid-file and the last line dropped
// when the writer has not ended it yet, the complete lines scanned from the end for the newest one
// that carries an assistant text block. A file the window cannot answer — unreadable, or a window
// whose complete lines carry no assistant text — answers "".
func lastAssistantText(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	window := make([]byte, 64<<10)
	start := info.Size() - int64(len(window))
	if start < 0 {
		start = 0
	}
	n, err := file.ReadAt(window, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return ""
	}
	body := window[:n]
	// the window of a longer file starts inside a line: that first line is torn
	if start > 0 {
		i := bytes.IndexByte(body, '\n')
		if i < 0 {
			return ""
		}
		body = body[i+1:]
	}
	// the last line is the one the writer may still be writing: no newline behind it, no answer
	if len(body) > 0 && body[len(body)-1] != '\n' {
		i := bytes.LastIndexByte(body, '\n')
		if i < 0 {
			return ""
		}
		body = body[:i+1]
	}
	for end := len(body); end > 0; {
		lineStart := bytes.LastIndexByte(body[:end-1], '\n') + 1
		if text, ok := assistantText(body[lineStart : end-1]); ok {
			return cutLastText(text)
		}
		end = lineStart
	}
	return ""
}

// assistantText answers one transcript line's assistant text: its last text block's words. A line
// that is not an assistant turn, or one whose text blocks all hold nothing, answers false.
func assistantText(line []byte) (string, bool) {
	var decoded struct {
		Type    string `json:"type"`
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &decoded); err != nil {
		return "", false
	}
	if decoded.Type != KindAssistant {
		return "", false
	}
	for i := len(decoded.Message.Content) - 1; i >= 0; i-- {
		if block := decoded.Message.Content[i]; block.Type == "text" && block.Text != "" {
			return block.Text, true
		}
	}
	return "", false
}

// cutLastText cuts an agent's last text to maxLastText bytes on a rune boundary.
func cutLastText(s string) string {
	if len(s) <= maxLastText {
		return s
	}
	kept := s[:maxLastText]
	// back off the bytes of a rune the cut split
	for len(kept) > 0 {
		if r, size := utf8.DecodeLastRuneInString(kept); r != utf8.RuneError || size != 1 {
			break
		}
		kept = kept[:len(kept)-1]
	}
	return kept
}
