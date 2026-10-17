package server

// The link routes and the overhear fan-out (SPEC 8): the owner ties 2 or more sessions — here or
// on a satellite — to a channel at a level, and every broadcast post the channel carries is fanned
// out to the links that overhear it. A linked session is no member: the channel's events do not
// reach it on their own, the fan-out queues each post through the owner-message queue, so a
// satellite's session is served by its machine's poll with no code of its own.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// one link by its channel and its session, and one channel's links
var (
	linkPath    = regexp.MustCompile(`^/api/links/([^/]+)/([^/]+)$`)
	linksOfPath = regexp.MustCompile(`^/api/links/([^/]+)$`)
)

// pathPart answers one captured path part decoded: a client may name its channel or session with
// escapes the URL's own parse leaves in place.
func pathPart(raw string) string {
	if part, err := url.PathUnescape(raw); err == nil {
		return part
	}
	return raw
}

// linkRoute ties the named sessions to the channel at the one level and tells each session of the
// tie on its own delivery route. Linking carries the owner's reach into sessions (SPEC 7.4), so
// it is gated like a send; reads and the level move are not.
func (s *server) linkRoute(w http.ResponseWriter, r *http.Request, me auth.Who) error {
	if s.registry == nil {
		return s.needsUnavailable()
	}
	if s.stepUp.Required(r, me) {
		return &channel.HuddleError{Status: 401, Msg: "step-up required"}
	}
	body, err := s.body(r)
	if err != nil {
		return err
	}
	sessions, ok := sessionListOf(body)
	if !ok {
		return &channel.HuddleError{Status: 400, Msg: "sessions: an array of session ids"}
	}
	distinct := make([]string, 0, len(sessions))
	seen := map[string]bool{}
	for _, id := range sessions {
		if !seen[id] {
			seen[id] = true
			distinct = append(distinct, id)
		}
	}
	if len(distinct) < 2 {
		return &channel.HuddleError{Status: 400, Msg: "link takes 2 or more sessions"}
	}
	level := textOf(fieldOf(body, "level"))
	if level == "" {
		level = hub.LevelDigest
	}
	if !hub.ValidLevel(level) {
		return &channel.HuddleError{Status: 400, Msg: "level is overhear, digest or on-request"}
	}
	ctx := r.Context()
	rows := make([]*hub.SessionRow, 0, len(distinct))
	for _, id := range distinct {
		row, err := s.registry.SessionRow(ctx, id, s.now())
		if err != nil {
			return err
		}
		if row == nil {
			return &channel.HuddleError{Status: 404, Msg: "no session " + id}
		}
		rows = append(rows, row)
	}
	var ch *channel.Channel
	if truthy(fieldOf(body, "create")) {
		ch, err = s.hub.Get(ctx, textOf(fieldOf(body, "channel")))
	} else {
		ch, err = s.hub.GetExisting(ctx, textOf(fieldOf(body, "channel")))
	}
	if err != nil {
		return err
	}
	if err := s.registry.LinkSessions(ctx, ch.Name, distinct, level, jsfmt.ISOTime(s.now())); err != nil {
		return err
	}
	// each session hears of the tie at once, on its own route — a satellite's through its
	// machine's poll, the rest on their next prompt
	for i, id := range distinct {
		others := make([]string, 0, len(distinct)-1)
		for j, row := range rows {
			if j == i {
				continue
			}
			if row.Title != "" {
				others = append(others, row.Title)
				continue
			}
			others = append(others, row.SessionID)
		}
		in := hub.MessageIn{SessionID: id, FromKind: hub.FromOwner, Text: fmt.Sprintf(
			"You are linked into #%s with %s at level %s.", ch.Name, strings.Join(others, ", "), level)}
		if _, _, err := s.queueForSession(ctx, id, in); err != nil {
			return err
		}
	}
	s.writeJSON(w, 200, struct {
		Channel  string   `json:"channel"`
		Level    string   `json:"level"`
		Sessions []string `json:"sessions"`
	}{ch.Name, level, distinct}, nil)
	return nil
}

// linkLevelRoute moves one link's level: what a session hears changes without a new link. No
// step-up: nothing reaches the session but a change of what it already agreed to hear.
func (s *server) linkLevelRoute(w http.ResponseWriter, r *http.Request, path string) error {
	if s.registry == nil {
		return s.needsUnavailable()
	}
	body, err := s.body(r)
	if err != nil {
		return err
	}
	level := textOf(fieldOf(body, "level"))
	if !hub.ValidLevel(level) {
		return &channel.HuddleError{Status: 400, Msg: "level is overhear, digest or on-request"}
	}
	parts := linkPath.FindStringSubmatch(path)
	moved, err := s.registry.SetLinkLevel(r.Context(), pathPart(parts[1]), pathPart(parts[2]), level)
	if err != nil {
		return err
	}
	if !moved {
		return &channel.HuddleError{Status: 404, Msg: "no link"}
	}
	s.writeJSON(w, 200, struct {
		Channel   string `json:"channel"`
		SessionID string `json:"session_id"`
		Level     string `json:"level"`
	}{pathPart(parts[1]), pathPart(parts[2]), level}, nil)
	return nil
}

