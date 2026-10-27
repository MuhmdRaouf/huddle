// The PermissionRequest hook: one tool call the terminal is being asked about
// becomes a needs-you item the owner's app may allow or deny, while the hook holds a long poll
// for the decision. Whichever side answers first wins: the app's decision prints as Claude
// Code's own PermissionRequest answer, and a terminal answer withdraws the item, so the poll
// returns with no decision and Claude Code keeps the terminal's. When nothing arrives — no
// app, no decision, the budget out — the hook prints nothing and the terminal prompt stands.
// Silent on every failure (quiet.go): the normal permission flow is the fallback.
package hooks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// The permit hook's own deadlines, in milliseconds. The budget sits under seventy percent of
// the 600 s hooks.json grants the event, the last poll's hold and its client slack still
// inside: a hook is dead to Claude Code at its timeout, and its answer must land well before.
const (
	permitStdinMS  = 500
	permitPostMS   = 4000
	permitWaitMS   = 30000
	permitSlackMS  = 5000
	permitRetryMS  = 1000
	permitBudgetMS = 375000
)

// permitDenyMessage is what Claude reads when the owner denied without saying why.
const permitDenyMessage = "Huddle: the owner denied this permission"

// permitInput is what Claude Code sends the hook on stdin; every field is optional.
type permitInput struct {
	SessionID string          `json:"session_id"`
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

// permitDecision is the decision object Claude Code's PermissionRequest hook answers with.
type permitDecision struct {
	Behavior     string          `json:"behavior"`               // allow or deny
	Message      string          `json:"message,omitempty"`      // deny only: why, for Claude
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"` // allow only: an answered AskUserQuestion's input with the owner's answers set
}

// permitOutput is the one answer this hook gives: the documented hookSpecificOutput shape. An
// allow carries updatedInput only for an answered AskUserQuestion, and never
// updatedPermissions — Huddle decides this one prompt and never writes a permission rule.
type permitOutput struct {
	HookSpecificOutput struct {
		HookEventName string         `json:"hookEventName"`
		Decision      permitDecision `json:"decision"`
	} `json:"hookSpecificOutput"`
}

// Permit turns one permission prompt into a needs-you item and waits for its answer.
func Permit(ctx context.Context, w *cli.World, in io.Reader, out io.Writer) {
	Run(ctx, w, "permit", func() error {
		var input permitInput
		readInput(in, permitStdinMS, &input)
		if input.SessionID == "" || input.ToolName == "" || len(input.ToolInput) == 0 {
			return nil // nothing to ask about: the terminal prompt stands
		}
		id := cli.Identify(ctx, w, input.SessionID)
		if id.URL == "" {
			return nil // no huddle to ask: the terminal prompt stands
		}
		body, err := domain.MarshalCompactJSON(struct {
			SessionID string          `json:"session_id"`
			ToolName  string          `json:"tool_name"`
			ToolInput json.RawMessage `json:"tool_input"`
		}{input.SessionID, input.ToolName, input.ToolInput})
		if err != nil {
			return err
		}
		resp, err := cli.HFetch(ctx, w, http.MethodPost, id.URL+"/api/needs",
			map[string]string{"content-type": "application/json"}, body,
			time.Duration(permitPostMS)*time.Millisecond, input.SessionID)
		if err != nil {
			return err // a huddle that never answers costs the POST's deadline and no more
		}
		raw, err := io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil // not in this huddle (401), a torn post, anything: the terminal stands
		}
		var created struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(raw, &created); err != nil {
			return err
		}
		if created.ID == 0 {
			return nil // nothing to poll
		}
		deadline := nowOf(w).Add(permitBudget(w))
		for {
			left := deadline.Sub(nowOf(w))
			if left <= 0 {
				return nil // the budget is out: the terminal prompt stands
			}
			secs := int(left.Seconds()) + 1
			if secs > permitWaitMS/1000 {
				secs = permitWaitMS / 1000
			}
			callURL := id.URL + "/api/needs/" + strconv.FormatInt(created.ID, 10) + "/wait?timeout=" + strconv.Itoa(secs)
			resp, err := cli.HFetch(ctx, w, http.MethodGet, callURL, nil, nil,
				time.Duration(secs*1000+permitSlackMS)*time.Millisecond, input.SessionID)
			if err != nil {
				// a refused or cut poll is a hub restarting: wait and poll again
				if err := permitPause(ctx, w, deadline); err != nil {
					return err
				}
				continue
			}
			raw, err := io.ReadAll(resp.Body)
			closeErr := resp.Body.Close()
			if err != nil || closeErr != nil || resp.StatusCode >= 500 {
				// a body cut short or a hub still starting: wait and poll again
				if err := permitPause(ctx, w, deadline); err != nil {
					return err
				}
				continue
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return nil // the item is gone (404), not ours (401), anything: the terminal stands
			}
			var row hub.PermissionItem
			if err := json.Unmarshal(raw, &row); err != nil {
				return err
			}
			switch {
			case row.State == hub.StateAnswered && row.Decision == "allow":
				updated, ok := permitUpdatedInput(input, row.Answers)
				if !ok {
					return nil // no complete updatedInput for a question: the terminal dialog stands
				}
				var answer permitOutput
				answer.HookSpecificOutput.HookEventName = "PermissionRequest"
				answer.HookSpecificOutput.Decision.Behavior = "allow"
				answer.HookSpecificOutput.Decision.UpdatedInput = updated
				writeJSON(out, answer)
				return nil
			case row.State == hub.StateAnswered && row.Decision == "deny":
				message := jsscalar.UTF16Slice(row.Message, 0, 300)
				if message == "" {
					message = permitDenyMessage
				}
				var answer permitOutput
				answer.HookSpecificOutput.HookEventName = "PermissionRequest"
				answer.HookSpecificOutput.Decision.Behavior = "deny"
				answer.HookSpecificOutput.Decision.Message = message
				writeJSON(out, answer)
				return nil
			case row.State == hub.StateOpen:
				// the wait timed out with the item still waiting: poll again
			default:
				// withdrawn (the terminal answered first), expired, snoozed, dismissed: no
				// decision of ours to print
				return nil
			}
		}
	})
}

