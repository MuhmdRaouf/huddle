package server

import (
	"context"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/adapters/keystore"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/radar/ledger"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
)

// The owner's read of one request's bodies in the router's own store: GET /api/bodies/<id>.
// The hub only reads the store, never creates it — a read with no store file, no stored key
// or an unreadable key answers "off" and leaves nothing behind, so a browser look never
// turns bodies on.

// bodiesID is the request id a read accepts: one to 128 characters of letters, digits, dot,
// underscore, colon or dash. Anything else never reaches the store — nor do "." and "..",
// the two names a URL's path folds.
var bodiesID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// bodiesAnswer is one read's answer: state is kept (both sides' bodies follow), missing
// (the store holds no body for the id) or off (the store cannot be read).
type bodiesAnswer struct {
	State  string `json:"state"`
	Input  string `json:"input,omitempty"`
	Output string `json:"output,omitempty"`
}

// bodiesRoute answers the owner's read of one request's bodies. Every failure to read the
// store is "off" — the store belongs to the router, and the hub's only move is to read it.
func (s *server) bodiesRoute(w http.ResponseWriter, r *http.Request, me auth.Who, id string) error {
	if !(me.Root || me.Browser != "") {
		return &channel.HuddleError{Status: http.StatusForbidden, Msg: "only the owner reads request bodies"}
	}
	if !bodiesID.MatchString(id) || id == "." || id == ".." {
		return &channel.HuddleError{Status: http.StatusBadRequest,
			Msg: "a request id is 1 to 128 characters of letters, digits, dot, underscore, colon or dash"}
	}
	store, err := s.bodiesStore(r.Context())
	if err != nil {
		return err
	}
	answer := bodiesAnswer{State: "off"}
	if store != nil {
		input, inOK, err := store.Body(r.Context(), id, "input")
		if err != nil {
			return err
		}
		output, outOK, err := store.Body(r.Context(), id, "output")
		if err != nil {
			return err
		}
		answer = bodiesAnswer{State: "missing"}
		if inOK || outOK {
			answer = bodiesAnswer{State: "kept", Input: string(input), Output: string(output)}
		}
	}
	s.writeJSON(w, http.StatusOK, answer, nil)
	return nil
}

// bodiesStatsAnswer is the switch page's read of the whole store: how many requests it
// keeps bodies for and how much disk it holds. state is kept (the numbers follow) or off
// (the store cannot be read, the numbers are zero).
type bodiesStatsAnswer struct {
	State    string `json:"state"`
	Requests int    `json:"requests"`
	Bytes    int64  `json:"bytes"`
}

// bodiesStatsRoute answers the owner's read of the store's totals. The same rule as the
// per-request read: every failure to read the store is "off".
func (s *server) bodiesStatsRoute(w http.ResponseWriter, r *http.Request, me auth.Who) error {
	if !(me.Root || me.Browser != "") {
		return &channel.HuddleError{Status: http.StatusForbidden, Msg: "only the owner reads request bodies"}
	}
	answer := bodiesStatsAnswer{State: "off"}
	store, err := s.bodiesStore(r.Context())
	if err != nil {
		return err
	}
	if store != nil {
		stats, err := store.Stats(r.Context())
		if err != nil {
			return err
		}
		answer = bodiesStatsAnswer{State: "kept", Requests: stats.Requests, Bytes: stats.DiskBytes}
	}
	s.writeJSON(w, http.StatusOK, answer, nil)
	return nil
}

// bodiesStore answers the body store opened under the machine's body key, or nil while it
// cannot be read: no store file, no key store, no stored key, a key that does not open the
// store. A store opened once stays open until the server stops; a nil answer opens nothing
// and the next read tries again.
func (s *server) bodiesStore(ctx context.Context) (*ledger.BodyStore, error) {
	s.bodiesMu.Lock()
	defer s.bodiesMu.Unlock()
	if s.bodies != nil {
		return s.bodies, nil
	}
	env := bodiesEnv(s.o.Environ)
	file := paths.RouterBodiesPath(ctx, env)
	if _, err := os.Stat(file); err != nil {
		return nil, nil // no store yet: off, and never created by a read
	}
	key := s.o.BodyKey
	if key == nil {
		key = keystore.BodyKey(env)
	}
	if key == nil {
		return nil, nil
	}
	bodyKey, ok, err := ledger.ReadBodyKey(ctx, key)
	if err != nil || !ok {
		return nil, nil
	}
	store, err := ledger.OpenBodies(file, bodyKey)
	if err != nil {
		return nil, nil // the store stays unread: off, and the next read tries again
	}
	s.bodies = store
	return store, nil
}

// stopBodies closes the body store the reads opened. Nil (none opened) is a no-op.
func (s *server) stopBodies() {
	s.bodiesMu.Lock()
	defer s.bodiesMu.Unlock()
	if s.bodies != nil {
		_ = s.bodies.Close()
		s.bodies = nil
	}
}

// bodiesEnv reads the server's process environment as the lookup radar's paths resolve from.
func bodiesEnv(environ []string) domain.EnvLookup {
	env := make(domain.EnvLookup, len(environ))
	for _, entry := range environ {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}
	return env
}