// unlinkRoute ends one link at once (SPEC 8: no further sharing). The owner's reach out of a
// channel into a session is what linking carried in, so it is gated like a send.
func (s *server) unlinkRoute(w http.ResponseWriter, r *http.Request, path string, me auth.Who) error {
	if s.registry == nil {
		return s.needsUnavailable()
	}
	if s.stepUp.Required(r, me) {
		return &channel.HuddleError{Status: 401, Msg: "step-up required"}
	}
	parts := linkPath.FindStringSubmatch(path)
	gone, err := s.registry.Unlink(r.Context(), pathPart(parts[1]), pathPart(parts[2]))
	if err != nil {
		return err
	}
	if !gone {
		return &channel.HuddleError{Status: 404, Msg: "no link"}
	}
	s.writeJSON(w, 200, struct {
		OK bool `json:"ok"`
	}{true}, nil)
	return nil
}

// linksListRoute answers every link grouped by channel, in the one order the query reads them —
// a read, so it owes no step-up.
func (s *server) linksListRoute(w http.ResponseWriter, r *http.Request) error {
	if s.registry == nil {
		return s.needsUnavailable()
	}
	rows, err := s.registry.AllLinks(r.Context())
	if err != nil {
		return err
	}
	type group struct {
		Channel string        `json:"channel"`
		Links   []hub.LinkRow `json:"links"`
	}
	groups := make([]group, 0)
	for _, row := range rows {
		if len(groups) == 0 || groups[len(groups)-1].Channel != row.Channel {
			groups = append(groups, group{Channel: row.Channel, Links: make([]hub.LinkRow, 0)})
		}
		cur := &groups[len(groups)-1]
		cur.Links = append(cur.Links, row)
	}
	s.writeJSON(w, 200, struct {
		Channels []group `json:"channels"`
	}{groups}, nil)
	return nil
}

// linksOfRoute answers one channel's links.
func (s *server) linksOfRoute(w http.ResponseWriter, r *http.Request, path string) error {
	if s.registry == nil {
		return s.needsUnavailable()
	}
	links, err := s.registry.LinksOf(r.Context(), pathPart(linksOfPath.FindStringSubmatch(path)[1]))
	if err != nil {
		return err
	}
	s.writeJSON(w, 200, struct {
		Links []hub.Link `json:"links"`
	}{links}, nil)
	return nil
}

// overhear fans one channel op's broadcast post out to the sessions linked to the channel at the
// overhear level: a publish, or a send that names no recipient. The speaker's own session is
// skipped — its channel already carries its words — and digest and on-request links hear no posts
// here (HUB-DIGEST fills digest; on-request is what a brief targets). A failure is logged on one
// line and never fails the op that fed it.
func (s *server) overhear(ctx context.Context, ch, from, op string, body *jsfmt.Value) {
	if s.registry == nil {
		return
	}
	if op != "publish" && (op != "send" || fieldOf(body, "to") != nil) {
		return // no broadcast, nothing to overhear
	}
	text := textOf(fieldOf(body, "msg"))
	if text == "" {
		return
	}
	links, err := s.registry.LinksOf(ctx, ch)
	if err != nil {
		fmt.Fprintf(s.o.Stdout, "links: %v\n", err)
		return
	}
	for _, link := range links {
		if link.Level != hub.LevelOverhear {
			continue
		}
		if s.speaksHere(ctx, link.SessionID, ch, from) {
			continue
		}
		in := hub.MessageIn{
			SessionID: link.SessionID, FromKind: hub.FromChannel,
			FromName: ch + "/" + from, Text: text,
		}
		if _, _, err := s.queueForSession(ctx, link.SessionID, in); err != nil {
			fmt.Fprintf(s.o.Stdout, "links: the post for %s is not queued: %v\n", link.SessionID, err)
		}
	}
}

// speaksHere answers whether the session is the channel member that spoke: its own channel
// already carries its words.
func (s *server) speaksHere(ctx context.Context, session, ch, member string) bool {
	row, err := s.registry.SessionRow(ctx, session, s.now())
	return err == nil && row != nil && row.Channel == ch && row.Member == member
}
