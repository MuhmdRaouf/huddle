package server

// The shells routes: one agent's background shells listed from its transcript, and one shell's
// output paged off a byte cursor like the conversation is. The output file's path comes only
// from the transcript; no route ever reads a path from the request. An ended shell keeps the tail
// of its output in the registry, and the output route answers it once the file is gone.

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"regexp"

	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/convo"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
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
	if err := s.keepShellTails(r.Context(), match[1], match[2], shells); err != nil {
		return err
	}
	s.writeJSON(w, 200, struct {
		Shells []convo.Shell `json:"shells"`
	}{shells}, nil)
	return nil
}

// keepShellTails stores the tail of each ended shell that has none yet while its output file is
// there, so the output route can answer it once the OS clears the file. It runs only on a read
// of the list, and a shell with a stored tail costs no file read. A file that is gone or
// unreadable leaves nothing to keep.
func (s *server) keepShellTails(ctx context.Context, session, agent string, shells []convo.Shell) error {
	var kept map[string]bool
	for _, shell := range shells {
		if shell.State != "ended" || shell.ID == "" {
			continue
		}
		if kept == nil {
			var err error
			if kept, err = s.registry.ShellTailIDs(ctx, session, agent); err != nil {
				return err
			}
		}
		if kept[shell.ID] {
			continue
		}
		tail, err := shell.Tail()
		if err != nil {
			continue
		}
		if err := s.registry.SaveShellTail(ctx, session, agent, shell.ID, tail, jsscalar.ISOTime(s.now())); err != nil {
			return err
		}
	}
	return nil
}

// keptShellTail answers the page of the tail kept for a shell whose output file the disk lost.
// The tail is one page: a cursor past its start has been served already and gets an empty page.
// ok is false when the shell's file is there or no tail is kept.
func (s *server) keptShellTail(ctx context.Context, session, agent string, shells []convo.Shell, id string, after int64) (page convo.OutputPage, ok bool, err error) {
	for _, shell := range shells {
		if shell.ID != id {
			continue
		}
		if !shell.OutputGone() {
			return convo.OutputPage{}, false, nil
		}
		tail, kept, err := s.registry.ShellTail(ctx, session, agent, id)
		if err != nil || !kept {
			return convo.OutputPage{}, false, err
		}
		if after != 0 {
			return convo.OutputPage{Next: after, Size: int64(len(tail))}, true, nil
		}
		return convo.OutputPage{Text: tail, Next: int64(len(tail)), Size: int64(len(tail))}, true, nil
	}
	return convo.OutputPage{}, false, nil
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
	if page.Size == 0 {
		kept, ok, err := s.keptShellTail(r.Context(), match[1], match[2], shells, match[3], cursor.after)
		if err != nil {
			return err
		}
		if ok {
			page = kept
		}
	}
	s.writeJSON(w, 200, page, nil)
	return nil
}
