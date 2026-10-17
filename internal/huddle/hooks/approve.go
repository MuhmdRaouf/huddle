// The PreToolUse hook for Bash calls: the channel's approval rules (the channel package; the
// owner switches them in the dashboard's Settings). A command that matches no rule ends here,
// before any network or file read: silent, the normal permission flow goes on. One that could
// match asks the server, which knows which rules this channel has on; on a match it records an
// approval.request event (the owner's Inbox and a desktop notification) and this hook answers
// Claude Code's native permissionDecision "ask" with a short reason: Claude Code shows its own
// permission prompt, and the user decides there. It never denies and never waits. Silent when
// the session is not in a huddle (no channel, no credential: 401), when Huddle does not answer
// within its deadline, or when anything fails (quiet.go). The command is sent to the local
// server only, never stored, logged or shown.
package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// defaultReason is what the permission prompt says when the server gave none.
const defaultReason = "Huddle: the owner asked to approve this command"

// approveOutput is the one answer this hook gives: Claude Code's native ask.
type approveOutput struct {
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
}

// Approve decides nothing on its own: it turns the server's ask into Claude Code's native
// permission prompt, and every other outcome into silence.
func Approve(ctx context.Context, w *cli.World, in io.Reader, out io.Writer) {
	Run(ctx, w, "approve", func() error {
		var input hookInput
		readInput(in, approveStdinMS, &input)
		if input.ToolName != "" && input.ToolName != "Bash" {
			return nil
		}
		command := input.ToolInput.Command
		if len(channel.Matching(command)) == 0 {
			return nil // no rule could apply: nothing to ask anyone
		}
		id := cli.Identify(ctx, w, input.SessionID)
		if id.Channel == "" || id.As == "" || id.URL == "" {
			return nil // not in a huddle: silent
		}
		body, err := domain.MarshalCompactJSON(struct {
			Command       string `json:"command"`
			ClaudeSession string `json:"claude_session"`
		}{command, input.SessionID})
		if err != nil {
			return err
		}
		callURL := fmt.Sprintf("%s/api/c/%s/x/approval?as=%s", id.URL, url.PathEscape(id.Channel), url.PathEscape(id.As))
		resp, err := cli.HFetch(ctx, w, http.MethodPost, callURL,
			map[string]string{"content-type": "application/json"}, body,
			time.Duration(approveFetchMS)*time.Millisecond, input.SessionID)
		if err != nil {
			return err // the whole hook stays under a second
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil // 401 (not joined), 404 (no such session or channel), anything else: silent
		}
		var answer struct {
			Ask    bool    `json:"ask"`
			Reason *string `json:"reason"` // nil when the server gave none; an empty string stays empty
		}
		if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
			return err
		}
		if !answer.Ask {
			return nil
		}
		reason := defaultReason
		if answer.Reason != nil {
			reason = *answer.Reason
		}
		var output approveOutput
		output.HookSpecificOutput.HookEventName = "PreToolUse"
		output.HookSpecificOutput.PermissionDecision = "ask"
		output.HookSpecificOutput.PermissionDecisionReason = jsfmt.UTF16Slice(reason, 0, 300)
		writeJSON(out, output)
		return nil
	})
}
