package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/server"
)

// The server commands: start, stop, open, refresh and serve — their wording,
// their exit codes and the effects they drive. Every effect is recorded; no
// row binds a port or starts a process.

// fetchRecord is one request the fake client saw.
type fetchRecord struct {
	method, url, token string
}

// fetchAnswer is what the fake client answers one path with.
type fetchAnswer struct {
	status int
	body   string
}

// serverFixture is one board fixture plus a temp KANBAN_HOME and the recorded
// server effects.
type serverFixture struct {
	*fixture
	home     string
	boardAbs string
	port     int
	spawns   [][]string
	browsers []string
	fetches  []fetchRecord
	answers  map[string]fetchAnswer
	state    bool
	// hubLinks pins the links the fake hub answers for a board key; the "*"
	// entry answers every key. Empty means no hub answers.
	hubLinks map[string]string
	// hubAsked holds every key the hook asked the hub for, in order.
	hubAsked []string
}

// hubBoard is the fake HubBoard: it records the key asked and answers the link
// pinned for it. Nothing pinned is no hub.
func (s *serverFixture) hubBoard(ctx context.Context, key string) (string, bool) {
	s.hubAsked = append(s.hubAsked, key)
	if link, ok := s.hubLinks[key]; ok {
		return link, true
	}
	if link, ok := s.hubLinks["*"]; ok {
		return link, true
	}
	return "", false
}

func newServerFixture(t *testing.T) *serverFixture {
	t.Helper()
	f := newFixture(t)
	abs, err := filepath.Abs(f.root)
	if err != nil {
		t.Fatal(err)
	}
	// the board's one spelling: what every command resolves a symlinked path to
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	s := &serverFixture{
		fixture: f, home: t.TempDir(), boardAbs: abs, port: 45678,
		answers: map[string]fetchAnswer{}, hubLinks: map[string]string{},
	}
	f.env = map[string]string{"KANBAN_HOME": s.home}
	s.healthOK(true)
	return s
}

// healthOK makes the recorded server answer health, or not.
func (s *serverFixture) healthOK(ok bool) {
	if !ok {
		delete(s.answers, "/api/health")
		return
	}
	s.answers["/api/health"] = fetchAnswer{
		status: 200,
		body:   `{"ok":true,"board":` + jsonString(s.t, s.boardAbs) + `,"version":"test","pid":1}`,
	}
}

// jsonString encodes one string as JSON, so a path's backslashes stay valid.
func jsonString(t *testing.T, value string) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// recorded writes the state file the way the running server does.
func (s *serverFixture) recorded() {
	s.state = true
	st := server.State{
		Pid: 1, Port: s.port, Board: s.boardAbs,
		Version: "test", Token: "tok", StartedAt: "2026-10-15T12:00:00Z",
	}
	if err := server.WriteState(context.Background(), s.home, server.KeyOf(s.boardAbs), st); err != nil {
		s.t.Fatal(err)
	}
}

// fetch is the recorded client: it answers the paths the fixture pinned and
// records every call.
func (s *serverFixture) fetch(ctx context.Context, method, url string, header map[string]string) (int, []byte, bool) {
	s.fetches = append(s.fetches, fetchRecord{method: method, url: url, token: header["X-Kanban-Token"]})
	for suffix, answer := range s.answers {
		if strings.HasSuffix(url, suffix) {
			return answer.status, []byte(answer.body), true
		}
	}
	return 0, nil, false
}

// world builds the fixture's world with every server effect recorded.
func (s *serverFixture) world() World {
	return World{
		Getenv:      func(key string) (string, bool) { v, ok := s.env[key]; return v, ok },
		Cwd:         func() string { return s.cwd },
		Now:         func() time.Time { return fixedNow },
		ServerFetch: s.fetch,
		SpawnServe: func(command string, args []string, logFile string) int64 {
			s.spawns = append(s.spawns, args)
			if !s.state {
				s.recorded() // the child publishes its state once listening
			}
			return 1
		},
		OpenBrowser: func(url string) { s.browsers = append(s.browsers, url) },
		HubBoard:    s.hubBoard,
		Sleep:       func(int64) {},
		Version:     "test",
	}
}

