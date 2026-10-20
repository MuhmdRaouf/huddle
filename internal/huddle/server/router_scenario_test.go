package server

// The full edition: the hub hosts huddle-router in its own process, unless a router-only daemon
// already holds the router lock — in which case one line says so and the hub serves alone.

import (
	"context"
	"fmt"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/routerd"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// hubLog is one hub run's stderr, read while it serves.
type hubLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *hubLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, strings.TrimSuffix(string(p), "\n"))
	return len(p), nil
}

func (l *hubLog) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// startAHubWithRouterEnv runs one Serve over home with the environment given (the router's
// HUDDLE_ROUTER_* variables ride in it), and waits for /health. The server stops with the test.
func startAHubWithRouterEnv(scenario *testing.T, home string, environ []string) (base string, stderr *hubLog, err error) {
	scenario.Helper()
	port := ports.Free(scenario)
	base = fmt.Sprintf("http://127.0.0.1:%d", port)
	stderr = &hubLog{}
	stdout := &hubLog{}
	o := Options{
		Version: "0.0.0", Home: home, Data: home, Token: "scenario-root",
		Port: port, PortNamed: true, Stdout: stdout, Stderr: stderr,
		Environ: environ,
	}
	ctx, cancel := context.WithCancel(context.Background())
	scenario.Cleanup(cancel)
	go func() { _ = Serve(ctx, o) }()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if answers(base) {
			return base, stderr, nil
		}
		if time.Now().After(deadline) {
			cancel()
			return "", nil, fmt.Errorf("the hub never came up at %s", base)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// routerEnviron lays a temp router home out and answers the environment both editions read:
// the hub's process env for its router goroutine, and the daemon's EnvLookup.
func routerEnviron(scenario *testing.T) (environ []string, env domain.EnvLookup, routerHome string, routerPort int) {
	scenario.Helper()
	root := scenario.TempDir()
	routerHome = filepath.Join(root, "router-home")
	routerPort = ports.Free(scenario)
	env = domain.EnvLookup{
		"HOME":               root,
		"HUDDLE_ROUTER_HOME": routerHome,
		"RADAR_HOME":         filepath.Join(root, "radar"),
		"HUDDLE_ROUTER_PORT": strconv.Itoa(routerPort),
	}
	for key, value := range env {
		environ = append(environ, key+"="+value)
	}
	return environ, env, routerHome, routerPort
}

// routerHealth reads the huddle-router health answer at port; ok is false while it answers
// nothing.
func routerHealth(t *testing.T, port int) (string, bool) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, router.MultiHealthPath))
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", false
	}
	raw := make([]byte, 1024)
	n, _ := resp.Body.Read(raw)
	return string(raw[:n]), true
}

func TestTheHubHostsHuddleRouter(t *testing.T) {
	bdd.Feature(t, "the hub hosts huddle-router", func(f *bdd.F) {
		f.Scenario("the hub serves huddle-router when no other process does", func(s *bdd.S) {
			var (
				hub        string
				env        domain.EnvLookup
				routerPort int
			)
			var hubErr *hubLog
			s.Given("zai is enabled in a temp router home", func(t *testing.T) {
				environ, given, home, port := routerEnviron(s.T)
				env, routerPort = given, port
				if err := router.SetEnabled(env, "zai", true); err != nil {
					t.Fatal(err)
				}
				var err error
				hub, hubErr, err = startAHubWithRouterEnv(s.T, home, environ)
				if err != nil {
					t.Fatalf("start the hub: %v", err)
				}
			})
			s.Then("the hub answers", func(t *testing.T) {
				if !answers(hub) {
					t.Fatalf("the hub does not answer at %s", hub)
				}
			})
			s.And("huddle-router answers beside it", func(t *testing.T) {
				body, ok := awaitRouterHealth(t, routerPort, "zai")
				if !ok {
					t.Fatalf("huddle-router never answered its health path; the hub's stderr:\n%s", hubErr.joined())
				}
				if !strings.Contains(body, `"zai"`) {
					t.Errorf("huddle-router serves %q, want zai", body)
				}
			})
		})

		f.Scenario("the hub steps aside when a router-only daemon holds the router lock", func(s *bdd.S) {
			var (
				hub        string
				hubErr     *hubLog
				environ    []string
				env        domain.EnvLookup
				routerPort int
				daemonDone chan error
				daemonLogs *hubLog
			)
			s.Given("a router-only daemon holds the router lock and serves the port", func(t *testing.T) {
				environ, env, _, routerPort = routerEnviron(s.T)
				if err := router.SetEnabled(env, "zai", true); err != nil {
					t.Fatal(err)
				}
				daemonLogs = &hubLog{}
				ctx, cancel := context.WithCancel(context.Background())
				daemonDone = make(chan error, 1)
				// The scenario's t: the daemon outlives this step.
				s.T.Cleanup(func() {
					cancel()
					select {
					case <-daemonDone:
					case <-time.After(10 * time.Second):
						t.Error("the daemon never stopped")
					}
				})
				go func() {
					daemonDone <- routerd.Run(ctx, routerd.Options{
						Env: env, Port: routerPort, Log: func(line string) { daemonLogs.Write([]byte(line + "\n")) },
					})
				}()
				if _, ok := awaitRouterHealth(t, routerPort, "zai"); !ok {
					t.Fatal("the daemon never came up")
				}
			})
			s.When("a hub starts with the same router home", func(t *testing.T) {
				var err error
				hub, hubErr, err = startAHubWithRouterEnv(s.T, s.T.TempDir(), environ)
				if err != nil {
					t.Fatalf("start the hub: %v", err)
				}
			})
			s.Then("the hub serves", func(t *testing.T) {
				if !answers(hub) {
					t.Fatalf("the hub does not answer at %s", hub)
				}
			})
			s.And("one line says the daemon has the router", func(t *testing.T) {
				deadline := time.Now().Add(5 * time.Second)
				want := fmt.Sprintf("huddle-router: served by pid %d", os.Getpid())
				for time.Now().Before(deadline) {
					if strings.Contains(hubErr.joined(), want) {
						return
					}
					time.Sleep(20 * time.Millisecond)
				}
				t.Errorf("the hub's stderr = %q, want %q", hubErr.joined(), want)
			})
			s.And("the daemon keeps serving the router port", func(t *testing.T) {
				if _, ok := awaitRouterHealth(t, routerPort, "zai"); !ok {
					t.Fatal("the daemon stopped answering")
				}
			})
		})
	})
}

// awaitRouterHealth waits for the router health answer naming want.
func awaitRouterHealth(t *testing.T, port int, want string) (string, bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if body, ok := routerHealth(t, port); ok && strings.Contains(body, `"`+want+`"`) {
			return body, true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return "", false
}
