package server

// The session live route: one GET naming what runs in the session right now — each running
// subagent with what radar knows of it, and each running background shell of the main and of
// those subagents — for the live tray above the sessions screen's prompt. The transcript is
// the path the session's own reports named; no route ever reads a path from the request.

import (
	"errors"
	"io/fs"
	"net/http"
	"regexp"

	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/convo"
	"github.com/MuhmdRaouf/huddle/internal/huddle/radartab"
)

// sessionLivePath is one session's live tray.
var sessionLivePath = regexp.MustCompile(`^/api/sessions/([^/]+)/live$`)

// liveAnswer is the tray. Both lists are always arrays, empty when nothing runs.
type liveAnswer struct {
	Agents []liveAgent `json:"agents"`
	Shells []liveShell `json:"shells"`
}

// liveAgent is one running subagent of the session, with the radar facts when radar knows the
// agent: its output tokens, when it last made a call, and that call's one-line what. An agent
// radar names no row for holds none of the three keys.
type liveAgent struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	Description  string `json:"description,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	OutputTokens *int64 `json:"output_tokens,omitempty"`
	LastActivity string `json:"last_activity,omitempty"`
	What         string `json:"what,omitempty"`
}

// liveShell is one running background shell and the agent whose transcript ran it.
type liveShell struct {
	Agent       string `json:"agent"`
	ID          string `json:"id"`
	Command     string `json:"command"`
	Description string `json:"description,omitempty"`
	StartedAt   string `json:"started_at"`
}

// liveTray lays out the tray from the session's agents and each transcript's shells: every
// running subagent in the order given, then the running shells of the main and of each of
// them in that same order. A finished subagent and its shells stay out. Facts joins radar's
// rows to the tray by bare agent id — radar names a subagent agent-<id> or <id> for the same
// agent — and an agent whose id facts holds gets its output tokens always and its last
// activity only when radar holds a time. No I/O.
func liveTray(agents []convo.Agent, shells map[string][]convo.Shell, facts map[string]radartab.AgentFacts) liveAnswer {
	answer := liveAnswer{Agents: []liveAgent{}, Shells: []liveShell{}}
	running := make(map[string]bool, len(agents))
	for _, agent := range agents {
		if agent.ID == convo.MainAgent || !agent.Running {
			continue
		}
		running[agent.ID] = true
		entry := liveAgent{
			ID:          agent.ID,
			Type:        agent.Type,
			Description: agent.Description,
			StartedAt:   agent.StartedAt,
		}
		if fact, ok := facts[agent.ID]; ok {
			tokens := fact.OutputTokens
			entry.OutputTokens = &tokens
			if !fact.LastAt.IsZero() {
				entry.LastActivity = fact.LastAt.UTC().Format("2006-01-02T15:04:05.000Z")
			}
			entry.What = fact.What
		}
		answer.Agents = append(answer.Agents, entry)
	}
	for _, agent := range agents {
		if agent.ID != convo.MainAgent && !running[agent.ID] {
			continue
		}
		for _, shell := range shells[agent.ID] {
			if shell.State != "running" {
				continue
			}
			answer.Shells = append(answer.Shells, liveShell{
				Agent:       agent.ID,
				ID:          shell.ID,
				Command:     shell.Command,
				Description: shell.Description,
				StartedAt:   shell.StartedAt,
			})
		}
	}
	return answer
}

// sessionLiveRoute answers the session's live tray. The transcript is the path the session's
// own reports named — nothing a query names is ever read. No transcript, or one the disk no
// longer has, answers an empty tray; any other read error is the answer's error. Radar is
// read only when the tray holds a subagent to attach facts to.
func (s *server) sessionLiveRoute(w http.ResponseWriter, r *http.Request, path string) error {
	if s.registry == nil {
		return s.needsUnavailable()
	}
	id := sessionLivePath.FindStringSubmatch(path)[1]
	ctx := r.Context()
	row, err := s.registry.SessionRow(ctx, id, s.now())
	if err != nil {
		return err
	}
	if row == nil {
		return &channel.HuddleError{Status: 404, Msg: "no such session"}
	}
	transcript, err := s.registry.TranscriptPath(ctx, id)
	if err != nil {
		return err
	}
	agents := []convo.Agent{}
	if transcript != "" {
		agents, err = convo.Agents(ctx, transcript)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err != nil {
			agents = []convo.Agent{}
		}
	}
	shells := map[string][]convo.Shell{}
	for _, agent := range agents {
		if agent.ID != convo.MainAgent && !agent.Running {
			continue
		}
		shellPath := transcript
		if agent.ID != convo.MainAgent {
			found, ok := convo.AgentPath(transcript, agent.ID)
			if !ok {
				continue
			}
			shellPath = found
		}
		read, err := readShells(ctx, shellPath)
		if err != nil {
			return err
		}
		shells[agent.ID] = read
	}
	var facts map[string]radartab.AgentFacts
	if s.radar != nil {
		for _, agent := range agents {
			if agent.ID != convo.MainAgent && agent.Running {
				facts = s.radar.AgentFactsOf(ctx, id)
				break
			}
		}
	}
	s.writeJSON(w, 200, liveTray(agents, shells, facts), nil)
	return nil
}
