package server

// The session turn route: one GET naming what the session's transcript says about its current
// turn — when it started, whether it ended, and the permission mode in force — so the sessions
// screen's footer counts the elapsed time itself from the start it is given.

import (
	"errors"
	"io/fs"
	"net/http"
	"regexp"

	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/convo"
)

// sessionTurnPath is one session's turn facts.
var sessionTurnPath = regexp.MustCompile(`^/api/sessions/([^/]+)/turn$`)

// sessionTurnRoute answers the turn facts the session's transcript holds. The transcript is the
// path the session's own reports named — nothing a query names is ever read. No transcript, or
// one the disk no longer has, answers a turn that never started; any other read error is the
// answer's error.
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
	s.writeJSON(w, 200, facts, nil)
	return nil
}
