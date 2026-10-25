package server

// The insights route: today's tips over the history the registry already holds, the owner's
// credential only. Tool runs and lane requests live in radar and are not read here, so the
// always_allowed and longest_wait rules are the ones that can fire.

import (
	"net/http"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/insights"
)

// insightsAnswer is the route's whole body.
type insightsAnswer struct {
	Tips []tipView `json:"tips"`
}

// tipView is one tip as the dashboard reads it.
type tipView struct {
	ID       string `json:"id"`
	Rule     string `json:"rule"`
	Session  string `json:"session,omitempty"`
	Sentence string `json:"sentence"`
	Count    int    `json:"count"`
	WaitMs   int64  `json:"wait_ms,omitempty"`
}

// insightsRoute answers GET /api/insights with the tips the last 7 days of needs-you history
// supports, read-only over the registry's own rows.
func (s *server) insightsRoute(w http.ResponseWriter, r *http.Request) error {
	if s.registry == nil {
		return s.needsUnavailable()
	}
	now := s.now()
	records, err := s.registry.ItemHistory(r.Context(), now.Add(-7*24*time.Hour))
	if err != nil {
		return err
	}
	in := insights.Input{Now: now}
	for _, record := range records {
		in.Items = append(in.Items, insights.Item{
			Session:   record.SessionID,
			Kind:      record.Kind,
			CreatedAt: record.CreatedAt,
			DecidedAt: record.DecidedAt,
		})
		if record.Kind != hub.KindPermission {
			continue
		}
		in.Asks = append(in.Asks, insights.PermissionAsk{
			Session:   record.SessionID,
			Tool:      record.Tool,
			Command:   record.Command,
			Decision:  record.Decision,
			CreatedAt: record.CreatedAt,
			DecidedAt: record.DecidedAt,
		})
	}
	tips := insights.Detect(in)
	views := make([]tipView, 0, len(tips))
	for _, tip := range tips {
		views = append(views, tipView{
			ID:       tip.ID,
			Rule:     tip.Rule,
			Session:  tip.Session,
			Sentence: tip.Sentence,
			Count:    tip.Count,
			WaitMs:   tip.Wait.Milliseconds(),
		})
	}
	s.writeJSON(w, 200, insightsAnswer{Tips: views}, nil)
	return nil
}
