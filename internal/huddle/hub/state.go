// The registry's state rules (SPEC 5.2): a session's state is computed when it is read, never
// stored. The stored rows carry the inputs — the events, and the two facts that end a session —
// and every read derives one of the five states from them.
package hub

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
)

// The five states, in the spec's order.
const (
	StateNeedsYou = "needs-you"
	StateWorking  = "working"
	StateIdle     = "idle"
	StateStuck    = "stuck"
	StateEnded    = "ended"
)

// states lists the five states; a filter's state must be one of them.
var states = []string{StateNeedsYou, StateWorking, StateIdle, StateStuck, StateEnded}

// stuckAfter is the quiet past which a session that never said it stopped may be stuck: the
// spec's 10-minute limit. A tool run that fires no event for minutes is normal work; ten quiet
// minutes is not.
const stuckAfter = 10 * time.Minute

// Facts is what one session's state is computed from. Asking says the last event itself waits
// on the owner: a Notification that asks (a permission request names itself in LastEvent).
type Facts struct {
	LastEvent   string
	LastEventAt time.Time
	Asking      bool
	OpenItem    bool
	Ended       bool
}

// StateOf answers the state of one session at now. Ended outranks everything: a session whose
// SessionEnd was seen, or whose machine reports its pid gone, is over whatever else it holds.
// Needs-you comes next — an open item, or the last event itself waiting on the owner, a
// permission request or a Notification that asks, with no later prompt or tool event after it —
// then idle, whose Stops never go stuck, then stuck past the quiet limit, and working
// otherwise: a young event of another kind (a session that just started, a notification that
// asks nothing) still reads working, so every live session shows one of the five states.
func StateOf(f Facts, now time.Time) string {
	switch {
	case f.Ended:
		return StateEnded
	case f.OpenItem, f.LastEvent == "PermissionRequest", f.LastEvent == "Notification" && f.Asking:
		return StateNeedsYou
	case f.LastEvent == "Stop" || f.LastEvent == "SubagentStop":
		return StateIdle
	case now.Sub(f.LastEventAt) >= stuckAfter:
		return StateStuck
	default:
		return StateWorking
	}
}

// KnownState says whether s names one of the five states.
func KnownState(s string) bool { return slices.Contains(states, s) }

// StateNames joins the state names, for the message a refused filter reads.
func StateNames() string { return strings.Join(states, ", ") }

// SessionsFilter narrows a registry read; an empty field filters nothing.
type SessionsFilter struct {
	Machine string
	Project string
	State   string
}

// SessionRow is one registry read: the stored session with its state computed at read time.
// The optional fields keep the report's own omitempty shape; cost_usd and provider are read
// from the radar tab when the row is served, and stay null when radar knows nothing of the
// session.
type SessionRow struct {
	SessionID      string  `json:"session_id"`
	Machine        string  `json:"machine,omitempty"`
	Project        string  `json:"project,omitempty"`
	Branch         string  `json:"branch,omitempty"`
	Title          string  `json:"title,omitempty"`
	Task           string  `json:"task,omitempty"`
	Model          string  `json:"model,omitempty"`
	ContextPercent float64 `json:"context_percent,omitempty"`
	Cwd            string  `json:"cwd,omitempty"`
	Pid            int     `json:"pid,omitempty"`
	Channel        string  `json:"channel,omitempty"`
	Member         string  `json:"member,omitempty"`
	// Harness names the coding agent the session runs in when it is not Claude Code.
	Harness     string   `json:"harness,omitempty"`
	LastEvent   string   `json:"last_event"`
	LastEventAt string   `json:"last_event_at"`
	CostUSD     *float64 `json:"cost_usd"`
	Provider    *string  `json:"provider"`
	State       string   `json:"state"`
	// DeliveryRoute says how a message to this session arrives (SPEC 6.4): pushed now while its
	// bridge proves its channel pushes flow, else on its next prompt.
	DeliveryRoute string `json:"delivery_route"`
}

// SessionEvent is one stored report in a session's event list: the row's number and the report
// as it came.
type SessionEvent struct {
	Seq    int64  `json:"seq"`
	Report Report `json:"report"`
}

// SessionDetail is one session's row with the events that led to it.
type SessionDetail struct {
	SessionRow
	Events []SessionEvent `json:"events"`
}

// sessionColumns is what a registry read selects, in the row's own order.
const sessionColumns = `session_id, machine, project, branch, title, task, model, context_percent,
                        cwd, pid, channel, member, harness, last_event, last_event_at, ended_at, opened_at`

