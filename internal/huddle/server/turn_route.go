package server

// The session turn route: one GET naming what the session's transcript says about its current
// turn — when it started, whether it ended, and the permission mode in force — so the sessions
// screen's footer counts the elapsed time itself from the start it is given. A turn that
// started also names the main agent's output tokens since that start, read from radar.

import (
	"errors"
	"io/fs"
	"net/http"
	"regexp"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/convo"
)

// sessionTurnPath is one session's turn facts.
var sessionTurnPath = regexp.MustCompile(`^/api/sessions/([^/]+)/turn$`)

// turnAnswer is one session's turn facts with the turn's output tokens beside them. The
// embedded facts keep their JSON keys; the tokens are absent when the turn never started or
// radar holds no answer.
type turnAnswer struct {
	convo.TurnFacts
	OutputTokens *int64 `json:"output_tokens,omitempty"`
}

// sessionTurnRoute answers the turn facts the session's transcript holds. The transcript is the
// path the session's own reports named — nothing a query names is ever read. No transcript, or
// one the disk no longer has, answers a turn that never started; any other read error is the
// answer's error. A turn that started also carries the main agent's output tokens radar
// recorded since that start; a hub with no radar tab answers the facts alone.
func (s *server) sessionTurnRoute(w http.ResponseWriter, r *http.Request, path string) error {
	if s.registry == nil {
		return s.needsUnavailable()
	}
	id := sessionTurnPath.FindStringSubmatch(path)[1]
	ctx := r.Context()
	row, err := s.registry.SessionRow(ctx, id, s.now())
	if err != nil {
		return err
	}
	if row == nil {
		return &channel.HuddleError{Status: 404, Msg: "no such session"}
	}
	facts := convo.TurnFacts{}
	transcript, err := s.registry.TranscriptPath(ctx, id)
	if err != nil {
		return err
	}
	if transcript != "" {
		read, err := convo.Turn(ctx, transcript)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err == nil {
			facts = read
		}
	}
	answer := turnAnswer{TurnFacts: facts}
	if facts.StartedAt != "" && s.radar != nil {
		if started, err := time.Parse(time.RFC3339Nano, facts.StartedAt); err == nil {
			if tokens, ok := s.radar.OutputTokensSince(ctx, id, started); ok {
				answer.OutputTokens = &tokens
			}
		}
	}
	s.writeJSON(w, 200, answer, nil)
	return nil
}
