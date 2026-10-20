package cli_test

// One server: the daemon. The scenarios drive radar's and kanban's own words
// with the real EnsureHub wired in — the one way every satellite word reaches
// the hub — and prove no word or hook of theirs starts a server of its own.

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hubaddr"
	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	kanbancli "github.com/MuhmdRaouf/huddle/internal/kanban/cli"
	radarcli "github.com/MuhmdRaouf/huddle/internal/radar/cli"
	radarhook "github.com/MuhmdRaouf/huddle/internal/radar/hook"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// world is one throwaway machine: a temp user home, a temp huddle home and a
// temp radar state dir. setUp lays the huddle home out (set up), pointAt
// records a hub's port in it the way a started hub does.
type world struct {
	user   string
	huddle string
	radar  string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	user := t.TempDir()
	return &world{
		user:   user,
		huddle: filepath.Join(user, "huddle-home"),
		radar:  filepath.Join(user, "radar"),
	}
}

// setUp creates the home the way /huddle:setup does, so EnsureHub may start
// its hub.
func (w *world) setUp(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(w.huddle, 0o700); err != nil {
		t.Fatal(err)
	}
}

// getenv answers the machine's variables, every one pointing inside the world.
func (w *world) getenv(key string) string {
	switch key {
	case "HOME":
		return w.user
	case "HUDDLE_HOME":
		return w.huddle
	case "RADAR_HOME":
		return w.radar
	case "XDG_CONFIG_HOME":
		return filepath.Join(w.user, ".config")
	case "XDG_STATE_HOME":
		return filepath.Join(w.user, ".local", "state")
	case "CLAUDE_CONFIG_DIR":
		return filepath.Join(w.user, ".claude")
	}
	return ""
}

// lookup is getenv the two-valued way the kanban world reads.
func (w *world) lookup(key string) (string, bool) {
	value := w.getenv(key)
	return value, value != ""
}

// environ is the world as one hook invocation's child environment.
func (w *world) environ(extra ...string) []string {
	out := make([]string, 0, 8)
	for _, key := range []string{"HOME", "HUDDLE_HOME", "RADAR_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "CLAUDE_CONFIG_DIR"} {
		out = append(out, key+"="+w.getenv(key))
	}
	return append(out, extra...)
}

