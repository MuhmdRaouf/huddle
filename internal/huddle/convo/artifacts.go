package convo

// The artifacts of one session: what the Artifact calls of its main transcript published. Each
// artifact id is one row at its latest publish, in the order the ids were first published.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// artifactURLPrefix is the only origin a listed artifact's url may have.
const artifactURLPrefix = "https://claude.ai/"

// Artifact is one page the session published, as the latest successful Artifact call that named
// its id answered. At is that call's result line.
type Artifact struct {
	ID      string `json:"artifact_id"`
	URL     string `json:"url"`
	Title   string `json:"title"`
	Version string `json:"version"`
	At      string `json:"at"`
}

// artifactLine is one transcript line an artifact scan may need.
type artifactLine struct {
	Type          string          `json:"type"`
	Timestamp     string          `json:"timestamp"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
	Message       struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// artifactResult is the toolUseResult of a successful Artifact call; a failed call carries a
// string there instead.
type artifactResult struct {
	URL     string `json:"url"`
	ID      string `json:"artifact_id"`
	Title   string `json:"title"`
	Version string `json:"version"`
}

// A line without this mark holds no Artifact call or answer, so the scan never decodes it.
const artifactMark = "Artifact"

// Artifacts answers the artifacts the transcript read from r published. Only complete lines
// count. A call that failed, an answer missing its id or url, and a url outside claude.ai list
// nothing.
func Artifacts(r io.Reader) ([]Artifact, error) {
	artifacts := []Artifact{}
	at := map[string]int{}
	calls := map[string]bool{}
	reader := bufio.NewReader(r)
	for {
		raw, err := reader.ReadBytes('\n')
		if len(raw) > 0 && raw[len(raw)-1] == '\n' {
			takeArtifactLine(raw, calls, at, &artifacts)
			if err == nil {
				continue
			}
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		return artifacts, nil
	}
}

// takeArtifactLine takes one complete transcript line: an assistant line's Artifact calls are
// remembered by call id, and the user line answering one adds its artifact or replaces the row
// of the same id.
func takeArtifactLine(raw []byte, calls map[string]bool, at map[string]int, artifacts *[]Artifact) {
	if !bytes.Contains(raw, []byte(artifactMark)) && !bytes.Contains(raw, []byte("artifact_id")) {
		return
	}
	var line artifactLine
	if json.Unmarshal(raw, &line) != nil {
		return
	}
	var blocks []contentBlock
	if json.Unmarshal(line.Message.Content, &blocks) != nil {
		return
	}
	switch line.Type {
	case "assistant":
		for _, block := range blocks {
			if block.Type == "tool_use" && block.Name == "Artifact" {
				calls[block.ID] = true
			}
		}
	case "user":
		// the line's toolUseResult belongs to its first tool_result block
		for _, block := range blocks {
			if block.Type != "tool_result" {
				continue
			}
			if calls[block.ToolUseID] {
				takeArtifactResult(line, at, artifacts)
			}
			return
		}
	}
}

// takeArtifactResult adds the artifact the line's toolUseResult names, or replaces the row that
// has its id.
func takeArtifactResult(line artifactLine, at map[string]int, artifacts *[]Artifact) {
	var result artifactResult
	if json.Unmarshal(line.ToolUseResult, &result) != nil ||
		result.ID == "" || !strings.HasPrefix(result.URL, artifactURLPrefix) {
		return
	}
	row := Artifact{ID: result.ID, URL: result.URL, Title: result.Title, Version: result.Version, At: line.Timestamp}
	if i, ok := at[row.ID]; ok {
		(*artifacts)[i] = row
		return
	}
	at[row.ID] = len(*artifacts)
	*artifacts = append(*artifacts, row)
}
