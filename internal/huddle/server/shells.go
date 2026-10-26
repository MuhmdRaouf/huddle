package server

// The shells routes: one agent's background shells listed from its transcript, and one shell's
// output paged off a byte cursor like the conversation is. The output file's path comes only
// from the transcript; no route ever reads a path from the request.

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"regexp"

	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/convo"
)

var (
	agentShellsPath      = regexp.MustCompile(`^/api/sessions/([^/]+)/agents/([^/]+)/shells$`)
	agentShellOutputPath = regexp.MustCompile(`^/api/sessions/([^/]+)/agents/([^/]+)/shells/([^/]+)/output$`)
)

// agentCursorOf settles what an agent's route reads: the session's transcript as its reports
// named it with the query's byte cursor, and the agent's own transcript — the session's own for
// the main agent, its subagent file for any other. A session the registry does not know, a
// cursor that is not a whole number and an id no agent of the session answers are refusals.
func (s *server) agentCursorOf(r *http.Request, id, agent string) (sessionCursor, error) {
	cursor, err := s.sessionCursorOf(r, id)
	if err != nil {
		return cursor, err
	}
	if agent != convo.MainAgent {
		found, ok := convo.AgentPath(cursor.transcript, agent)
		if !ok {
			return cursor, &channel.HuddleError{Status: 404, Msg: "no such agent"}
		}
		cursor.transcript = found
	}
	return cursor, nil
}

// readShells answers one transcript's shells; a session whose reports never named a transcript,
// and one whose transcript the disk lost, answer none.
func readShells(ctx context.Context, transcript string) ([]convo.Shell, error) {
	if transcript == "" {
		return []convo.Shell{}, nil
	}
	shells, err := convo.Shells(ctx, transcript)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err != nil {
		return []convo.Shell{}, nil
	}
	return shells, nil
}

// agentShellsRoute answers one agent's background shells in transcript order.
func (s *server) agentShellsRoute(w http.ResponseWriter, r *http.Request, path string) error {
	if s.registry == nil {
		return s.needsUnavailable()
	}
	match := agentShellsPath.FindStringSubmatch(path)
	cursor, err := s.agentCursorOf(r, match[1], match[2])
	if err != nil {
		return err
	}
	shells, err := readShells(r.Context(), cursor.transcript)
	if err != nil {
		return err
	}
	s.writeJSON(w, 200, struct {
		Shells []convo.Shell `json:"shells"`
	}{shells}, nil)
	return nil
}

// agentShellOutputRoute answers one shell's output page at the query's byte cursor. A shell the
// transcript's lines never named is a refusal.
func (s *server) agentShellOutputRoute(w http.ResponseWriter, r *http.Request, path string) error {
	if s.registry == nil {
		return s.needsUnavailable()
	}
	match := agentShellOutputPath.FindStringSubmatch(path)
	cursor, err := s.agentCursorOf(r, match[1], match[2])
	if err != nil {
		return err
	}
	shells, err := readShells(r.Context(), cursor.transcript)
	if err != nil {
		return err
	}
	page, ok, err := convo.ShellOutput(r.Context(), shells, match[3], cursor.after)
	if err != nil {
		return err
	}
	if !ok {
		return &channel.HuddleError{Status: 404, Msg: "no such shell"}
	}
	s.writeJSON(w, 200, page, nil)
	return nil
}
