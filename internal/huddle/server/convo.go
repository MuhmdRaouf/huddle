package server

// The conversation route and the transcript watch: a session's transcript read as conversation
// items off a byte cursor, and the registry streams told when a live transcript grows.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/convo"
)

// conversationPath is one session's conversation, its items paged from a byte cursor.
var conversationPath = regexp.MustCompile(`^/api/sessions/([^/]+)/conversation$`)

// conversationRoute answers one session's conversation from its transcript: the items of the
// complete lines past the query's byte cursor, and the cursor the next read starts at. The
// transcript is the path the session's own reports named — nothing a query names is ever read.
// A transcript the disk no longer has, and a session whose reports never named one, answer an
// empty page at the cursor: the conversation is what the transcript still says.
func (s *server) conversationRoute(w http.ResponseWriter, r *http.Request, path string) error {
	if s.registry == nil {
		return s.needsUnavailable()
	}
	id := conversationPath.FindStringSubmatch(path)[1]
	after := int64(0)
	if raw := r.URL.Query().Get("after"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			return &channel.HuddleError{Status: 400, Msg: "after must be a non-negative whole number"}
		}
		after = parsed
	}
	row, err := s.registry.SessionRow(r.Context(), id, s.now())
	if err != nil {
		return err
	}
	if row == nil {
		return &channel.HuddleError{Status: 404, Msg: "no such session"}
	}
	transcript, err := s.registry.TranscriptPath(r.Context(), id)
	if err != nil {
		return err
	}
	page := convo.Page{Items: []convo.Item{}, Next: after}
	if transcript != "" {
		read, err := convo.Read(r.Context(), transcript, after)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err == nil {
			page = read
		}
	}
	s.writeJSON(w, 200, page, nil)
	return nil
}

// grewEvent is one conversation.grew event's data: which session, and how many bytes its
// transcript now holds.
type grewEvent struct {
	SessionID string `json:"session_id"`
	Size      int64  `json:"size"`
}

// emitConversationGrew fans one conversation.grew event out to every open registry stream, so
// a page holding the conversation refetches it from its cursor. Best effort, like every emit.
func (s *server) emitConversationGrew(id string, size int64) {
	encoded, err := domain.MarshalCompactJSON(struct {
		Type string    `json:"type"`
		Data grewEvent `json:"data"`
	}{"conversation.grew", grewEvent{SessionID: id, Size: size}})
	if err != nil {
		return
	}
	s.fanRegistryRow("data: " + string(encoded) + "\n\n")
}

// watchTranscripts tells every open registry stream which live transcripts grew, each pass:
// the sizes are the watch's own, a session no longer listed leaving the map. A pass with no
// stream open stats nothing; a first sighting records a size and fans nothing, so a session
// the watch meets mid-life starts at the size it already is.
func (s *server) watchTranscripts(ctx context.Context) {
	interval := time.Duration(s.o.TranscriptCheckMS) * time.Millisecond
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	sizes := map[string]int64{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.registrySubsMu.Lock()
			open := len(s.registrySubs) > 0
			s.registrySubsMu.Unlock()
			if !open {
				continue
			}
			live, err := s.registry.LiveTranscripts(ctx)
			if err != nil {
				fmt.Fprintf(s.o.Stderr, "registry: transcript watch: %v\n", err)
				continue
			}
			for id, path := range live {
				info, err := os.Stat(path)
				if err != nil {
					continue
				}
				if seen, ok := sizes[id]; ok && info.Size() > seen {
					s.emitConversationGrew(id, info.Size())
				}
				sizes[id] = info.Size()
			}
			for id := range sizes {
				if _, ok := live[id]; !ok {
					delete(sizes, id)
				}
			}
		}
	}
}