// permitUpdatedInput answers what an allow prints as the decision's updatedInput, and whether
// there is an allow to print at all. Only an AskUserQuestion carries one: Claude Code runs a
// question without its dialog only when the allow's updatedInput holds the owner's answers, so
// the hook echoes the call's own stdin input — never the hub's stored copy, which may be cut —
// with answers set to the item's picks, replacing any answers the input already had. An ask
// with no answers stored, or whose input is no JSON object, has no complete one to give:
// (nil, false) prints nothing and the terminal dialog stands, never a bare allow. Every other
// tool answers (nil, true): its allow stays bare.
func permitUpdatedInput(input permitInput, answers map[string]string) (json.RawMessage, bool) {
	if input.ToolName != "AskUserQuestion" {
		return nil, true
	}
	if len(answers) == 0 {
		return nil, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input.ToolInput, &fields); err != nil || fields == nil {
		return nil, false
	}
	picked, err := domain.MarshalCompactJSON(answers)
	if err != nil {
		return nil, false
	}
	fields["answers"] = picked
	updated, err := domain.MarshalCompactJSON(fields)
	if err != nil {
		return nil, false
	}
	return updated, true
}

// permitPause holds the hook between a failed poll and the next. The retry shape is a fixed
// 1 s pause after every refused, cut or 5xx poll, cut short by the budget's deadline, so a hub
// that restarts mid-ask is polled again within a second of coming back and a hub that never
// returns costs the budget and no more. It answers the context's error when that ends first.
func permitPause(ctx context.Context, w *cli.World, deadline time.Time) error {
	pause := time.Duration(permitRetryMS) * time.Millisecond
	if left := deadline.Sub(nowOf(w)); left < pause {
		pause = left
	}
	if pause <= 0 {
		return nil
	}
	timer := time.NewTimer(pause)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// permitBudget answers the hook's whole budget: HUDDLE_PERMIT_BUDGET_MS over the default, a
// value that is no number answering the default rather than an endless wait.
func permitBudget(w *cli.World) time.Duration {
	raw := jsscalar.Trim(getenvOf(w, "HUDDLE_PERMIT_BUDGET_MS"))
	if raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return time.Duration(permitBudgetMS) * time.Millisecond
}
