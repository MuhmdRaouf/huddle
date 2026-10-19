// The PreToolUse part for the editing tools: before this session changes a file, the channel
// says who reserved it. A path another live session holds (or a directory reservation above it)
// prints one line of context naming the holder and until when — never a deny, never a decision:
// Claude Code goes on exactly as before. Silent when the tool does not edit, the session is not
// in a channel, nobody holds the path, or anything fails (quiet.go), and inside the deadline the
// hook plan grants this part.
package hooks

import (
	"context"
	"io"

	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
)

// The reserve part's own deadlines, in milliseconds; their sum sits inside the 300 ms the hook
// plan grants this part. The stdin read ends at once (the event process already read it), the
// origin lookup bounds its one git call, and the fetch waits only for what is left.
const (
	reserveStdinMS  = 50
	reserveOriginMS = 90
	reserveFetchMS  = 150
)

// Reserve warns a session about to edit a path another live session in its channel reserved.
func Reserve(ctx context.Context, w *cli.World, in io.Reader, out io.Writer) {
	Run(ctx, w, "reserve", func() error {
		var input hookInput
		readInput(in, reserveStdinMS, &input)
		if !cli.EditTool(input.ToolName) {
			return nil
		}
		raw := input.ToolInput.FilePath
		if raw == "" {
			raw = input.ToolInput.NotebookPath
		}
		if raw == "" {
			return nil
		}
		id := cli.Identify(ctx, w, input.SessionID)
		if id.Channel == "" || id.As == "" || id.URL == "" {
			return nil // not in a huddle: silent
		}
		target := cli.TargetOf(ctx, w, raw, input.Cwd, reserveOriginMS)
		warn := cli.ReserveWarn(ctx, w, id, target, reserveFetchMS)
		if warn == "" {
			return nil
		}
		event := input.HookEventName
		if event == "" {
			event = "PreToolUse" // the event this hook serves, when the input named none
		}
		writeContext(out, event, warn)
		return nil
	})
}