// pointAt records the hub's port the way a started hub does, so the address
// lookup finds it.
func (w *world) pointAt(t *testing.T, port int) {
	t.Helper()
	h := home.Home{Root: w.huddle}
	if err := os.MkdirAll(h.Run(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.PortFile(), []byte(strconv.Itoa(port)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// ensureHub is the real EnsureHub over this world's environment.
func (w *world) ensureHub(ctx context.Context, budgetMS int) (string, bool, string) {
	return cli.EnsureHub(ctx, &cli.World{Getenv: w.getenv}, budgetMS)
}

// hubBoard answers the board page's address the way kanban's own client does:
// the home's recorded base, probed for health, with the key as the hash.
func (w *world) hubBoard(ctx context.Context, key string) (string, bool) {
	base := hubaddr.Base(w.lookup)
	if base == "" || !hubaddr.Healthy(ctx, base) {
		return "", false
	}
	return base + "/#/kanban/" + key, true
}

// radarDeps builds the radar deps the satellite word runs with: this world's
// hub lookup and EnsureHub, every other effect inert.
func (w *world) radarDeps(out, errOut func(string)) radarcli.Deps {
	env := domain.EnvLookup{}
	for _, key := range []string{"HOME", "HUDDLE_HOME", "RADAR_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "CLAUDE_CONFIG_DIR"} {
		env[key] = w.getenv(key)
	}
	return radarcli.Deps{
		Env:         env,
		Version:     "test",
		Out:         out,
		Err:         errOut,
		FetchJSON:   func(string) radarcli.FetchResult { return radarcli.FetchResult{} },
		Sleep:       func(int64) {},
		ShouldStop:  func() bool { return false },
		IsAlive:     func(int64) bool { return false },
		OpenBrowser: func(string) {},
		Now:         func() int64 { return 0 },
		SinceMs:     86_400_000,
		Hub: func() (string, bool) {
			base := hubaddr.Base(w.lookup)
			if base == "" || !hubaddr.Healthy(context.Background(), base) {
				return "", false
			}
			return base, true
		},
		EnsureHub: func(budgetMS int) (string, bool, string) {
			return w.ensureHub(context.Background(), budgetMS)
		},
	}
}

// healthyHub starts one fake hub answering /health and answers its port. The
// port is httptest's own, outside every range the tests are kept away from.
func healthyHub(t *testing.T) int {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatalf("read the fake hub's port: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse the fake hub's port %q: %v", portText, err)
	}
	return port
}

// initBoard lays a board out in a project of the world and answers the
// project's path.
func initBoard(t *testing.T, w *world) string {
	t.Helper()
	project := filepath.Join(w.user, "project")
	if _, err := board.Init(context.Background(), filepath.Join(project, ".agents", "kanban"), "", time.Now()); err != nil {
		t.Fatalf("init the board: %v", err)
	}
	return project
}

func TestOneServerTheDaemon(t *testing.T) {
	bdd.Feature(t, "one server: the daemon", func(f *bdd.F) {
		f.Scenario("radar start with the hub running names the hub's radar page and starts nothing", func(s *bdd.S) {
			var (
				w     *world
				port  int
				lines []string
				errs  []string
				code  int
			)
			s.Given("a set-up huddle home whose hub answers", func(t *testing.T) {
				w = newWorld(s.T)
				w.setUp(s.T)
				port = healthyHub(s.T)
				w.pointAt(s.T, port)
			})
			s.When("`radar start` runs", func(t *testing.T) {
				deps := w.radarDeps(func(line string) { lines = append(lines, line) }, func(line string) { errs = append(errs, line) })
				code = radarcli.RunCli(context.Background(), []string{"start"}, deps)
			})
			s.Then("it exits 0 naming the hub's radar page", func(t *testing.T) {
				if code != 0 {
					t.Fatalf("radar start = %d, stderr %q", code, strings.Join(errs, "|"))
				}
				want := "radar: in the huddle hub at http://127.0.0.1:" + strconv.Itoa(port) + "/#/radar"
				if strings.Join(lines, "\n") != want {
					t.Errorf("radar start printed %q, want %q", strings.Join(lines, "\n"), want)
				}
			})
			s.And("no dashboard of its own was started", func(t *testing.T) {
				if _, err := os.Stat(filepath.Join(w.radar, "server.json")); !os.IsNotExist(err) {
					t.Errorf("a standalone dashboard left server.json behind (err %v)", err)
				}
			})
		})

		f.Scenario("radar start without a huddle home says to run /huddle:setup", func(s *bdd.S) {
			var (
				w    *world
				errs []string
				code int
				out  bytes.Buffer
			)
			s.Given("a machine whose huddle home was never set up", func(t *testing.T) {
				w = newWorld(s.T) // the home's directory is never created
			})
			s.When("`radar start` runs", func(t *testing.T) {
				deps := w.radarDeps(func(line string) { out.WriteString(line + "\n") }, func(line string) { errs = append(errs, line) })
				code = radarcli.RunCli(context.Background(), []string{"start"}, deps)
			})
			s.Then("it exits 6 and tells the user to run /huddle:setup", func(t *testing.T) {
				if code != 6 {
					t.Fatalf("radar start = %d, want 6", code)
				}
				if strings.Join(errs, "\n") != "radar: the huddle hub is not set up; run /huddle:setup" {
					t.Errorf("stderr = %q, want the setup line", strings.Join(errs, "\n"))
				}
				if out.String() != "" {
					t.Errorf("stdout = %q, want silence", out.String())
				}
			})
		})

		f.Scenario("kanban start with the hub running names the board's page in the hub", func(s *bdd.S) {
			var (
				w      *world
				port   int
				out    bytes.Buffer
				errOut bytes.Buffer
				code   int
			)
			s.Given("a board in a project and a hub answering for a set-up home", func(t *testing.T) {
				w = newWorld(s.T)
				w.setUp(s.T)
				port = healthyHub(s.T)
				w.pointAt(s.T, port)
				initBoard(s.T, w)
			})
			s.When("`kanban start` runs in the project", func(t *testing.T) {
				world := kanbancli.World{
					Getenv:      w.lookup,
					Cwd:         func() string { return filepath.Join(w.user, "project") },
					Now:         time.Now,
					EnsureHub:   w.ensureHub,
					HubBoard:    w.hubBoard,
					OpenBrowser: func(string) {},
				}
				code = kanbancli.Run(context.Background(), world, []string{"start", "--no-open"}, &out, &errOut)
			})
			s.Then("it exits 0 naming the board's page in the hub", func(t *testing.T) {
				if code != 0 {
					t.Fatalf("kanban start = %d, stderr %q", code, errOut.String())
				}
				want := "kanban: board at http://127.0.0.1:" + strconv.Itoa(port) + "/#/kanban/"
				if !strings.HasPrefix(out.String(), want) {
					t.Errorf("kanban start printed %q, want it to start with %q", out.String(), want)
				}
			})
			s.And("no board server was started", func(t *testing.T) {
				entries, err := filepath.Glob(filepath.Join(w.user, ".local", "state", "kanban", "*"))
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 0 {
					t.Errorf("the kanban state dir holds %v, want it empty", entries)
				}
			})
		})

		f.Scenario("a session start without a hub starts no radar dashboard", func(s *bdd.S) {
			var (
				w      *world
				stdout bytes.Buffer
				code   int
			)
			s.Given("a set-up home with no hub running and autostart wanted", func(t *testing.T) {
				w = newWorld(s.T)
				w.setUp(s.T)
			})
			s.When("a SessionStart hook runs", func(t *testing.T) {
				environ := w.environ("RADAR_AUTOSTART=1")
				code = radarhook.Main(context.Background(), strings.NewReader(`{"hook_event_name":"SessionStart"}`),
					&stdout, environ, time.Now, 1, 2)
			})
			s.Then("it exits 0 and says nothing", func(t *testing.T) {
				if code != 0 {
					t.Fatalf("hook = %d", code)
				}
				if stdout.String() != "" {
					t.Errorf("hook printed %q, want silence", stdout.String())
				}
			})
			s.And("no dashboard was started", func(t *testing.T) {
				if _, err := os.Stat(filepath.Join(w.radar, "server.json")); !os.IsNotExist(err) {
					t.Errorf("a standalone dashboard left server.json behind (err %v)", err)
				}
			})
		})
	})
}