// Sessions answers every registered session that passes the filter, newest activity first,
// each with its state computed at now.
func (d *DB) Sessions(ctx context.Context, filter SessionsFilter, now time.Time) ([]SessionRow, error) {
	rows, err := d.st.All(ctx,
		"SELECT "+sessionColumns+" FROM sessions ORDER BY last_event_at DESC, session_id")
	if err != nil {
		return nil, err
	}
	waiting, err := d.OpenItemSessions(ctx, now)
	if err != nil {
		return nil, err
	}
	out := []SessionRow{}
	for _, row := range rows {
		id := textOfRow(row, "session_id")
		lastEvent := textOfRow(row, "last_event")
		s := d.rowOfSession(ctx, row, now, waiting[id], d.asksOf(ctx, id, lastEvent))
		if filter.Machine != "" && s.Machine != filter.Machine {
			continue
		}
		if filter.Project != "" && s.Project != filter.Project {
			continue
		}
		if filter.State != "" && s.State != filter.State {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// SessionRow answers one session's row by id, nil when no such session is registered.
func (d *DB) SessionRow(ctx context.Context, id string, now time.Time) (*SessionRow, error) {
	row, err := d.st.Get(ctx, "SELECT "+sessionColumns+" FROM sessions WHERE session_id = ?", id)
	if err != nil || row == nil {
		return nil, err
	}
	out := d.rowOfSession(ctx, row, now, d.sessionWaits(ctx, id, now),
		d.asksOf(ctx, id, textOfRow(row, "last_event")))
	return &out, nil
}

// Session answers one session with the events that led to it — the newest 200, oldest of them
// first — or nil when no such session is registered.
func (d *DB) Session(ctx context.Context, id string, now time.Time) (*SessionDetail, error) {
	row, err := d.SessionRow(ctx, id, now)
	if err != nil || row == nil {
		return nil, err
	}
	rows, err := d.st.All(ctx,
		"SELECT seq, json FROM events WHERE session_id = ? ORDER BY seq DESC LIMIT 200", id)
	if err != nil {
		return nil, err
	}
	events := make([]SessionEvent, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- { // newest-first rows read back oldest-first
		var rep Report
		if err := json.Unmarshal([]byte(textOfRow(rows[i], "json")), &rep); err != nil {
			return nil, err
		}
		seq, _ := rows[i]["seq"].(int64)
		events = append(events, SessionEvent{Seq: seq, Report: rep})
	}
	return &SessionDetail{SessionRow: *row, Events: events}, nil
}

// PidProbe answers a pid's command line, and whether the pid lives. A probe only looks: the
// decision it feeds never signals the process it read.
type PidProbe func(ctx context.Context, pid int) (string, bool)

// MarkEndedPids ends every not-yet-ended session whose pid no longer runs a claude process:
// the machine's report that the pid is gone (SPEC 5.2). A pid that lives but runs something
// else ends its session too — the claude process it served is gone from under it. It answers
// the session ids it marked, in id order.
func (d *DB) MarkEndedPids(ctx context.Context, at string, probe PidProbe) ([]string, error) {
	rows, err := d.st.All(ctx,
		"SELECT session_id, pid FROM sessions WHERE ended_at IS NULL AND pid > 0 ORDER BY session_id")
	if err != nil {
		return nil, err
	}
	marked := []string{}
	for _, row := range rows {
		pid, _ := row["pid"].(int64)
		command, alive := probe(ctx, int(pid))
		if alive && strings.Contains(command, "claude") {
			continue
		}
		if err := d.st.Run(ctx, "UPDATE sessions SET ended_at = ? WHERE session_id = ?",
			at, row["session_id"]); err != nil {
			return marked, err
		}
		marked = append(marked, textOfRow(row, "session_id"))
	}
	return marked, nil
}

// rowOfSession builds one session row from its stored columns, with the state and the delivery
// route computed at now. waiting says the session holds an open needs-you item; asking says its
// last event itself waits on the owner; the route reads the bridge acks.
func (d *DB) rowOfSession(ctx context.Context, row store.Row, now time.Time, waiting, asking bool) SessionRow {
	out := SessionRow{
		SessionID:   textOfRow(row, "session_id"),
		Machine:     textOfRow(row, "machine"),
		Project:     textOfRow(row, "project"),
		Branch:      textOfRow(row, "branch"),
		Title:       textOfRow(row, "title"),
		Task:        textOfRow(row, "task"),
		Model:       textOfRow(row, "model"),
		Cwd:         textOfRow(row, "cwd"),
		Channel:     textOfRow(row, "channel"),
		Member:      textOfRow(row, "member"),
		Harness:     textOfRow(row, "harness"),
		LastEvent:   textOfRow(row, "last_event"),
		LastEventAt: textOfRow(row, "last_event_at"),
	}
	out.ContextPercent, _ = row["context_percent"].(float64)
	pid, _ := row["pid"].(int64)
	out.Pid = int(pid)
	out.State = StateOf(Facts{
		LastEvent:   out.LastEvent,
		LastEventAt: parseEventTime(out.LastEventAt),
		Asking:      asking,
		OpenItem:    waiting,
		Ended:       textOfRow(row, "ended_at") != "",
	}, now)
	out.DeliveryRoute = d.routeOfMember(ctx, out.Channel, out.Member, now)
	return out
}

// asksOf answers whether the session's newest event itself waits on the owner with nothing
// that records the wait: a permission request, or a Notification that asks and carries no
// words to grow a needs-you item from (SPEC 5.2). An ask whose item exists is the item's own
// rule — open it holds the session in needs-you, and one the owner snoozed, answered or
// dismissed is handled and holds it no more. A Notification this read cannot parse counts as
// not asking, so a torn row never holds a session in needs-you.
func (d *DB) asksOf(ctx context.Context, id, lastEvent string) bool {
	if lastEvent == "PermissionRequest" {
		return true
	}
	if lastEvent != "Notification" {
		return false
	}
	row, err := d.st.Get(ctx,
		"SELECT json FROM events WHERE session_id = ? AND kind = 'Notification' ORDER BY seq DESC LIMIT 1", id)
	if err != nil || row == nil {
		return false
	}
	var rep Report
	if err := json.Unmarshal([]byte(textOfRow(row, "json")), &rep); err != nil {
		return false
	}
	return asksOwner(rep.Notification) && cleanSummary(rep.Notification.Message) == ""
}

// sessionWaits says whether one session holds an open needs-you item at now.
func (d *DB) sessionWaits(ctx context.Context, id string, now time.Time) bool {
	waiting, err := d.OpenItemSessions(ctx, now)
	if err != nil {
		return false
	}
	return waiting[id]
}

// parseEventTime reads one report's timestamp. A value that parses as no time reads as long
// ago, so a malformed timestamp never keeps a quiet session looking young.
func parseEventTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

// textOfRow reads one column as text; NULL and other types read as the empty string.
func textOfRow(row store.Row, col string) string {
	text, _ := row[col].(string)
	return text
}
