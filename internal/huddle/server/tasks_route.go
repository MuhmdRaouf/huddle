package server

// The session tasks route: one GET naming what a session is working on — its own todo list,
// read from the transcript its reports named, beside the channel tasks its member owns and the
// kanban cards it works on: the cards it holds on the board at its working directory, or the
// not-done cards it authored there, beside the board's key.

import (
	"errors"
	"io/fs"
	"net/http"
	"regexp"

	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/convo"
	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
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

// cardEntry is one kanban card a session's member works on: a card it holds, or — when it
// holds none — one it authored. Source names which of the two lists the card came from.
type cardEntry struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Column  string `json:"column"`
	Cluster string `json:"cluster"`
	Source  string `json:"source"`
}

// memberCards answers the not-done cards of the list the member works on: the cards it holds,
// or — when it holds none — the cards it authored. Either way the list keeps its order and the
// names match exactly.
func memberCards(listed []board.Card, member string) []cardEntry {
	held, authored := []cardEntry{}, []cardEntry{}
	for _, card := range listed {
		if card.Column == "done" {
			continue
		}
		switch {
		case card.Holder == member:
			held = append(held, cardEntry{ID: card.ID, Title: card.Title, Column: card.Column, Cluster: card.Cluster, Source: "held"})
		case card.Author == member:
			authored = append(authored, cardEntry{ID: card.ID, Title: card.Title, Column: card.Column, Cluster: card.Cluster, Source: "authored"})
		}
	}
	if len(held) > 0 {
		return held
	}
	return authored
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
	cards := []cardEntry{}
	boardKey := ""
	if row.Cwd != "" {
		// the board is the one at the session's own working directory; noNamedEnv keeps the
		// huddle process's own KANBAN_BOARD out of it. A dir with no board holds no cards.
		if b, err := board.Resolve("", noNamedEnv, row.Cwd); err == nil {
			if s.kanban != nil {
				boardKey = s.kanban.BoardKey(ctx, b.Root)
			}
			if row.Member != "" {
				held, err := b.List(ctx, "")
				if err != nil {
					return err
				}
				cards = memberCards(held, row.Member)
			}
		}
	}
	s.writeJSON(w, 200, struct {
		Todos []convo.Todo `json:"todos"`
		Tasks []taskEntry  `json:"tasks"`
		Cards []cardEntry  `json:"cards"`
		Board string       `json:"board"`
	}{todos, tasks, cards, boardKey}, nil)
	return nil
}
