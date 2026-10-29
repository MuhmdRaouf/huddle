package server

// The conversation route and the transcript watch: a session's transcript read as conversation
// items off a byte cursor, and the registry streams told when a live transcript grows.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
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
	cursor, err := s.sessionCursorOf(r, id)
	if err != nil {
		return err
	}
	page, err := readPage(r.Context(), cursor.transcript, cursor.after, convo.Read)
	if err != nil {
		return err
	}
	s.writeJSON(w, 200, page, nil)
	return nil
}

// grewEvent is one conversation.grew event's data: which session, which of its agents, and how
// many bytes that transcript now holds.
type grewEvent struct {
	SessionID string `json:"session_id"`
	Agent     string `json:"agent"`
	Size      int64  `json:"size"`
}

// emitConversationGrew fans one conversation.grew event out to every open registry stream, so
// a page holding the conversation refetches it from its cursor. Best effort, like every emit.
func (s *server) emitConversationGrew(id, agent string, size int64) {
	encoded, err := domain.MarshalCompactJSON(struct {
		Type string    `json:"type"`
		Data grewEvent `json:"data"`
	}{"conversation.grew", grewEvent{SessionID: id, Agent: agent, Size: size}})
	if err != nil {
		return
	}
	s.fanRegistryRow("data: " + string(encoded) + "\n\n")
}

// transcriptFile is one transcript the watch tracks: which of the session's agents it belongs
// to, and how many bytes it holds.
type transcriptFile struct {
	agent string
	size  int64
}

// agentTranscripts stats one session's main transcript and reads its subagents folder once: the
// main as agent main, each agent-<id>.jsonl beside it as its id from the name. A main the disk
// lost, and a session with no subagents folder, answer what is there.
func agentTranscripts(path string) []transcriptFile {
	var out []transcriptFile
	if info, err := os.Stat(path); err == nil {
		out = append(out, transcriptFile{agent: convo.MainAgent, size: info.Size()})
	}
	entries, err := os.ReadDir(convo.SubagentsDir(path))
	if err != nil {
		return out
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "agent-") || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		agent := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), "agent-"), ".jsonl")
		if agent == "" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		out = append(out, transcriptFile{agent: agent, size: info.Size()})
	}
	return out
}

// watchTranscripts tells every open registry stream which live transcripts grew, each pass,
// keyed by session and agent: the main transcript and every subagent's beside it. The sizes are
// the watch's own, a transcript no longer listed leaving the map. A pass with no stream open
// stats nothing; a first sighting records a size and fans nothing, so a session the watch meets
// mid-life starts at the size it already is.
func (s *server) watchTranscripts(ctx context.Context) {
	interval := time.Duration(s.o.TranscriptCheckMS) * time.Millisecond
	if interval <= 0 {
		interval = time.Second
	}
	s.health.Register("hub.transcripts", domain.HealthLoop, interval)
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
				// a pass with no stream open still ran; it is what keeps the watch off stale
				s.health.OK("hub.transcripts")
				continue
			}
			live, err := s.registry.LiveTranscripts(ctx)
			if err != nil {
				fmt.Fprintf(s.o.Stderr, "registry: transcript watch: %v\n", err)
				// the fail stands alone: ok and fail can land in the same millisecond, and the
				// state rule reads lastErrAt > lastOkAt
				s.health.Fail("hub.transcripts", err)
				continue
			}
			s.health.OK("hub.transcripts")
			present := map[string]bool{}
			for id, path := range live {
				for _, transcript := range agentTranscripts(path) {
					key := id + "/" + transcript.agent
					present[key] = true
					if seen, ok := sizes[key]; ok && transcript.size > seen {
						s.emitConversationGrew(id, transcript.agent, transcript.size)
					}
					sizes[key] = transcript.size
				}
			}
			for key := range sizes {
				if !present[key] {
					delete(sizes, key)
				}
			}
		}
	}
}
