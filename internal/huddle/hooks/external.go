// The external hook word: another agent's lifecycle hook runs `huddle hook external
// --harness <name> --event <event>` with its own payload on stdin, and this maps the payload
// onto the report the registry takes — the session, its cwd, the event under the report name
// the hub's states read, and the tool a tool event names — tagging the session with its
// harness as it goes. The delivery is the report's own: the outbox replays first and what
// cannot land queues behind what is already there. No pid rides the report (the registry's
// pid check ends sessions whose claude process is gone, and this process is not one), and no
// transcript is read (the agents' transcripts are not Claude Code's). Whatever fails, the
// word answers in silence (quiet.go): a hook never blocks the agent that ran it.
package hooks

import (
	"context"
	"io"

	"github.com/MuhmdRaouf/huddle/internal/harness"
	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// externalInput is what an agent's hook sends on stdin; every field is optional. The field
// names are the ones every supported agent spells (the discovery file cites them), so one
// read covers all of them and a forwarded Claude Code payload besides.
type externalInput struct {
	SessionID     string `json:"session_id"`
	HookEventName string `json:"hook_event_name"`
	Cwd           string `json:"cwd"`
	ToolName      string `json:"tool_name"`
	Prompt        string `json:"prompt"`
}

// External reports one event of another agent's session to the local huddle's registry. An
// unknown harness, an event the harness does not report, and a payload without a session or
// an event all say nothing.
func External(ctx context.Context, w *cli.World, name, event string, in io.Reader, out io.Writer) {
	Run(ctx, w, "external", func() error {
		var payload externalInput
		readInput(in, reportStdinMS, &payload)
		if event == "" {
			event = payload.HookEventName
		}
		if payload.SessionID == "" || event == "" {
			return nil // nothing to report: no session, no event
		}
		report := reportEventOf(name, event)
		if report == "" {
			return nil
		}
		id := cli.Identify(ctx, w, payload.SessionID)
		rep := hub.Report{
			SessionID:   payload.SessionID,
			Channel:     id.Channel,
			Member:      id.As,
			Cwd:         payload.Cwd,
			Harness:     name,
			LastEvent:   report,
			LastEventAt: jsfmt.ISOTime(nowOf(w)),
		}
		rep.Machine = machineOf(ctx, w)
		rep.Project, rep.Branch = checkoutOf(payload.Cwd)
		switch {
		case payload.Prompt != "":
			// the title is the first prompt cut to 80 runes and the task its first line, the
			// report hook's own rule
			rep.Title = jsfmt.UTF16Slice(payload.Prompt, 0, 80)
			rep.Task = jsfmt.UTF16Slice(firstLineOf(payload.Prompt), 0, 80)
		case payload.ToolName != "" && (report == "PreToolUse" || report == "PostToolUse"):
			// a tool event's word is what the session is doing right now: the row's task
			// line says so until the next prompt replaces it
			rep.Task = jsfmt.UTF16Slice(payload.ToolName, 0, 80)
		}
		t0 := nowOf(w)
		left := func() int {
			return max(1, reportBudgetMS-int(nowOf(w).Sub(t0).Milliseconds()))
		}
		return deliverReport(ctx, w, id, rep, left)
	})
}

// reportEventOf maps one agent's own event onto the report name the hub's states read, ""
// when the harness is none of huddle's or reports no such event.
func reportEventOf(name, event string) string {
	events, err := harness.HookEvents(name)
	if err != nil {
		return ""
	}
	for _, one := range events {
		if one.Event == event {
			return one.Report
		}
	}
	return ""
}
