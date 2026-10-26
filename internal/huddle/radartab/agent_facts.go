package radartab

import (
	"context"
	"encoding/json"
	"net/url"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// AgentFacts is what radar knows of one tray subagent: its output tokens, when it last made a
// call, and the one-line what of that call. LastAt is zero when radar holds no time for the
// agent, and What is empty when its newest call has no what.
type AgentFacts struct {
	OutputTokens int64
	LastAt       time.Time
	What         string
}

// AgentFactsOf reads one session's subagent facts through the tab's own API, in-process — the
// same answer the dashboard reads — keyed by the bare agent ids a session's tray names. Radar
// names a subagent agent-<id> when it learned it from the subagent's transcript file and <id>
// when it learned it from a hook or the router; both spellings name one agent, so the rows are
// joined by bare id, the row with more requests winning (ties: the first in the answer). A
// failed or undecodable session read answers an empty map; a failed requests read leaves that
// agent's What empty.
func (t *Tab) AgentFactsOf(ctx context.Context, session string) map[string]AgentFacts {
	facts := map[string]AgentFacts{}
	body, ok := t.RadarAnswer(ctx, "/api/sessions/"+url.PathEscape(session))
	if !ok {
		return facts
	}
	var answer struct {
		Session *struct {
			Agents []struct {
				ID       string           `json:"id"`
				Kind     shared.AgentKind `json:"kind"`
				Requests int64            `json:"requests"`
				Tokens   struct {
					Output int64 `json:"output"`
				} `json:"tokens"`
				LastAt *int64 `json:"lastAt"`
			} `json:"agents"`
		} `json:"session"`
	}
	if err := json.Unmarshal(body, &answer); err != nil || answer.Session == nil {
		return facts
	}
	type row struct {
		id       string
		requests int64
		facts    AgentFacts
	}
	rows := map[string]*row{}
	var order []string
	for _, agent := range answer.Session.Agents {
		if agent.Kind != shared.KindSubagent {
			continue
		}
		bare := store.BareAgent(agent.ID)
		if held, seen := rows[bare]; seen && held.requests >= agent.Requests {
			continue
		}
		fact := AgentFacts{OutputTokens: agent.Tokens.Output}
		if agent.LastAt != nil {
			fact.LastAt = time.UnixMilli(*agent.LastAt)
		}
		if _, seen := rows[bare]; !seen {
			order = append(order, bare)
		}
		rows[bare] = &row{id: agent.ID, requests: agent.Requests, facts: fact}
	}
	for _, bare := range order {
		held := rows[bare]
		if body, ok := t.RadarAnswer(ctx, "/api/requests?"+url.Values{
			"session": {session},
			"agent":   {held.id},
			"limit":   {"1"},
		}.Encode()); ok {
			var newest struct {
				Requests []struct {
					What *string `json:"what"`
				} `json:"requests"`
			}
			if err := json.Unmarshal(body, &newest); err == nil &&
				len(newest.Requests) > 0 && newest.Requests[0].What != nil {
				held.facts.What = *newest.Requests[0].What
			}
		}
		facts[bare] = held.facts
	}
	return facts
}
