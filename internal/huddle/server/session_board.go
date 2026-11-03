// The kanban board a session is pinned to: the session itself pins it with `kanban board use`,
// and the owner reads and changes it from the session row.
package server

import (
	"net/http"
	"regexp"
	"strings"
	"unicode"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
)

// sessionBoardPath is one registry session's board pin.
var sessionBoardPath = regexp.MustCompile(`^/api/registry/sessions/([^/]+)/board$`)

// boardPinMax is the longest board name a pin keeps.
const boardPinMax = 64

// sessionBoardRoute reads (GET) or sets (POST {"board": name}) a session's pin; an empty name
// clears it. The owner reaches any session, a member only the session it acts as.
func (s *server) sessionBoardRoute(w http.ResponseWriter, r *http.Request, path string, me auth.Who) error {
	if s.registry == nil {
		return s.needsUnavailable()
	}
	id := sessionBoardPath.FindStringSubmatch(path)[1]
	ctx := r.Context()
	row, err := s.registry.SessionRow(ctx, id, s.now())
	if err != nil {
		return err
	}
	if row == nil {
		return &channel.HuddleError{Status: 404, Msg: "no session " + id}
	}
	pinned := row.Board
	if r.Method == http.MethodPost {
		if !me.Root && (row.Member == "" || me.Name != row.Member) {
			return &channel.HuddleError{Status: 403, Msg: "only the owner, or the session itself, pins its own session to a board"}
		}
		body, err := s.body(r)
		if err != nil {
			return err
		}
		pinned = strings.TrimSpace(textOf(fieldOf(body, "board")))
		if len(pinned) > boardPinMax || strings.IndexFunc(pinned, unicode.IsControl) >= 0 {
			return &channel.HuddleError{Status: 400, Msg: "board: a board name of at most 64 characters"}
		}
		if _, err := s.registry.SetBoard(ctx, id, pinned); err != nil {
			return err
		}
		s.emitRegistrySession(ctx, id)
	}
	s.writeJSON(w, 200, struct {
		Board string `json:"board"`
	}{pinned}, nil)
	return nil
}
