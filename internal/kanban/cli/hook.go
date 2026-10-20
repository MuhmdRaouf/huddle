package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	"github.com/MuhmdRaouf/huddle/internal/kanban/server"
)

// The session hook. Claude Code runs it at every session start of a project;
// it puts the plugin's bin on the session's PATH and answers one line of
// context naming the board's page in the huddle hub — the hub serves every
// board in process, so the hook starts no server of its own. Anything that
// fails ends in silence and exit 0: a hook never costs a session its start.

// hookOutput is the hook's one answer: the document Claude Code's hook
// protocol reads, one compact line.
type hookOutput struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// runHook runs the hook commands: session-start. boardPath is the explicit
// --board value when the launcher passed one.
func runHook(ctx context.Context, w World, positional []string, boardPath string, stdout, stderr io.Writer) int {
	if word := at(positional, 0); word != "session-start" {
		usageError(stderr, commands["hook"], word+" is not a hook word; session-start is")
		return 2
	}
	hookOnPath(w)
	from, ok := w.Getenv("CLAUDE_PROJECT_DIR")
	if !ok || from == "" {
		from = w.Cwd()
	}
	b, err := board.Resolve(boardPath, w.Getenv, from)
	if err != nil {
		return 0 // no board here: the hook has nothing to say
	}
	b, closeBoard, err := board.Open(ctx, b)
	if err != nil {
		return 0 // an unreadable board costs the session one line, nothing else
	}
	defer closeBoard()
	b.Now = w.Now
	abs := b.Root
	if resolved, err := filepath.Abs(b.Root); err == nil {
		abs = resolved
	}
	st, err := b.Status(ctx)
	if err != nil {
		return 0
	}
	open, inProgress, inReview := hookCounts(st)
	counts := fmt.Sprintf(".agents/kanban (%d open, %d in progress, %d in review)", open, inProgress, inReview)
	line := counts + ". Open it in the huddle hub once it runs (/huddle:setup)."
	if link, ok := hubLink(ctx, w, abs); ok {
		line = counts + " in the huddle hub at " + link + "."
	}
	line = "Kanban board: " + line + " Use the kanban skill to work it; move tickets only with the kanban command."
	var out hookOutput
	out.HookSpecificOutput.HookEventName = "SessionStart"
	out.HookSpecificOutput.AdditionalContext = line
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(out)
	return 0
}

// hubLink answers the board's page in the local huddle hub: the key the hub's
// tab lists the board under, resolved the same way — the repository's origin
// when one answers, else the path key — through the world's hub. No hub is no
// link.
func hubLink(ctx context.Context, w World, boardRoot string) (string, bool) {
	if w.HubBoard == nil {
		return "", false
	}
	key := board.RepoKey(board.OriginOf(ctx, board.RepoRootOf(boardRoot)), server.KeyOf(boardRoot))
	return w.HubBoard(ctx, key)
}

// hookCounts answers the three counts the hook line names: every ticket
// outside done, then the in-progress and review columns inside that count.
func hookCounts(st board.Status) (open, inProgress, inReview int) {
	for _, c := range st.Counts {
		if c.Column != "done" {
			open += c.Count
		}
		switch c.Column {
		case "in-progress":
			inProgress = c.Count
		case "review":
			inReview = c.Count
		}
	}
	return open, inProgress, inReview
}

// hookOnPath puts the plugin's bin on the PATH of this session's Bash commands
// (and its subagents'), once per session: Claude Code points CLAUDE_ENV_FILE
// at the file it sources for them and sets CLAUDE_PLUGIN_ROOT to the plugin it
// runs this hook from.
func hookOnPath(w World) {
	file, hasFile := w.Getenv("CLAUDE_ENV_FILE")
	root, hasRoot := w.Getenv("CLAUDE_PLUGIN_ROOT")
	if !hasFile || file == "" || !hasRoot || root == "" {
		return
	}
	line := `export PATH="` + filepath.Join(root, "bin") + `:$PATH"`
	if raw, err := os.ReadFile(file); err == nil && strings.Contains(string(raw), line) {
		return
	}
	envFile, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer envFile.Close()
	_, _ = envFile.WriteString(line + "\n")
}
