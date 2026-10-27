package server

// The insights route: today's tips over the history the registry already holds, the owner's
// credential only. The owner's card (no session) reads the needs-you history, the lanes'
// requests from the mounted radar tab and the registry's own session spans, so the
// always_allowed, longest_wait, lane_failing and long_day rules are the ones that can fire
// there; a session's answer adds that session's tool runs from the mounted radar tab, and
// same_command is the rule they add: tool_failing needs the error text radar does not record,
// and a lane's health and a day's length are no one session's, so the session's answer leaves
// all three out. Both answers add the subagents' recent starts and stops from the mounted
// radar tab, so subagent_silent can fire on either.

import (
	"net/http"
	"regexp"
	"slices"
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
// supports, read-only over the registry's own rows. The owner's answer adds the lanes' requests
// of the last 24 h from the mounted radar tab, so a lane that keeps failing names itself there,
// and the registry's session spans, so a day of long sessions does too. Both answers add the
// subagents' starts and stops of the last 2 h from the mounted radar tab, so a run with no
// stop event names itself to whoever reads.
// A session query narrows the answer to that session: its rows alone go in, its tool runs come
// in from the mounted radar tab and add same_command alone (tool_failing needs the error text
// radar does not record), and its tips alone come out. Tips for me gates the owner's answer,
// tips to my sessions the session's; the switch that is off empties the answer before anything
// else is read. The owner's answer refreshes the spinner tips file the spinnerTipsOverride
// entry names — the panel polls it, so the answer is the refresh point — and a session's
// answer leaves the file alone.
func (s *server) insightsRoute(w http.ResponseWriter, r *http.Request) error {
	session := r.URL.Query().Get("session")
	settings := insights.ReadSettings(insights.SettingsFile(s.o.Home))
	if session != "" {
		if !settings.TipsToSessions {
			s.writeJSON(w, 200, insightsAnswer{Tips: []tipView{}}, nil)
			return nil
		}
	} else if !settings.TipsForMe {
		// the file empties with the answer, so Claude Code stops showing the tips already there
		s.writeSpinnerTips(insights.SpinnerTipsOf(nil, settings))
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
		if session != "" && record.SessionID != session {
			continue
		}
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
	if session != "" && s.radar != nil {
		for _, run := range s.radar.ToolRunsOf(r.Context(), session) {
			in.Tools = append(in.Tools, insights.ToolRun{
				Session:  session,
				Tool:     run.Name,
				InputKey: run.InputKey,
				OK:       run.OK,
				At:       run.At,
			})
		}
	}
	// the owner's answer adds the lanes' own requests and the sessions' spans; a lane's health
	// and a day's length are no one session's
	if session == "" && s.radar != nil {
		for _, request := range s.radar.RequestsSince(r.Context(), now.Add(-24*time.Hour)) {
			in.Requests = append(in.Requests, insights.Request{
				Provider: request.Provider,
				Status:   request.Status,
				At:       request.At,
			})
		}
	}
	if session == "" {
		spans, err := s.registry.SessionSpans(r.Context())
		if err != nil {
			return err
		}
		for _, span := range spans {
			in.Spans = append(in.Spans, insights.SessionSpan{Session: span.SessionID, Start: span.Start, End: span.End})
		}
	}
	// both answers add the subagents' runs of the last 2 h: a run gone silent is its session's
	// fact and the huddle's
	if s.radar != nil {
		for _, run := range s.radar.SubagentsSince(r.Context(), session, now.Add(-2*time.Hour)) {
			in.Subagents = append(in.Subagents, insights.SubagentRun{
				Session: run.Session,
				Agent:   run.Agent,
				Name:    run.Name,
				Started: run.Started,
				Stopped: run.Stopped,
			})
		}
	}
	tips := insights.Detect(in)
	// a rule the owner muted leaves the answer, the owner's and every session's, until its 30
	// days run out
	tips = insights.WithoutMuted(tips, insights.MutedRules(insights.SettingsFile(s.o.Home), now))
	if session == "" {
		s.writeSpinnerTips(insights.SpinnerTipsOf(tips, settings))
	}
	views := make([]tipView, 0, len(tips))
	for _, tip := range tips {
		if session != "" && tip.Session != session {
			continue
		}
		// tool_failing reads the error text the radar tab's tool runs do not carry, so the
		// session's answer leaves its tips out rather than name a failure it cannot see
		if session != "" && tip.Rule == insights.RuleToolFailing {
			continue
		}
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

// writeSpinnerTips lands the owner's spinner entries in the home the server was started with,
// the file the spinnerTipsOverride entry names. It is best-effort: without a home, or when the
// write fails, the answer stands and nothing is said.
func (s *server) writeSpinnerTips(tips []insights.SpinnerTip) {
	userHome := s.o.Env["HOME"]
	if userHome == "" {
		return
	}
	_ = insights.WriteSpinnerTips(insights.SpinnerTipsFile(userHome), tips)
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

// muteRuleRe names the rule a mute takes: an Insights rule name, nothing else.
var muteRuleRe = regexp.MustCompile(`^[a-z_]{1,64}$`)

// insightsMuteAnswer is the mute route's whole body.
type insightsMuteAnswer struct {
	Muted []string `json:"muted"`
}

// insightsMuteRoute answers POST /api/insights/mute: the owner mutes one rule's tips for 30
// days. The body names the rule; the answer names every rule muted now, sorted.
func (s *server) insightsMuteRoute(w http.ResponseWriter, r *http.Request) error {
	body, err := s.body(r)
	if err != nil {
		return err
	}
	rule := fieldOf(body, "rule")
	if rule == nil || rule.Kind() != jsfmt.KindString || !muteRuleRe.MatchString(rule.Text()) {
		return &channel.HuddleError{Status: 400, Msg: "rule: the rule to mute, like same_command"}
	}
	path := insights.SettingsFile(s.o.Home)
	if err := insights.Mute(path, rule.Text(), s.now()); err != nil {
		return err
	}
	muted := insights.MutedRules(path, s.now())
	rules := make([]string, 0, len(muted))
	for name := range muted {
		rules = append(rules, name)
	}
	slices.Sort(rules)
	s.writeJSON(w, 200, insightsMuteAnswer{Muted: rules}, nil)
	return nil
}

// insightsForgetRoute answers POST /api/insights/forget: it deletes insights.json, the only
// learning Insights keeps, and the switches answer their defaults again.
func (s *server) insightsForgetRoute(w http.ResponseWriter, r *http.Request) error {
	if err := insights.Forget(insights.SettingsFile(s.o.Home)); err != nil {
		return err
	}
	s.writeJSON(w, 200, insightsSettingsAnswer{TipsForMe: true, TipsToSessions: false}, nil)
	return nil
}
