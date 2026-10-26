package server

// The session tasks route: one GET naming what a session is working on — its own todo list,
// read from the transcript its reports named, beside the channel tasks its member owns.

import (
	"errors"
	"io/fs"
	"net/http"
	"regexp"

	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/convo"
)

// sessionTasksPath is one session's tasks.
var sessionTasksPath = regexp.MustCompile(`^/api/sessions/([^/]+)/tasks$`)

// taskEntry is one channel task a session's member owns.
type taskEntry struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Status  string `json:"status"`
	Channel string `json:"channel"`
}

// sessionTasksRoute answers the session's todos and the channel tasks its member owns. The
// transcript is the path the session's own reports named — nothing a query names is ever read.
// A transcript the disk no longer has, a session in no channel, and a channel the hub does not
// hold leave their part of the answer empty.
func (s *server) sessionTasksRoute(w http.ResponseWriter, r *http.Request, path string) error {
	if s.registry == nil {
		return s.needsUnavailable()
	}
	id := sessionTasksPath.FindStringSubmatch(path)[1]
	ctx := r.Context()
	row, err := s.registry.SessionRow(ctx, id, s.now())
	if err != nil {
		return err
	}
	if row == nil {
		return &channel.HuddleError{Status: 404, Msg: "no such session"}
	}
	todos := []convo.Todo{}
	transcript, err := s.registry.TranscriptPath(ctx, id)
	if err != nil {
		return err
	}
	if transcript != "" {
		read, err := convo.Todos(ctx, transcript)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err == nil {
			todos = read
		}
	}
	tasks := []taskEntry{}
	if row.Channel != "" && row.Member != "" {
		held, err := s.hub.Exists(ctx, row.Channel)
		if err != nil {
			return err
		}
		if held {
			ch, err := s.hub.GetExisting(ctx, row.Channel)
			if err != nil {
				return err
			}
			owned, err := ch.Tasks(ctx, channel.TasksFilter{Owner: &row.Member})
			if err != nil {
				return err
			}
			for _, task := range owned {
				tasks = append(tasks, taskEntry{
					ID:      task.ID,
					Title:   task.Title,
					Status:  task.Status,
					Channel: row.Channel,
				})
			}
		}
	}
	s.writeJSON(w, 200, struct {
		Todos []convo.Todo `json:"todos"`
		Tasks []taskEntry  `json:"tasks"`
	}{todos, tasks}, nil)
	return nil
}
