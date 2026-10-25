package routerd

// The huddle-router daemon's behaviour: one process serves one router for every enabled
// provider, follows providers.json without a restart, and never lets a second daemon onto the
// port. Written as scenarios over the rig below.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/singleton"
	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// daemonSeen is one request a fake upstream saw.
type daemonSeen struct {
	path string
	auth string
	body string
}

// daemonUpstream is one recording fake upstream: it answers every POST with a two-event stream
// named after itself.
type daemonUpstream struct {
	server *httptest.Server
	mu     sync.Mutex
	seen   []daemonSeen
}

// newDaemonUpstream starts one recording fake upstream.
func newDaemonUpstream(t *testing.T, name string) *daemonUpstream {
	t.Helper()
	up := &daemonUpstream{}
	up.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		up.mu.Lock()
		up.seen = append(up.seen, daemonSeen{path: r.URL.Path, auth: r.Header.Get("Authorization"), body: string(body)})
		up.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+name+"-1\n\n")
		_, _ = io.WriteString(w, "data: "+name+"-2\n\n")
	}))
	t.Cleanup(up.server.Close)
	return up
}

func (u *daemonUpstream) requests() []daemonSeen {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]daemonSeen(nil), u.seen...)
}

// daemonRig is one daemon Run over a temp home, with a fake upstream per lane and one for
// Anthropic. The Run itself is a goroutine of the test process; Cleanup stops it. The cancel and
// done pair is guarded: a restarted Run (the watchdog scenarios') replaces it from another
// goroutine. The home is a short one: the daemon's handover socket is a unix socket under
// run/, and macOS caps a socket path at 104 bytes.
type daemonRig struct {
	t         *testing.T
	env       domain.EnvLookup
	home      string
	port      int
	logs      *daemonLog
	zai       *daemonUpstream
	kimi      *daemonUpstream
	anthropic *daemonUpstream
	mu        sync.Mutex
	cancel    context.CancelFunc
	done      <-chan error
	// panicOnFirst makes the daemon's own handler panic once, to prove the recovery.
	panicOnFirst bool
	// takeover starts the Run with Takeover set: a first start with nobody to take from
	// serves normally.
	takeover bool
	// hosted starts the Run as the hub's own: the memory handover is off.
	hosted bool
	// rssCeiling is the Run's own ceiling; 0 takes the environment's, else the default.
	rssCeiling int64
	// returned says the Run ended by itself (a handover's drain), so stop only cancels.
	returned bool
}

// daemonLog records the daemon's lines.
type daemonLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *daemonLog) add(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
}

func (l *daemonLog) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// newDaemonRig lays the temp home out and answers the env the daemon reads; the caller starts
// the Run itself, so a scenario controls what is enabled and when.
func newDaemonRig(t *testing.T) *daemonRig {
	t.Helper()
	root := t.TempDir()
	rig := &daemonRig{
		t:         t,
		logs:      &daemonLog{},
		zai:       newDaemonUpstream(t, "zai"),
		kimi:      newDaemonUpstream(t, "kimi"),
		anthropic: newDaemonUpstream(t, "anthropic"),
	}
	rig.home = shortHome(t)
	rig.port = ports.Free(t)
	rig.env = domain.EnvLookup{
		"HOME":                        root,
		"HUDDLE_ROUTER_HOME":          rig.home,
		"RADAR_HOME":                  filepath.Join(root, "radar"),
		"HUDDLE_ROUTER_PORT":          strconv.Itoa(rig.port),
		"HUDDLE_ROUTER_ANTHROPIC_URL": rig.anthropic.server.URL,
		"ZAI_ROUTER_URL":              rig.zai.server.URL,
		"KIMI_ROUTER_URL":             rig.kimi.server.URL,
		"ZAI_API_KEY":                 "test-key",
		"KIMI_API_KEY":                "kimi-key",
	}
	t.Cleanup(rig.stop)
	return rig
}

