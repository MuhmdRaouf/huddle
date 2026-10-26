package server

// The agents routes: one session's agents listed from its transcript's subagents folder, and one
// agent's conversation paged from a byte cursor like the session's own conversation is.

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"regexp"
	"strconv"

	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/convo"
)

// agentsPath is one session's agents list; agentConversationPath one agent's conversation.
var (
	agentsPath            = regexp.MustCompile(`^/api/sessions/([^/]+)/agents$`)
	agentConversationPath = regexp.MustCompile(`^/api/sessions/([^/]+)/agents/([^/]+)/conversation$`)
)

// sessionCursor is what a conversation route settles before reading: the session's transcript
// as its reports named it, and the query's byte cursor.
type sessionCursor struct {
	transcript string
	after      int64
}

// sessionTranscriptOf answers one session's transcript path, refusing a session the registry
// does not know.
func (s *server) sessionTranscriptOf(r *http.Request, id string) (string, error) {
	row, err := s.registry.SessionRow(r.Context(), id, s.now())
	if err != nil {
		return "", err
	}
	if row == nil {
		return "", &channel.HuddleError{Status: 404, Msg: "no such session"}
	}
	return s.registry.TranscriptPath(r.Context(), id)
}

// sessionCursorOf adds the query's byte cursor to the session's transcript. A cursor that is not
// a non-negative whole number is a refusal.
func (s *server) sessionCursorOf(r *http.Request, id string) (sessionCursor, error) {
	cursor := sessionCursor{}
	if raw := r.URL.Query().Get("after"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			return cursor, &channel.HuddleError{Status: 400, Msg: "after must be a non-negative whole number"}
		}
		cursor.after = parsed
	}
	transcript, err := s.sessionTranscriptOf(r, id)
	if err != nil {
		return cursor, err
	}
	cursor.transcript = transcript
	return cursor, nil
}

// readPage reads one transcript from a byte cursor with the reader the route chose. A session
// that never named a transcript, and a file the disk lost, answer an empty page at the cursor.
func readPage(ctx context.Context, transcript string, after int64, read func(context.Context, string, int64) (convo.Page, error)) (convo.Page, error) {
	if transcript == "" {
		return convo.Page{Items: []convo.Item{}, Next: after}, nil
	}
	page, err := read(ctx, transcript, after)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return convo.Page{}, err
	}
	if err != nil {
		return convo.Page{Items: []convo.Item{}, Next: after}, nil
	}
	return page, nil
}

// agentsRoute answers one session's agents: the main first, then its subagents, then its side
// chats. A session whose reports never named a transcript, and one whose transcript the disk
// lost, answer none.
func (s *server) agentsRoute(w http.ResponseWriter, r *http.Request, path string) error {
	if s.registry == nil {
		return s.needsUnavailable()
	}
	id := agentsPath.FindStringSubmatch(path)[1]
	transcript, err := s.sessionTranscriptOf(r, id)
	if err != nil {
		return err
	}
	agents := []convo.Agent{}
	if transcript != "" {
		listed, err := convo.Agents(r.Context(), transcript)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err == nil {
			agents = listed
		}
	}
	s.writeJSON(w, 200, struct {
		Agents []convo.Agent `json:"agents"`
	}{agents}, nil)
	return nil
}

// agentConversationRoute answers one agent's conversation: the main's is what the session's own
// conversation route answers, any other id's its subagent transcript read with its sidechain
// lines kept. An id no agent of the session answers is a refusal.
func (s *server) agentConversationRoute(w http.ResponseWriter, r *http.Request, path string) error {
	if s.registry == nil {
		return s.needsUnavailable()
	}
	match := agentConversationPath.FindStringSubmatch(path)
	id, agent := match[1], match[2]
	cursor, err := s.sessionCursorOf(r, id)
	if err != nil {
		return err
	}
	transcript, read := cursor.transcript, convo.Read
	if agent != convo.MainAgent {
		found, ok := convo.AgentPath(transcript, agent)
		if !ok {
			return &channel.HuddleError{Status: 404, Msg: "no such agent"}
		}
		transcript, read = found, convo.ReadAgent
	}
	page, err := readPage(r.Context(), transcript, cursor.after, read)
	if err != nil {
		return err
	}
	s.writeJSON(w, 200, page, nil)
	return nil
}
