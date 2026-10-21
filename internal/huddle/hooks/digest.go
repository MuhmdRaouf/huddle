// The session digest (SPEC 8): what the Stop hook builds from its own transcript so a linked
// peer can take over the session's context without reading it. No model call — the goal is the
// newest prompt, the files come from the edit tool calls, the decisions and open questions from
// the last assistant words — and nothing else of the transcript (tool results, commands, paths
// in outputs) ever reaches it. The whole text stays under digestMax bytes.
package hooks

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/MuhmdRaouf/huddle/internal/huddle/prompttext"
)

const (
	// digestMax is the most a digest may weigh, in bytes; past it the text is cut at a rune
	// boundary and closed with an ellipsis.
	digestMax = 1500
	// digestTail is how much of the transcript the digest reads: its last 256 KiB, where the
	// session's newest work sits.
	digestTail = 256 * 1024
)

// digestOf reads the transcript's tail and answers its digest; a transcript that cannot be read
// answers "", because a Stop hook that cannot build a digest still must not block the session.
func digestOf(path, cwd string) string {
	if path == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	start := max(0, info.Size()-digestTail)
	buf := make([]byte, info.Size()-start)
	if _, err := file.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return ""
	}
	body := string(buf)
	if start > 0 {
		// the window may open mid-line: the first partial line is dropped whole, never read
		// as the broken JSON it is
		at := strings.IndexByte(body, '\n')
		if at < 0 {
			return ""
		}
		body = body[at+1:]
	}
	raw := strings.Split(body, "\n")
	lines := make([][]byte, 0, len(raw))
	for _, line := range raw {
		lines = append(lines, []byte(line))
	}
	return buildDigest(lines, cwd)
}

// digestEntry is one transcript line's shape: the kind of turn and its message content, which
// is a plain string for a typed prompt and a block array otherwise. IsMeta marks a line the
// harness injected (a skill's body), never words the person typed.
type digestEntry struct {
	Type    string `json:"type"`
	IsMeta  bool   `json:"isMeta"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// digestBlock is one content block: a text block carries words, a tool_use block names the tool
// and its input, a tool_result block carries a command's output.
type digestBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// digestInput is the input a file tool call carries.
type digestInput struct {
	FilePath     string `json:"file_path"`
	NotebookPath string `json:"notebook_path"`
}

// digestFileTools maps the tools whose input names a file the session touched.
var digestFileTools = map[string]string{
	"Edit":         "file_path",
	"Write":        "file_path",
	"MultiEdit":    "file_path",
	"NotebookEdit": "notebook_path",
}

// digestCaps bound the digest's sections: the counts keep a long session's digest about the
// session's end, not its whole history.
const (
	digestGoalRunes = 200
	digestMaxFiles  = 10
	digestMaxLines  = 5
)

// buildDigest answers the digest of transcript lines, newest last, relative to cwd. Pure: the
// golden files under testdata/digest pin its bytes.
func buildDigest(lines [][]byte, cwd string) string {
	goal := ""
	var files []string
	seen := map[string]bool{}
	text := "" // the newest assistant message's words
	for _, line := range lines {
		var entry digestEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		switch entry.Type {
		case "user":
			// a skill body Claude Code injects is a meta user line, and a command or tool
			// wrapper is harness plumbing: the goal is what the person typed, so both are
			// skipped and an earlier prompt keeps it
			if entry.IsMeta {
				continue
			}
			if said := userTextOf(entry.Message.Content); said != "" && !prompttext.IsHarness(said) {
				goal = said
			}
		case "assistant":
			var blocks []digestBlock
			if err := json.Unmarshal(entry.Message.Content, &blocks); err != nil {
				continue
			}
			said := ""
			for _, block := range blocks {
				if block.Type != "tool_use" {
					continue
				}
				kind, ok := digestFileTools[block.Name]
				if !ok {
					continue
				}
				var input digestInput
				if err := json.Unmarshal(block.Input, &input); err != nil {
					continue
				}
				path := input.FilePath
				if kind == "notebook_path" {
					path = input.NotebookPath
				}
				if path == "" || seen[path] {
					continue
				}
				seen[path] = true
				if len(files) < digestMaxFiles {
					files = append(files, digestRel(cwd, path))
				}
			}
			for _, block := range blocks {
				if block.Type == "text" && block.Text != "" {
					if said != "" {
						said += "\n"
					}
					said += block.Text
				}
			}
			if said != "" {
				text = said
			}
		}
	}
	decisions, open := digestDecisionsAndOpen(text)
	sections := []string{}
	if goal != "" {
		sections = append(sections, "Goal: "+cutRunes(firstLineOf(goal), digestGoalRunes))
	}
	if len(decisions) > 0 {
		sections = append(sections, "Decisions:")
		for _, line := range decisions {
			sections = append(sections, "- "+line)
		}
	}
	if len(files) > 0 {
		sections = append(sections, "Files: "+strings.Join(files, ", "))
	}
	if len(open) > 0 {
		sections = append(sections, "Open:")
		for _, line := range open {
			sections = append(sections, "- "+line)
		}
	}
	if len(sections) == 0 {
		return ""
	}
	return capDigest(strings.Join(sections, "\n"))
}

// userTextOf answers what a user line said: the content when it is a plain string, else the
// first text block of an array that carries no tool result — a turn of pure results said
// nothing worth a goal.
func userTextOf(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	if content[0] == '"' {
		var said string
		if err := json.Unmarshal(content, &said); err != nil {
			return ""
		}
		return said
	}
	var blocks []digestBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return ""
	}
	for _, block := range blocks {
		if block.Type == "tool_result" {
			return ""
		}
	}
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			return block.Text
		}
	}
	return ""
}

// digestDecisionsAndOpen splits a session's last words into the decisions it made and the
// questions it left open. A line is a decision when it starts (after the list markers) with
// Decided or Decision, and open when it ends with ? or starts with Open or Question; a
// decision is never also open. At most digestMaxLines of each, each cut to digestGoalRunes.
func digestDecisionsAndOpen(text string) (decisions, open []string) {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		stripped := strings.TrimLeft(trimmed, " -*")
		lowered := strings.ToLower(stripped)
		switch {
		case strings.HasPrefix(lowered, "decided") || strings.HasPrefix(lowered, "decision"):
			if len(decisions) < digestMaxLines {
				decisions = append(decisions, cutRunes(trimmed, digestGoalRunes))
			}
		case strings.HasSuffix(trimmed, "?") ||
			strings.HasPrefix(lowered, "open") || strings.HasPrefix(lowered, "question"):
			if len(open) < digestMaxLines {
				open = append(open, cutRunes(trimmed, digestGoalRunes))
			}
		}
	}
	return decisions, open
}

// digestRel answers path relative to cwd when it sits under it, else path whole: the digest
// names files the way the session's own repo does.
func digestRel(cwd, path string) string {
	if cwd == "" || path == "" || !filepath.IsAbs(path) {
		return path
	}
	rel, err := filepath.Rel(cwd, path)
	if err != nil || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return rel
}

// capDigest holds a digest at digestMax bytes: the text is cut at the last rune boundary that
// leaves room for the ellipsis that says the rest was dropped.
func capDigest(text string) string {
	if len(text) <= digestMax {
		return text
	}
	room := digestMax - len("…")
	for room > 0 && !utf8.RuneStart(text[room]) {
		room--
	}
	return text[:room] + "…"
}

// cutRunes answers s cut to n runes, so a digest never splits a letter in half.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