// start runs the daemon until the test ends, and fails when it stops on its own.
func (r *daemonRig) start() {
	r.t.Helper()
	options := Options{Env: r.env, Port: r.port, Version: "test", Log: r.logs.add, Poll: 30 * time.Millisecond, Takeover: r.takeover, Hosted: r.hosted, RSSCeiling: r.rssCeiling}
	if r.panicOnFirst {
		var calls atomic.Int64
		// The test-only hook the handler runs before serving: its first panic on the data path
		// proves the recovery answers 502, and its silence after that proves the daemon lives
		// on. The health probes never panic.
		options.panicOnce = func(req *http.Request) {
			if req.URL.Path != "/v1/messages" {
				return
			}
			if calls.Add(1) == 1 {
				panic("the lane panicked")
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	r.mu.Lock()
	r.cancel, r.done = cancel, done
	r.mu.Unlock()
	go func() { done <- Run(ctx, options) }()
}

// stop ends the daemon and waits for it; a daemon that never stops fails the test. A daemon
// that ended by itself — a handover's drain — is only cancelled.
func (r *daemonRig) stop() {
	r.mu.Lock()
	cancel, done := r.cancel, r.done
	r.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	if r.returned {
		return
	}
	select {
	case err := <-done:
		if err != nil {
			r.t.Errorf("the daemon stopped with %v", err)
		}
	case <-time.After(10 * time.Second):
		r.t.Error("the daemon never stopped")
	}
}

// healthy says whether the daemon answers its health path, and what it served.
func (r *daemonRig) health(t *testing.T) (string, bool) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d%s", r.port, router.MultiHealthPath))
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 200 {
		return "", false
	}
	return string(body), true
}

// awaitHealth waits until the daemon answers its health path naming every want provider. The
// window is generous: the whole suite runs in parallel, and a goroutine of this process can
// wait seconds for a slice of CPU without anything being wrong.
func (r *daemonRig) awaitHealth(t *testing.T, want ...string) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if body, ok := r.health(t); ok {
			every := true
			for _, name := range want {
				if !strings.Contains(body, `"`+name+`"`) {
					every = false
				}
			}
			if every {
				return body
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the daemon never served %v; its log:\n%s", want, r.logs.joined())
	return ""
}

// postModel sends one model request the way Claude Code does — the caller's own credentials on
// it — and answers the status and body.
func (r *daemonRig) postModel(t *testing.T, model string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/v1/messages", r.port),
		strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[]}`, model)))
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer anthropic-oauth")
	request.Header.Set("X-Api-Key", "anthropic-key")
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Do(request)
	if err != nil {
		t.Fatalf("post %s: %v", model, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the answer: %v", err)
	}
	return resp.StatusCode, string(body)
}

// portFree says whether the daemon's port refuses a connection.
func (r *daemonRig) portFree(t *testing.T) bool {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", r.port), 200*time.Millisecond)
	if err != nil {
		return true
	}
	_ = conn.Close()
	return false
}

func TestHuddleRouterDaemonScenarios(t *testing.T) {
	t.Parallel()
	bdd.Feature(t, "huddle-router in the daemon", func(f *bdd.F) {
		f.Scenario("with zai enabled, a glm request reaches the provider with its key and a claude request reaches Anthropic", func(s *bdd.S) {
			var rig *daemonRig
			s.Given("zai is enabled and its key is in the environment", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				if err := router.SetEnabled(rig.env, "zai", true); err != nil {
					t.Fatal(err)
				}
				rig.start()
			})
			s.When("a glm-5.3 request arrives", func(t *testing.T) {
				rig.awaitHealth(t, "zai")
				if status, body := rig.postModel(t, "glm-5.3"); status != 200 {
					t.Fatalf("glm-5.3 = %d %q", status, body)
				}
			})
			s.Then("the zai upstream answered with zai's key", func(t *testing.T) {
				seen := rig.zai.requests()
				if len(seen) != 1 {
					t.Fatalf("zai saw %d requests", len(seen))
				}
				if seen[0].auth != "Bearer test-key" {
					t.Errorf("authorization = %q", seen[0].auth)
				}
				if !strings.Contains(seen[0].body, "glm-5.3") {
					t.Errorf("the body lost the model: %q", seen[0].body)
				}
			})
			s.And("a claude request reaches Anthropic untouched", func(t *testing.T) {
				if status, body := rig.postModel(t, "claude-opus-5-5"); status != 200 {
					t.Fatalf("claude = %d %q", status, body)
				}
				seen := rig.anthropic.requests()
				if len(seen) != 1 {
					t.Fatalf("Anthropic saw %d requests", len(seen))
				}
				if seen[0].auth != "Bearer anthropic-oauth" {
					t.Errorf("authorization = %q, want the caller's own", seen[0].auth)
				}
				if len(rig.zai.requests()) != 1 {
					t.Errorf("zai saw the claude request")
				}
			})
		})

		f.Scenario("enabling kimi in providers.json adds its lane within one poll, without a restart", func(s *bdd.S) {
			var rig *daemonRig
			s.Given("zai is enabled and the daemon serves it", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				if err := router.SetEnabled(rig.env, "zai", true); err != nil {
					t.Fatal(err)
				}
				rig.start()
				rig.awaitHealth(t, "zai")
			})
			s.When("kimi is enabled in the file", func(t *testing.T) {
				if err := router.SetEnabled(rig.env, "kimi", true); err != nil {
					t.Fatal(err)
				}
			})
			s.Then("the daemon serves both within one poll", func(t *testing.T) {
				rig.awaitHealth(t, "zai", "kimi")
			})
			s.And("a kimi request reaches the kimi upstream", func(t *testing.T) {
				if status, body := rig.postModel(t, "kimi-k2"); status != 200 {
					t.Fatalf("kimi-k2 = %d %q", status, body)
				}
				if seen := rig.kimi.requests(); len(seen) != 1 {
					t.Fatalf("kimi saw %d requests", len(seen))
				}
			})
		})

		f.Scenario("a second Run for the same home returns HeldError and the first keeps serving", func(s *bdd.S) {
			var rig *daemonRig
			s.Given("a daemon serves a temp home", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				if err := router.SetEnabled(rig.env, "zai", true); err != nil {
					t.Fatal(err)
				}
				rig.start()
				rig.awaitHealth(t, "zai")
			})
			s.When("a second Run starts for the same home", func(t *testing.T) {
				err := Run(context.Background(), Options{Env: rig.env, Port: rig.port, Log: rig.logs.add, Poll: 30 * time.Millisecond})
				var held *singleton.HeldError
				if !errors.As(err, &held) {
					t.Fatalf("second Run = %v, want a *HeldError", err)
				}
				if held.PID != os.Getpid() {
					t.Errorf("HeldError pid = %d, want %d", held.PID, os.Getpid())
				}
			})
			s.Then("the first still serves", func(t *testing.T) {
				rig.awaitHealth(t, "zai")
			})
		})

		f.Scenario("with no provider enabled the port stays free until one is enabled", func(s *bdd.S) {
			var rig *daemonRig
			s.Given("a daemon runs with no provider enabled", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				rig.start()
			})
			s.Then("the port stays free across a poll", func(t *testing.T) {
				deadline := time.Now().Add(500 * time.Millisecond)
				for time.Now().Before(deadline) {
					if !rig.portFree(t) {
						t.Fatal("the daemon bound the port with no provider enabled")
					}
					time.Sleep(50 * time.Millisecond)
				}
			})
			s.When("zai is enabled in the file", func(t *testing.T) {
				if err := router.SetEnabled(rig.env, "zai", true); err != nil {
					t.Fatal(err)
				}
			})
			s.Then("the daemon takes the port within one poll", func(t *testing.T) {
				rig.awaitHealth(t, "zai")
			})
		})

		f.Scenario("disabling every provider keeps the port serving", func(s *bdd.S) {
			var rig *daemonRig
			s.Given("a daemon serves zai", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				if err := router.SetEnabled(rig.env, "zai", true); err != nil {
					t.Fatal(err)
				}
				rig.start()
				rig.awaitHealth(t, "zai")
			})
			s.When("providers.json disables every provider", func(t *testing.T) {
				if err := router.SetEnabled(rig.env, "zai", false); err != nil {
					t.Fatal(err)
				}
			})
			s.Then("the daemon still answers after a poll, empty but up", func(t *testing.T) {
				time.Sleep(100 * time.Millisecond)
				rig.awaitHealth(t, "zai")
			})
		})

		f.Scenario("a panic in a lane is answered 502 and the next request is served", func(s *bdd.S) {
			var rig *daemonRig
			s.Given("a daemon whose handler panics on the first request", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				rig.panicOnFirst = true
				if err := router.SetEnabled(rig.env, "zai", true); err != nil {
					t.Fatal(err)
				}
				rig.start()
				rig.awaitHealth(t, "zai")
			})
			var status int
			s.When("the first request arrives", func(t *testing.T) {
				status, _ = rig.postModel(t, "glm-5.3")
			})
			s.Then("it is answered 502 and the daemon lives on", func(t *testing.T) {
				if status != 502 {
					t.Fatalf("the panicking request = %d, want 502", status)
				}
			})
			s.And("the next request is served by the lane", func(t *testing.T) {
				if status, body := rig.postModel(t, "glm-5.3"); status != 200 {
					t.Fatalf("the next request = %d %q", status, body)
				}
				if seen := rig.zai.requests(); len(seen) != 1 {
					t.Errorf("zai saw %d requests, want the second one", len(seen))
				}
			})
		})
	})
}
