package radartab

// The subagents' runs, read through the tab's own API for the insights route.

import (
	"cmp"
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"time"
)

// SubagentRun is one subagent run, as the tab's own history pairs it: a start, and its stop
// once one sits in the same page.
type SubagentRun struct {
	Session string
	Agent   string // the agent id radar keyed the run under
	Name    string // the start's label, the agent's own name; "" when none was recorded
	Started time.Time
	Stopped time.Time // zero while no stop sits in the page
}

// SubagentsSince reads the subagent runs that started or stopped since the given moment through
// the tab's own API, in process, oldest start first. The limit is radar's own largest, so the
// whole window answers; an event of another kind, or one with no session or no agent, says
// nothing about a run and stays out, a stop whose start is not in the page stays out with it,
// and an unreadable answer answers none.
func (t *Tab) SubagentsSince(ctx context.Context, session string, since time.Time) []SubagentRun {
	values := url.Values{
		"since": {strconv.FormatInt(since.UnixMilli(), 10)},
		"limit": {"1000"},
	}
	if session != "" {
		values.Set("session", session)
	}
	body, ok := t.RadarAnswer(ctx, "/api/events?"+values.Encode())
	if !ok {
		return nil
	}
	var answer struct {
		Events []struct {
			Ts        int64   `json:"ts"`
			Kind      string  `json:"kind"`
			SessionID *string `json:"sessionId"`
			AgentID   *string `json:"agentId"`
			Label     *string `json:"label"`
		} `json:"events"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil
	}
	type hit struct {
		kind           string
		session, agent string
		label          string
		ts             int64
	}
	hits := make([]hit, 0, len(answer.Events))
	for _, event := range answer.Events {
		if event.Kind != "SubagentStart" && event.Kind != "SubagentStop" {
			continue
		}
		if event.SessionID == nil || *event.SessionID == "" || event.AgentID == nil || *event.AgentID == "" {
			continue
		}
		var label string
		if event.Label != nil {
			label = *event.Label
		}
		hits = append(hits, hit{kind: event.Kind, session: *event.SessionID, agent: *event.AgentID, label: label, ts: event.Ts})
	}
	// the API answers newest first; pairing reads oldest first
	slices.SortStableFunc(hits, func(a, b hit) int { return cmp.Compare(a.ts, b.ts) })
	runs := make([]SubagentRun, 0, len(hits))
	for _, h := range hits {
		if h.kind == "SubagentStart" {
			runs = append(runs, SubagentRun{Session: h.session, Agent: h.agent, Name: h.label, Started: time.UnixMilli(h.ts)})
			continue
		}
		for i := len(runs) - 1; i >= 0; i-- { // a stop pairs with the newest start still waiting
			if runs[i].Session == h.session && runs[i].Agent == h.agent && runs[i].Stopped.IsZero() {
				runs[i].Stopped = time.UnixMilli(h.ts)
				break
			}
		}
	}
	slices.SortStableFunc(runs, func(a, b SubagentRun) int { return a.Started.Compare(b.Started) })
	return runs
}
