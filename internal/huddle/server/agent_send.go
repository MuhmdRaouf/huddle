package server

// The agent send route: the owner's words to one of a session's subagents. A subagent has no
// inbox of its own — only the session's main agent reads owner messages — so the words go to
// the main as one forward request naming the agent, and the main passes them on with Claude
// Code's SendMessage tool.

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"regexp"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/convo"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
)

// agentSendPath is one subagent of one session, the target of an owner's forward.
var agentSendPath = regexp.MustCompile(`^/api/sessions/([^/]+)/agents/([^/]+)/send$`)

// agentSendRoute answers the owner's send to one subagent: the text queued as one forward
// request to the main, which passes it on with SendMessage. A forward answers none of the
// session's open questions, so they stay open.
func (s *server) agentSendRoute(w http.ResponseWriter, r *http.Request, path string, me auth.Who) error {
	if s.registry == nil {
		return s.needsUnavailable()
	}
	// the owner's words reach a session's work, so it is gated like an allow
	if s.stepUp.Required(r, me) {
		return &channel.HuddleError{Status: 401, Msg: "step-up required"}
	}
	body, err := s.body(r)
	if err != nil {
		return err
	}
	text := textOf(fieldOf(body, "text"))
	if reason := messageTextErr(text); reason != "" {
		return &channel.HuddleError{Status: 400, Msg: reason}
	}
	match := agentSendPath.FindStringSubmatch(path)
	id, agent := match[1], match[2]
	transcript, err := s.sessionTranscriptOf(r, id)
	if err != nil {
		return err
	}
	if agent == convo.MainAgent {
		return &channel.HuddleError{Status: 400, Msg: "send to the main through the session's answer route"}
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
	var target convo.Agent
	found := false
	for _, one := range agents {
		if one.ID == agent {
			target, found = one, true
			break
		}
	}
	if !found {
		return &channel.HuddleError{Status: 404, Msg: "no such agent"}
	}
	if !target.Running {
		return &channel.HuddleError{Status: 409,
			Msg: fmt.Sprintf("agent %s has finished: the main can no longer reach it", agent)}
	}
	in := hub.MessageIn{SessionID: id, FromKind: hub.FromOwner, Text: forwardText(target, text)}
	route, messageID, err := s.queueForSession(r.Context(), id, in)
	if err != nil {
		return err
	}
	// the message stands once queued, so a row that cannot be written is answered as an error:
	// an unaudited forward never passes silently
	if _, err := s.audit(r.Context(), auditActor(me), "forward", id, nil); err != nil {
		return err
	}
	row, err := s.registry.SessionRow(r.Context(), id, s.now())
	if err != nil {
		return err
	}
	channelName := ""
	if row != nil {
		channelName = row.Channel
	}
	s.emitRow(r.Context(), channelName, id)
	s.writeJSON(w, 200, struct {
		OK      bool   `json:"ok"`
		Route   string `json:"route"`
		Message int64  `json:"message"`
		Agent   string `json:"agent"`
	}{true, route, messageID, agent}, nil)
	return nil
}

// forwardText wraps the owner's words as one request to the main: pass them to the named
// subagent with SendMessage, word for word, and report its answer back.
func forwardText(target convo.Agent, text string) string {
	who := "subagent " + target.ID + " (" + target.Type
	if target.Description != "" {
		who += ": " + target.Description
	}
	who += ")"
	return fmt.Sprintf("The owner asks you to forward the message below to your %s with SendMessage, "+
		"word for word, and then to tell the owner its answer.\n\n%s", who, text)
}
