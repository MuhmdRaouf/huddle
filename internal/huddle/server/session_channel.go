// The owner's "Add to channel": a live session joins an existing channel at once and its project
// remembers the channel, so the project's next session joins on start.
package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"regexp"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
)

// sessionChannelPath is one registry session's channel, the target of the owner's add.
var sessionChannelPath = regexp.MustCompile(`^/api/registry/sessions/([^/]+)/channel$`)

// ProjectFiles reads and writes the settings file of the project a session works in. The hub
// reaches the files through it because their format belongs to the CLI.
type ProjectFiles interface {
	// MemberName answers the name a session working in cwd joins as: asked when given, else the
	// name the project's settings file holds, else the project's own name.
	MemberName(ctx context.Context, cwd, asked string) string
	// SaveChannel writes channel and name into the settings file of the project around cwd and
	// answers that file.
	SaveChannel(ctx context.Context, cwd, channel, name string) (string, error)
}

// sessionChannelRoute joins one live session to an existing channel, then saves the channel to
// the session's project. The join comes first so a channel that refuses the session (an
// allowlist) leaves the project as it was. The channel is never created here: a name nobody made
// is the owner's mistake to see. A session on another machine has no directory here to save to.
func (s *server) sessionChannelRoute(w http.ResponseWriter, r *http.Request, path string, me auth.Who) error {
	if !me.Root {
		return &channel.HuddleError{Status: 403, Msg: "only the owner adds a session to a channel"}
	}
	if s.registry == nil {
		return s.needsUnavailable()
	}
	// the join puts words and a file into a session's project, so it is gated like an allow
	if s.stepUp.Required(r, me) {
		return &channel.HuddleError{Status: 401, Msg: "step-up required"}
	}
	id := sessionChannelPath.FindStringSubmatch(path)[1]
	ctx := r.Context()
	row, err := s.registry.SessionRow(ctx, id, s.now())
	if err != nil {
		return err
	}
	if row == nil {
		return &channel.HuddleError{Status: 404, Msg: "no session " + id}
	}
	if err := s.localProject(ctx, row.Machine, row.Cwd); err != nil {
		return err
	}
	body, err := s.body(r)
	if err != nil {
		return err
	}
	name := textOf(fieldOf(body, "channel"))
	if name == "" {
		return &channel.HuddleError{Status: 400, Msg: "channel: the name of the channel to join"}
	}
	ch, err := s.hub.GetExisting(ctx, name)
	if err != nil {
		var refused *channel.HuddleError
		if errors.As(err, &refused) && refused.Status == 404 {
			return &channel.HuddleError{Status: 409, Msg: "no channel " + name +
				": make it first with New channel on the Channels page"}
		}
		return err
	}
	member := s.o.ProjectFiles.MemberName(ctx, row.Cwd, textOf(fieldOf(body, "as")))
	task := "added from the dashboard"
	if _, err := ch.Join(ctx, member, channel.JoinOpts{Task: &task}); err != nil {
		return err
	}
	channel.Link(ctx, ch, member, id)
	file, err := s.o.ProjectFiles.SaveChannel(ctx, row.Cwd, name, member)
	if err != nil {
		return &channel.HuddleError{Status: 500, Msg: "joined " + name + " as " + member +
			", but the project's settings could not be saved: " + err.Error()}
	}
	if _, err := s.audit(ctx, auditActor(me), "add-to-channel", id, struct {
		Channel string `json:"channel"`
		Member  string `json:"member"`
	}{name, member}); err != nil {
		return err
	}
	s.writeJSON(w, 200, struct {
		OK      bool   `json:"ok"`
		Channel string `json:"channel"`
		Member  string `json:"member"`
		Saved   string `json:"saved"`
	}{true, name, member, file}, nil)
	return nil
}

// localProject says whether a session's directory is one this hub can write to: the session ran
// on this machine and its directory is here. A session on another machine answers 501 with the
// machine's name.
func (s *server) localProject(ctx context.Context, machine, cwd string) error {
	if own := s.thisMachine(ctx); machine != "" && own != "" && machine != own {
		return &channel.HuddleError{Status: 501, Msg: "this session runs on " + machine +
			": add it to a channel from that machine"}
	}
	if info, err := os.Stat(cwd); cwd == "" || err != nil || !info.IsDir() {
		return &channel.HuddleError{Status: 501, Msg: "this session's directory is not on this machine"}
	}
	if s.o.ProjectFiles == nil {
		return &channel.HuddleError{Status: 501, Msg: "this hub keeps no project settings"}
	}
	return nil
}
