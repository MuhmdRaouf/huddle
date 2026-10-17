// The PostToolUse and UserPromptSubmit hook: bring the new messages of this session's channels
// into its context (the cli package's feed), so every session knows what the others said, also
// to each other, without the channels preview. On a prompt it also takes what the owner sent the
// session while it worked (the cli package's messages take, SPEC 6.4's next-prompt route).
// Silent when nothing is new, no channel is set, the session holds no credential for the server
// (it has not joined), Huddle does not answer, or anything fails (quiet.go). A subagent's tool
// calls (agent_id set) are skipped: the feed's cursor is the session's, and a subagent reading
// it would take the messages away from its parent (subagents read the channel through recall and
// their brief). An edit (Edit, Write, MultiEdit, NotebookEdit: the cli package's touch) is
// reported as its repo-relative path, also from a subagent (as its session's); when another live
// session edited the same file lately, one line says so.
package hooks

import (
	"context"
	"io"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
)

// Listen brings the channel's new events, and this session's own edit, into its context.
func Listen(ctx context.Context, w *cli.World, in io.Reader, out io.Writer) {
	Run(ctx, w, "listen", func() error {
		var input hookInput
		readInput(in, listenStdinMS, &input)
		var touchInput cli.TouchInput
		touchInput.ToolName = input.ToolName
		touchInput.ToolInput.FilePath = input.ToolInput.FilePath
		touchInput.ToolInput.NotebookPath = input.ToolInput.NotebookPath
		touchInput.Cwd = input.Cwd
		touch := cli.TouchOf(touchInput)
		if input.AgentID != "" && touch == nil {
			return nil // a subagent's tool call: the parent's feed is not its to take
		}
		id := cli.Identify(ctx, w, input.SessionID)
		if id.Channel == "" || id.As == "" {
			return nil
		}
		warn := ""
		if touch != nil {
			warn = cli.ReportTouch(ctx, w, id, *touch, listenTouchMS)
		}
		if input.AgentID != "" { // a subagent's edits are its session's; the feed is not
			if warn != "" {
				writeContext(out, input.HookEventName, warn)
			}
			return nil
		}
		feedText := cli.Feed(ctx, w, id, cli.Home(ctx, w), input.SessionID, cli.FeedOptions{})
		parts := make([]string, 0, 3)
		if input.HookEventName == "UserPromptSubmit" {
			// the owner's answer rides this prompt (SPEC 6.4): it goes first, ahead of the feed
			parts = append(parts, cli.TakeMessages(ctx, w, id, input.SessionID, listenFeedMS)...)
		}
		for _, part := range []string{warn, feedText} {
			if part != "" {
				parts = append(parts, part)
			}
		}
		if len(parts) > 0 {
			writeContext(out, input.HookEventName, strings.Join(parts, "\n\n"))
		}
		return nil
	})
}