// run runs one command line against the recorded world.
func (s *serverFixture) run(argv ...string) (int, string, string) {
	s.t.Helper()
	var out, errOut bytes.Buffer
	code := Run(context.Background(), s.world(), argv, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestServerCommands(t *testing.T) {
	line := "kanban: board at http://127.0.0.1:45678 ("
	tests := []struct {
		name    string
		prepare func(t *testing.T, s *serverFixture)
		argv    []string
		code    int
		outHas  []string
		errHas  []string
		check   func(t *testing.T, s *serverFixture, stdout string)
	}{
		{
			name:    "start reuses a healthy server and opens the browser",
			prepare: func(t *testing.T, s *serverFixture) { s.recorded() },
			argv:    []string{"start"},
			code:    0,
			outHas:  []string{line},
			check: func(t *testing.T, s *serverFixture, _ string) {
				if len(s.spawns) != 0 {
					t.Errorf("a healthy server was spawned again: %v", s.spawns)
				}
				if len(s.browsers) != 1 || s.browsers[0] != "http://127.0.0.1:45678" {
					t.Errorf("browsers = %v", s.browsers)
				}
			},
		},
		{
			name: "start spawns serve when nothing answers",
			prepare: func(t *testing.T, s *serverFixture) {
				// no state and no health answer: the spawn alone brings the
				// server up (the fake child publishes the state, then health
				// answers)
			},
			argv:   []string{"start"},
			code:   0,
			outHas: []string{line},
			check: func(t *testing.T, s *serverFixture, _ string) {
				if len(s.spawns) != 1 {
					t.Fatalf("spawns = %v", s.spawns)
				}
				args := s.spawns[0]
				joined := strings.Join(args, " ")
				if !strings.Contains(joined, "serve") || !strings.Contains(joined, s.boardAbs) {
					t.Errorf("spawn args = %q, want serve for the board", args)
				}
			},
		},
		{
			name:    "start --no-open never opens the browser",
			prepare: func(t *testing.T, s *serverFixture) { s.recorded() },
			argv:    []string{"start", "--no-open"},
			code:    0,
			outHas:  []string{line},
			check: func(t *testing.T, s *serverFixture, _ string) {
				if len(s.browsers) != 0 {
					t.Errorf("browsers = %v, want none", s.browsers)
				}
			},
		},
		{
			name: "start honours KANBAN_NO_OPEN",
			prepare: func(t *testing.T, s *serverFixture) {
				s.recorded()
				s.env["KANBAN_NO_OPEN"] = "1"
			},
			argv: []string{"start"},
			code: 0,
			check: func(t *testing.T, s *serverFixture, _ string) {
				if len(s.browsers) != 0 {
					t.Errorf("browsers = %v, want none", s.browsers)
				}
			},
		},
		{
			name:   "start --port garbage is a usage error",
			argv:   []string{"start", "--port", "nope"},
			code:   2,
			errHas: []string{"--port"},
		},
		{
			name: "stop posts the shutdown with the token",
			prepare: func(t *testing.T, s *serverFixture) {
				s.recorded()
				s.answers["/api/shutdown"] = fetchAnswer{status: 200, body: `{}`}
			},
			argv:   []string{"stop"},
			code:   0,
			outHas: []string{"kanban: stopped"},
			check: func(t *testing.T, s *serverFixture, _ string) {
				var stop *fetchRecord
				for i := range s.fetches {
					if s.fetches[i].method == "POST" {
						stop = &s.fetches[i]
					}
				}
				if stop == nil {
					t.Fatal("stop posted nothing")
				}
				if !strings.HasSuffix(stop.url, "/api/shutdown") || stop.token != "tok" {
					t.Errorf("stop posted %s %s with token %q", stop.method, stop.url, stop.token)
				}
			},
		},
		{
			name:   "stop with no server is exit 6",
			argv:   []string{"stop"},
			code:   6,
			errHas: []string{"kanban: no server for "},
			outHas: []string{},
		},
		{
			name:    "open prints the address and opens the browser",
			prepare: func(t *testing.T, s *serverFixture) { s.recorded() },
			argv:    []string{"open"},
			code:    0,
			outHas:  []string{line},
			check: func(t *testing.T, s *serverFixture, _ string) {
				if len(s.browsers) != 1 {
					t.Errorf("browsers = %v", s.browsers)
				}
			},
		},
		{
			name:   "open with no server is exit 6 with the hint",
			argv:   []string{"open"},
			code:   6,
			errHas: []string{"kanban: no server for ", "run kanban start"},
		},
		{
			name: "refresh answers the rev",
			prepare: func(t *testing.T, s *serverFixture) {
				s.recorded()
				s.answers["/api/refresh"] = fetchAnswer{status: 200, body: `{"rev":7}`}
			},
			argv:   []string{"refresh"},
			code:   0,
			outHas: []string{"kanban: refreshed (rev 7)"},
		},
		{
			name:   "refresh with no server is exit 6 with the hint",
			argv:   []string{"refresh"},
			code:   6,
			errHas: []string{"kanban: no server for ", "run kanban start"},
		},
		{
			name:   "serve --port garbage is a usage error",
			argv:   []string{"serve", "--port", "nope"},
			code:   2,
			errHas: []string{"--port"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newServerFixture(t)
			if tt.prepare != nil {
				tt.prepare(t, s)
			}
			code, stdout, stderr := s.run(tt.argv...)
			if code != tt.code {
				t.Errorf("exit = %d, want %d (stdout %q, stderr %q)", code, tt.code, stdout, stderr)
			}
			has(t, "stdout", stdout, tt.outHas...)
			has(t, "stderr", stderr, tt.errHas...)
			if tt.check != nil {
				tt.check(t, s, stdout)
			}
		})
	}
}
