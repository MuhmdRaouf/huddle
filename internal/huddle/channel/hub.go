// Every channel, each its own SQLite file under the hub's directory, opened on first use and kept
// open. The server-wide knowledge (the shared store) opens with the first channel and serves them
// all; a channel works without it when its file cannot be opened.
package channel

import (
	"context"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/outside"
	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
)

const channelReSrc = `^[a-z0-9][a-z0-9-]{0,39}$`

// ChannelRe is a channel's name: a lowercase letter or digit, then lowercase letters, digits and
// dashes, 40 characters at most.
var ChannelRe = regexp.MustCompile(channelReSrc)

// HubSession is one live session of a listed channel.
type HubSession struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Stale   bool   `json:"stale"`
	Control string `json:"control"`
}

// HubEntry is one channel of the hub's list.
type HubEntry struct {
	Name        string       `json:"name"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Profile     *string      `json:"profile"`
	Turn        *string      `json:"turn"`
	Sessions    []HubSession `json:"sessions"`
	Stats       Stats        `json:"stats"`
}

// HealthReport takes a store's open result: OK after a good open, Fail after a failed one.
type HealthReport interface {
	OK(name string)
	Fail(name string, err error)
}

// Hub is every channel of one server. AutoCreate says a session's join may create the channel it
// names; Radar answers the digest's estimated costs.
type Hub struct {
	dir        string
	autoCreate bool
	Radar      *outside.Radar

	// Liveness answers every channel's who-is-alive question from outside the channel files;
	// nil leaves each channel its own last_seen age.
	Liveness Liveness

	// MemberSessions answers every channel's Claude session ids from the registry; nil leaves
	// each channel's member rows the only answer.
	MemberSessions Sessions

	// Health takes each channel store's and the knowledge store's open result; nil reports
	// nothing.
	Health HealthReport

	onGet func(*Channel)

	mu     sync.Mutex
	open   map[string]*Channel
	shared *Shared
}

// NewHub opens the hub over dir, creating its channels directory.
func NewHub(dir string, autoCreate bool) *Hub {
	h := &Hub{
		dir: dir, autoCreate: autoCreate, Radar: outside.NewRadar(),
		open: map[string]*Channel{},
	}
	_ = os.MkdirAll(filepath.Join(dir, "channels"), 0o700)
	return h
}

// Where answers the directory the channels live in, for logs.
func (h *Hub) Where() string { return h.dir }

// File answers one channel's database file.
func (h *Hub) File(name string) string {
	return filepath.Join(h.dir, "channels", name+".db")
}

// Remove takes a channel off the hub: the open channel closes with its WAL checkpointed first,
// the file moves to channels/.removed/<name>-<UTC stamp>.db (a mistake is undone by moving it
// back), and the name leaves the cache. The hub's lock is held throughout, so no Get reopens
// the file midway. A channel without a file answers a not-found error.
func (h *Hub) Remove(ctx context.Context, name string, now time.Time) (string, error) {
	if !ChannelRe.MatchString(name) {
		return "", httpErr(400, "channel name must match /"+channelReSrc+"/")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := os.Stat(h.File(name)); err != nil {
		return "", httpErr(404, fmt.Sprintf("no channel %s", name))
	}
	if ch, ok := h.open[name]; ok {
		if err := ch.checkpoint(ctx); err != nil {
			return "", err
		}
		if err := ch.Close(ctx); err != nil {
			return "", err
		}
		delete(h.open, name)
	}
	kept := filepath.Join(h.dir, "channels", ".removed",
		fmt.Sprintf("%s-%s.db", name, now.UTC().Format("20060102T150405Z")))
	if err := os.MkdirAll(filepath.Dir(kept), 0o700); err != nil {
		return "", err
	}
	if err := os.Rename(h.File(name), kept); err != nil {
		return "", err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(h.File(name) + suffix)
	}
	return kept, nil
}

// Exists answers whether the channel is open or has a file.
func (h *Hub) Exists(ctx context.Context, name string) (bool, error) {
	h.mu.Lock()
	_, open := h.open[name]
	h.mu.Unlock()
	if open {
		return true, nil
	}
	_, err := os.Stat(h.File(name))
	return err == nil, nil
}

// Names lists the channel files that carry a channel's name, sorted.
func (h *Hub) Names(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(h.dir, "channels"))
	if err != nil {
		return []string{}, nil
	}
	var out []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".db")
		if ChannelRe.MatchString(name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out, nil
}

// OnGet registers fn on the hub: every channel Get and GetExisting answer goes through it,
// once per answer, before the caller sees the channel.
func (h *Hub) OnGet(fn func(*Channel)) { h.onGet = fn }

// Get opens (or answers the open) channel, creating it when the hub may.
func (h *Hub) Get(ctx context.Context, name string) (*Channel, error) {
	ch, err := h.get(ctx, name, h.autoCreate)
	h.got(ch, err)
	return ch, err
}

// GetExisting opens a channel that already exists, never creating one.
func (h *Hub) GetExisting(ctx context.Context, name string) (*Channel, error) {
	ch, err := h.get(ctx, name, false)
	h.got(ch, err)
	return ch, err
}

func (h *Hub) got(ch *Channel, err error) {
	if err == nil && ch != nil && h.onGet != nil {
		h.onGet(ch)
	}
}

func (h *Hub) get(ctx context.Context, name string, create bool) (*Channel, error) {
	if !ChannelRe.MatchString(name) {
		return nil, httpErr(400, "channel name must match /"+channelReSrc+"/")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.open[name]; ok {
		return c, nil
	}
	if !create {
		if _, err := os.Stat(h.File(name)); err != nil {
			return nil, httpErr(404, fmt.Sprintf("no channel %s", name))
		}
	}
	st, err := store.Open(ctx, h.File(name))
	if err != nil {
		h.report("channel:"+name, err)
		return nil, err
	}
	ch, err := Open(ctx, name, st)
	if err != nil {
		_ = st.Close()
		h.report("channel:"+name, err)
		return nil, err
	}
	h.report("channel:"+name, nil)
	if h.shared == nil {
		// the store's earlier spelling (shared.db) moves onto knowledge.db once, here, because
		// this open is the file's only reader and writer
		s, err := OpenShared(ctx, home.KnowledgeFile(h.dir))
		if err != nil {
			// a channel works without the server-wide knowledge; the next open tries again
			log.Printf("shared knowledge: %v", err)
			h.report("knowledge.db", err)
		} else {
			h.shared = s
			h.report("knowledge.db", nil)
		}
	}
	if h.shared != nil {
		AttachShared(ch, h.shared)
	}
	if h.Radar != nil {
		ch.Radar = h.Radar
	}
	ch.Liveness = h.Liveness
	ch.MemberSessions = h.MemberSessions
	h.open[name] = ch
	return ch, nil
}

// report hands one open result to Health: a nil err is a good open.
func (h *Hub) report(name string, err error) {
	if h.Health == nil {
		return
	}
	if err != nil {
		h.Health.Fail(name, err)
		return
	}
	h.Health.OK(name)
}

// List answers every channel with its configuration, its live sessions and its stats.
func (h *Hub) List(ctx context.Context) ([]HubEntry, error) {
	names, err := h.Names(ctx)
	if err != nil {
		return nil, err
	}
	out := []HubEntry{}
	for _, n := range names {
		c, err := h.GetExisting(ctx, n)
		if err != nil {
			return nil, err
		}
		cfg := c.Config()
		turn, err := c.Turn(ctx)
		if err != nil {
			return nil, err
		}
		sessions, err := c.Sessions(ctx)
		if err != nil {
			return nil, err
		}
		stats, err := c.Stats(ctx)
		if err != nil {
			return nil, err
		}
		live := []HubSession{}
		for _, s := range sessions {
			if s.State == "left" {
				continue
			}
			live = append(live, HubSession{
				Name: s.Name, State: s.State, Stale: s.Stale, Control: s.Control,
			})
		}
		out = append(out, HubEntry{
			Name: n, Title: cfg.Title, Description: cfg.Description, Profile: cfg.Profile,
			Turn: turn.Holder, Sessions: live, Stats: stats,
		})
	}
	return out, nil
}

// Close closes every open channel and the shared knowledge store; the hub opens again after it.
func (h *Hub) Close(ctx context.Context) error {
	h.mu.Lock()
	chans := h.open
	h.open = map[string]*Channel{}
	shared := h.shared
	h.shared = nil
	h.mu.Unlock()
	var firstErr error
	for _, name := range slices.Sorted(maps.Keys(chans)) {
		if err := chans[name].Close(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if shared != nil {
		if err := shared.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
