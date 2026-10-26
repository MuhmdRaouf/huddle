package server

// The insights route: today's tips over the history the registry already holds, the owner's
// credential only. Tool runs and lane requests live in radar and are not read here, so the
// always_allowed and longest_wait rules are the ones that can fire.

import (
	"net/http"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/insights"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
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

// insightsSettingsAnswer is the switches' route's whole body.
type insightsSettingsAnswer struct {
	TipsForMe      bool `json:"tips_for_me"`
	TipsToSessions bool `json:"tips_to_sessions"`
}

// insightsRoute answers GET /api/insights with the tips the last 7 days of needs-you history
// supports, read-only over the registry's own rows. Tips for me off empties the answer before
// anything else is read.
func (s *server) insightsRoute(w http.ResponseWriter, r *http.Request) error {
	if !insights.ReadSettings(insights.SettingsFile(s.o.Home)).TipsForMe {
		s.writeJSON(w, 200, insightsAnswer{Tips: []tipView{}}, nil)
		return nil
	}
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

// insightsSettingsRoute answers GET and PUT /api/insights/settings: the owner reads the two
// switches or turns either on. A PUT names either or both fields as booleans, the change merges
// over the file's values, and the file keeps every key the switches do not own.
func (s *server) insightsSettingsRoute(w http.ResponseWriter, r *http.Request) error {
	path := insights.SettingsFile(s.o.Home)
	if r.Method == http.MethodGet {
		got := insights.ReadSettings(path)
		s.writeJSON(w, 200, insightsSettingsAnswer{TipsForMe: got.TipsForMe, TipsToSessions: got.TipsToSessions}, nil)
		return nil
	}
	body, err := s.body(r)
	if err != nil {
		return err
	}
	got := insights.ReadSettings(path)
	forMe, forSessions := fieldOf(body, "tips_for_me"), fieldOf(body, "tips_to_sessions")
	if forMe == nil && forSessions == nil {
		return &channel.HuddleError{Status: 400, Msg: "tips_for_me or tips_to_sessions: the switch to record"}
	}
	if forMe != nil && forMe.Kind() != jsfmt.KindBool {
		return &channel.HuddleError{Status: 400, Msg: "tips_for_me: true or false"}
	}
	if forSessions != nil && forSessions.Kind() != jsfmt.KindBool {
		return &channel.HuddleError{Status: 400, Msg: "tips_to_sessions: true or false"}
	}
	if forMe != nil {
		got.TipsForMe = forMe.Boolean()
	}
	if forSessions != nil {
		got.TipsToSessions = forSessions.Boolean()
	}
	if err := insights.WriteSettings(path, got); err != nil {
		return err
	}
	s.writeJSON(w, 200, insightsSettingsAnswer{TipsForMe: got.TipsForMe, TipsToSessions: got.TipsToSessions}, nil)
	return nil
}
