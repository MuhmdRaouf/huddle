package server

// The session digest's arrival and its two owner acts (SPEC 8). A Stop report that carries a
// digest is shared the moment it lands: every digest-level link of the session holds at most
// the sender's newest digest (the next one drops the one still waiting, so a peer reads each
// peer's latest words once, on its next prompt), and the session's own channel hears it under
// digest.<session>. Brief and hand over put one session's context into another on demand. A
// satellite's reports ride here too — its machine's batch lands through the same Report — so a
// digest crosses machines with no satellite code of its own.

import (
	"context"
	"fmt"
	"net/http"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
)

// shareDigest fans one arriving digest out to the links and the channel that hear it. Nothing
// here may fail the report that carried the digest: every error is one line, and the next Stop
// rebuilds the whole digest anyway.
func (s *server) shareDigest(ctx context.Context, rep hub.Report) {
	if s.registry == nil {
		return
	}
	links, err := s.registry.LinksOfSession(ctx, rep.SessionID)
	if err != nil {
		fmt.Fprintf(s.o.Stdout, "digest: %v\n", err)
		return
	}
	for _, link := range links {
		peers, err := s.registry.LinksOf(ctx, link.Channel)
		if err != nil {
			fmt.Fprintf(s.o.Stdout, "digest: %v\n", err)
			continue
		}
		for _, peer := range peers {
			// the level is the tie's whole contract: overhear hears posts, on-request waits
			// for a brief, only digest receives these
			if peer.SessionID == rep.SessionID || peer.Level != hub.LevelDigest {
				continue
			}
			if err := s.registry.DropPendingDigests(ctx, rep.SessionID, peer.SessionID); err != nil {
				fmt.Fprintf(s.o.Stdout, "digest: %v\n", err)
				continue
			}
			in := hub.MessageIn{
				SessionID: peer.SessionID, FromKind: hub.FromDigest,
				FromName: rep.SessionID, Text: rep.Digest,
			}
			if _, _, err := s.queueForSession(ctx, peer.SessionID, in); err != nil {
				fmt.Fprintf(s.o.Stdout, "digest: the digest for %s is not queued: %v\n", peer.SessionID, err)
			}
		}
	}
	// the session's own channel hears the digest on the session's topic, member or not
	row, err := s.registry.SessionRow(ctx, rep.SessionID, s.now())
	if err != nil || row == nil || row.Channel == "" || row.Member == "" {
		return
	}
	ch, err := s.hub.GetExisting(ctx, row.Channel)
	if err != nil {
		return // a registry row may name a channel that is gone; the links already heard it
	}
	if _, err := ch.Publish(ctx, row.Member, channel.PublishArgs{
		Topic: "digest." + rep.SessionID, Msg: rep.Digest,
	}); err != nil {
		fmt.Fprintf(s.o.Stdout, "digest: the publish for %s is not written: %v\n", rep.SessionID, err)
	}
}

// briefBody is what brief and hand over read: who is briefed from whom.
type briefBody struct {
	From string
	To   string
}

// briefOf reads the body's pair, checks both sessions and answers the sender's newest digest.
// The step-up is the route's to make: a brief carries a session's words into another (SPEC 7.4).
func (s *server) briefOf(ctx context.Context, r *http.Request) (string, briefBody, error) {
	body, err := s.body(r)
	if err != nil {
		return "", briefBody{}, err
	}
	pair := briefBody{From: textOf(fieldOf(body, "from")), To: textOf(fieldOf(body, "to"))}
	if pair.From == "" || pair.To == "" {
		return "", pair, &channel.HuddleError{Status: 400, Msg: "name from and to"}
	}
	for _, id := range []string{pair.From, pair.To} {
		row, err := s.registry.SessionRow(ctx, id, s.now())
		if err != nil {
			return "", pair, err
		}
		if row == nil {
			return "", pair, &channel.HuddleError{Status: 404, Msg: "no session " + id}
		}
	}
	digest, _, ok, err := s.registry.DigestOf(ctx, pair.From)
	if err != nil {
		return "", pair, err
	}
	if !ok {
		return "", pair, &channel.HuddleError{Status: 404, Msg: "session " + pair.From + " has no digest yet"}
	}
	return digest, pair, nil
}

// briefRoute sends one session's newest digest to another as a message (SPEC 8). A brief is
// explicit: nothing already waiting is dropped, the owner asked for this one to arrive.
func (s *server) briefRoute(w http.ResponseWriter, r *http.Request, me auth.Who) error {
	if s.registry == nil {
		return s.needsUnavailable()
	}
	if s.stepUp.Required(r, me) {
		return &channel.HuddleError{Status: 401, Msg: "step-up required"}
	}
	ctx := r.Context()
	digest, pair, err := s.briefOf(ctx, r)
	if err != nil {
		return err
	}
	route, _, err := s.queueForSession(ctx, pair.To, hub.MessageIn{
		SessionID: pair.To, FromKind: hub.FromDigest, FromName: pair.From, Text: digest,
	})
	if err != nil {
		return err
	}
	if _, err := s.audit(ctx, auditActor(me), "brief", pair.To, nil); err != nil {
		return err
	}
	s.writeJSON(w, 200, struct {
		Route string `json:"route"`
	}{route}, nil)
	return nil
}

// handoverRoute briefs another session and gives it the work: the sender's open tasks move to
// the receiver and the sender leaves the channel (SPEC 8). Both must sit in the one channel,
// because that is the only place whose tasks and presence the move can name.
func (s *server) handoverRoute(w http.ResponseWriter, r *http.Request, me auth.Who) error {
	if s.registry == nil {
		return s.needsUnavailable()
	}
	if s.stepUp.Required(r, me) {
		return &channel.HuddleError{Status: 401, Msg: "step-up required"}
	}
	ctx := r.Context()
	digest, pair, err := s.briefOf(ctx, r)
	if err != nil {
		return err
	}
	fromRow, err := s.registry.SessionRow(ctx, pair.From, s.now())
	if err != nil {
		return err
	}
	toRow, err := s.registry.SessionRow(ctx, pair.To, s.now())
	if err != nil {
		return err
	}
	if fromRow.Channel == "" || toRow.Channel != fromRow.Channel || fromRow.Member == "" || toRow.Member == "" {
		return &channel.HuddleError{Status: 409, Msg: "hand over needs both sessions in one channel"}
	}
	if _, _, err := s.queueForSession(ctx, pair.To, hub.MessageIn{
		SessionID: pair.To, FromKind: hub.FromDigest, FromName: pair.From, Text: digest,
	}); err != nil {
		return err
	}
	ch, err := s.hub.GetExisting(ctx, fromRow.Channel)
	if err != nil {
		return err
	}
	tasks, err := ch.Tasks(ctx, channel.TasksFilter{Owner: &fromRow.Member})
	if err != nil {
		return err
	}
	moved := []string{}
	for _, task := range tasks {
		// done work is history, not a handover's burden; everything else moves whole
		if task.Status == "done" || task.Status == "skipped" {
			continue
		}
		if _, err := ch.UpdateTask(ctx, toRow.Member, task.ID,
			channel.UpdateTaskInput{Owner: &toRow.Member}); err != nil {
			return err
		}
		moved = append(moved, task.ID)
	}
	if _, err := ch.Leave(ctx, fromRow.Member, "handed over to "+toRow.Member); err != nil {
		return err
	}
	if _, err := s.audit(ctx, auditActor(me), "handover", pair.From, nil); err != nil {
		return err
	}
	s.writeJSON(w, 200, struct {
		Moved []string `json:"moved"`
	}{moved}, nil)
	return nil
}
