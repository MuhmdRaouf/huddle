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
// row binds a port or starts a process, and no row spawns a serve: the huddle
// hub serves the boards, and start and open only name its page.

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
	browsers []string
	fetches  []fetchRecord
	answers  map[string]fetchAnswer
	// hubLinks pins the links the fake hub answers for a board key; the "*"
	// entry answers every key. Empty means no hub answers.
	hubLinks map[string]string
	// hubAsked holds every key the hook asked the hub for, in order.
	hubAsked []string
	// ensureBase, ensureOK and ensureMsg are what the fake EnsureHub answers;
	// ensures records every budget it was called with.
	ensureBase string
	ensureOK   bool
	ensureMsg  string
	ensures    []int
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

// ensureHub is the fake EnsureHub: it records the budget and answers what the
// case pinned.
func (s *serverFixture) ensureHub(ctx context.Context, budgetMS int) (string, bool, string) {
	s.ensures = append(s.ensures, budgetMS)
	return s.ensureBase, s.ensureOK, s.ensureMsg
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
		OpenBrowser: func(url string) { s.browsers = append(s.browsers, url) },
		HubBoard:    s.hubBoard,
		EnsureHub:   s.ensureHub,
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

// hubLink is the page a fake hub answers with for this fixture's board.
func (s *serverFixture) hubLink() string {
	return "http://127.0.0.1:42101/#/kanban/" + server.KeyOf(s.boardAbs)
}

// ensuredLink is the page the fake EnsureHub's base names for this board.
func (s *serverFixture) ensuredLink() string {
	return s.ensureBase + "/#/kanban/" + server.KeyOf(s.boardAbs)
}

func TestServerCommands(t *testing.T) {
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
			name: "start names the hub's page and starts nothing",
			prepare: func(t *testing.T, s *serverFixture) {
				s.hubLinks["*"] = s.hubLink()
			},
			argv:   []string{"start"},
			code:   0,
			outHas: []string{"kanban: board at "},
			check: func(t *testing.T, s *serverFixture, stdout string) {
				if !strings.Contains(stdout, "kanban: board at "+s.hubLink()+" (") {
					t.Errorf("stdout = %q, want the hub page", stdout)
				}
				if len(s.browsers) != 1 || s.browsers[0] != s.hubLink() {
					t.Errorf("browsers = %v, want the hub page once", s.browsers)
				}
				if len(s.ensures) != 0 {
					t.Errorf("EnsureHub ran %d times, want none: the hub already answers", len(s.ensures))
				}
				if len(s.fetches) != 0 {
					t.Errorf("fetches = %v, want none: start talks to no server", s.fetches)
				}
			},
		},
		{
			name: "start with no hub starts the hub and names its page",
			prepare: func(t *testing.T, s *serverFixture) {
				s.ensureBase, s.ensureOK = "http://127.0.0.1:41860", true
			},
			argv:   []string{"start"},
			code:   0,
			outHas: []string{"kanban: board at "},
			check: func(t *testing.T, s *serverFixture, stdout string) {
				if !strings.Contains(stdout, "kanban: board at "+s.ensuredLink()+" (") {
					t.Errorf("stdout = %q, want the started hub's page", stdout)
				}
				if len(s.ensures) != 1 || s.ensures[0] != 35_000 {
					t.Fatalf("EnsureHub budgets = %v, want [35000]", s.ensures)
				}
				if len(s.browsers) != 1 || s.browsers[0] != s.ensuredLink() {
					t.Errorf("browsers = %v, want the started hub's page once", s.browsers)
				}
			},
		},
		{
			name: "start with no hub to start is exit 6 with the message on stderr",
			prepare: func(t *testing.T, s *serverFixture) {
				s.ensureMsg = "the huddle hub is not set up; run /huddle:setup"
			},
			argv:   []string{"start"},
			code:   6,
			errHas: []string{"kanban: the huddle hub is not set up; run /huddle:setup"},
			check: func(t *testing.T, s *serverFixture, _ string) {
				if len(s.browsers) != 0 {
					t.Errorf("browsers = %v, want none", s.browsers)
				}
			},
		},
		{
			name: "start --no-open never opens the browser",
			prepare: func(t *testing.T, s *serverFixture) {
				s.hubLinks["*"] = s.hubLink()
			},
			argv:   []string{"start", "--no-open"},
			code:   0,
			outHas: []string{"kanban: board at "},
			check: func(t *testing.T, s *serverFixture, _ string) {
				if len(s.browsers) != 0 {
					t.Errorf("browsers = %v, want none", s.browsers)
				}
			},
		},
		{
			name: "start honours KANBAN_NO_OPEN",
			prepare: func(t *testing.T, s *serverFixture) {
				s.hubLinks["*"] = s.hubLink()
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
			name:    "stop names the hub that serves the boards and stops nothing",
			prepare: func(t *testing.T, s *serverFixture) { s.recorded() },
			argv:    []string{"stop"},
			code:    0,
			outHas:  []string{"kanban: boards are served by the huddle hub; /huddle:stop stops it"},
			check: func(t *testing.T, s *serverFixture, _ string) {
				if len(s.fetches) != 0 {
					t.Errorf("fetches = %v, want none: no shutdown is posted", s.fetches)
				}
			},
		},
		{
			name: "open names the hub's page and opens the browser",
			prepare: func(t *testing.T, s *serverFixture) {
				s.hubLinks["*"] = s.hubLink()
			},
			argv:   []string{"open"},
			code:   0,
			outHas: []string{"kanban: board at "},
			check: func(t *testing.T, s *serverFixture, _ string) {
				if len(s.browsers) != 1 || s.browsers[0] != s.hubLink() {
					t.Errorf("browsers = %v, want the hub page once", s.browsers)
				}
			},
		},
		{
			name: "open with no hub starts the hub and opens its page",
			prepare: func(t *testing.T, s *serverFixture) {
				s.ensureBase, s.ensureOK = "http://127.0.0.1:41860", true
			},
			argv:   []string{"open"},
			code:   0,
			outHas: []string{"kanban: board at "},
			check: func(t *testing.T, s *serverFixture, _ string) {
				if len(s.browsers) != 1 || s.browsers[0] != s.ensuredLink() {
					t.Errorf("browsers = %v, want the started hub's page once", s.browsers)
				}
			},
		},
		{
			name: "open with no hub to start is exit 6 with the message on stderr",
			prepare: func(t *testing.T, s *serverFixture) {
				s.ensureMsg = "the huddle hub is not set up; run /huddle:setup"
			},
			argv:   []string{"open"},
			code:   6,
			errHas: []string{"kanban: the huddle hub is not set up; run /huddle:setup"},
		},
		{
			name: "refresh says the hub watches the board while a hub answers",
			prepare: func(t *testing.T, s *serverFixture) {
				s.hubLinks["*"] = s.hubLink()
			},
			argv:   []string{"refresh"},
			code:   0,
			outHas: []string{"kanban: the hub watches the board; nothing to refresh"},
			check: func(t *testing.T, s *serverFixture, _ string) {
				if len(s.fetches) != 0 {
					t.Errorf("fetches = %v, want none: the hub needs no refresh", s.fetches)
				}
			},
		},
		{
			name: "refresh answers the rev through a server someone runs by hand",
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
			errHas: []string{"kanban: no server for ", "run kanban serve"},
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
