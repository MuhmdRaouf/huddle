package routerd

// The daemon's handover: a new Run takes the listening socket from a running one over the
// unix socket, so the port never closes, while the old Run drains the streams it already
// accepted and then lets the router lock go. Written as scenarios over the daemon rig: the old
// and the new Run are goroutines of the test process, each with its own context.

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/singleton"
	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/radar/ledger"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// slowUpstream is one fake upstream whose answer is a stream that stays open across a
// takeover: the first event leaves at once, the second two seconds later. The channel receives
// one signal when the first event is out, so a scenario knows the stream is running.
func slowUpstream(t *testing.T, started chan<- struct{}) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flushing", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: slow-1\n\n")
		flusher.Flush()
		started <- struct{}{}
		time.Sleep(2 * time.Second)
		_, _ = io.WriteString(w, "data: slow-2\n\n")
		flusher.Flush()
	}))
	t.Cleanup(server.Close)
	return server
}

// slowSetup is how long the slow keystore's read takes: the stalled setup a busy machine
// stands for during a takeover.
const slowSetup = 3 * time.Second

// slowKeystore is a body key source whose read takes slowSetup, and that holds the scenarios'
// key: openBodies waits on it while the taker sets up, the way every step of a setup stretches
// on a busy machine.
type slowKeystore struct {
	*bodiesKeystore
}

func slowKeystoreOf() *slowKeystore {
	return &slowKeystore{bodiesKeystore: bodiesKeystoreOf(bodiesTestKey)}
}

// Get sleeps before it answers, so the taker runs its whole setup while this one read is out.
func (k *slowKeystore) Get(ctx context.Context) (string, bool) {
	time.Sleep(slowSetup)
	return k.bodiesKeystore.Get(ctx)
}

// dialHammer dials the daemon's port every five milliseconds until stop closes, dropping each
// connection at once, and answers how many dials failed: a refusal during a takeover is the
// window where nobody accepts.
func dialHammer(port int, stop <-chan struct{}) <-chan int {
	refused := make(chan int, 1)
	go func() {
		bad := 0
		for {
			select {
			case <-stop:
				refused <- bad
				return
			default:
			}
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
			if err != nil {
				bad++
			} else {
				_ = conn.Close()
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	return refused
}

// startTakeover runs one Run with Takeover set until the test ends, and fails when it stops on
// its own. Its context is its own, like a second process's would be. A non-nil key is the
// taker's body key source, the way a scenario slows the setup down.
func startTakeover(t *testing.T, rig *daemonRig, key ledger.KeySource) <-chan error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Env: rig.env, Log: rig.logs.add, Poll: 30 * time.Millisecond, Takeover: true, BodyKey: key})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("the takeover Run stopped with %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("the takeover Run never stopped")
		}
	})
	return done
}

// awaitReturn waits for the rig's Run to end within ten seconds and fails when the end never
// comes or carries an error. A Run that ended by itself — a handover's drain — spares the
// cleanup's stop the wait.
func awaitReturn(t *testing.T, rig *daemonRig, what string) {
	t.Helper()
	select {
	case err := <-rig.done:
		rig.returned = true
		if err != nil {
			t.Fatalf("%s returned %v", what, err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("%s never returned; its log:\n%s", what, rig.logs.joined())
	}
}

func TestHuddleRouterHandoverScenarios(t *testing.T) {
	t.Parallel()
	bdd.Feature(t, "the router port never closes", func(f *bdd.F) {
		f.Scenario("a takeover keeps a long stream alive and new requests go to the new process", func(s *bdd.S) {
			var rig *daemonRig
			streamed := make(chan string, 1)
			var streamStarted chan struct{}
			s.Given("a daemon serves zai over an upstream that streams for two seconds", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				streamStarted = make(chan struct{}, 1)
				// the scenario's own T registers the server, so it outlives this step
				rig.env["ZAI_ROUTER_URL"] = slowUpstream(s.T, streamStarted).URL
				if err := router.SetEnabled(rig.env, "zai", true); err != nil {
					t.Fatal(err)
				}
				rig.start()
				rig.awaitHealth(t, "zai")
			})
			s.When("a request starts streaming and a takeover serves the same port", func(t *testing.T) {
				go func() {
					status, body := rig.postModel(t, "glm-5.3")
					streamed <- fmt.Sprintf("%d %s", status, body)
				}()
				select {
				case <-streamStarted:
				case <-time.After(5 * time.Second):
					t.Fatal("the stream never started")
				}
				startTakeover(s.T, rig, nil)
				rig.awaitHealth(t, "zai")
			})
			s.Then("the stream the old process held finishes 200", func(t *testing.T) {
				select {
				case line := <-streamed:
					if !strings.HasPrefix(line, "200 ") {
						t.Fatalf("the stream = %q, want a 200 answer", line)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("the stream never finished")
				}
			})
			s.And("the old Run returned nil after its drain", func(t *testing.T) {
				awaitReturn(t, rig, "the old Run")
			})
			s.And("a request after the takeover is answered", func(t *testing.T) {
				if status, body := rig.postModel(t, "glm-5.3"); status != 200 {
					t.Fatalf("after the takeover: %d %q", status, body)
				}
			})
		})

		f.Scenario("a takeover with nobody to take from serves normally", func(s *bdd.S) {
			var rig *daemonRig
			s.Given("a home with zai enabled and no daemon running", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				if err := router.SetEnabled(rig.env, "zai", true); err != nil {
					t.Fatal(err)
				}
			})
			s.When("a Run starts with takeover set", func(t *testing.T) {
				rig.takeover = true
				rig.start()
			})
			s.Then("it serves zai normally", func(t *testing.T) {
				rig.awaitHealth(t, "zai")
			})
			s.And("its log names the daemon it did not find", func(t *testing.T) {
				if joined := rig.logs.joined(); !strings.Contains(joined, "no daemon to take over from") {
					t.Errorf("the log = %q, want the takeover miss", joined)
				}
			})
		})

		f.Scenario("a standalone router over its ceiling hands over", func(s *bdd.S) {
			var rig *daemonRig
			s.Given("a standalone router whose ceiling is one byte", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				if err := router.SetEnabled(rig.env, "zai", true); err != nil {
					t.Fatal(err)
				}
				rig.rssCeiling = 1
				rig.env["HUDDLE_ROUTER_RSS_INTERVAL_MS"] = "50"
			})
			s.When("the first rss checks pass", func(t *testing.T) {
				rig.start()
				rig.awaitHealth(t, "zai")
				time.Sleep(150 * time.Millisecond)
			})
			s.Then("the log names the handover", func(t *testing.T) {
				if joined := rig.logs.joined(); !strings.Contains(joined, "over the ceiling; handing over") {
					t.Errorf("the log = %q, want the handover", joined)
				}
			})
			s.And("the router still serves while the suite refuses its own spawn", func(t *testing.T) {
				rig.awaitHealth(t, "zai")
			})
		})

		f.Scenario("the old process releases the lock after draining and the new one holds it", func(s *bdd.S) {
			var rig *daemonRig
			var newDone <-chan error
			s.Given("a daemon serves zai", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				if err := router.SetEnabled(rig.env, "zai", true); err != nil {
					t.Fatal(err)
				}
				rig.start()
				rig.awaitHealth(t, "zai")
			})
			s.When("a takeover serves the same port", func(t *testing.T) {
				newDone = startTakeover(s.T, rig, nil)
				rig.awaitHealth(t, "zai")
			})
			s.And("the old Run returned nil after its drain", func(t *testing.T) {
				awaitReturn(t, rig, "the old Run")
			})
			s.Then("the router lock is held again", func(t *testing.T) {
				lock := filepath.Join(rig.home, "run", "router.lock")
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					if _, held := singleton.Holder(lock); held {
						return
					}
					time.Sleep(20 * time.Millisecond)
				}
				t.Fatal("nobody holds the router lock after the handover")
			})
			s.And("the new Run still serves", func(t *testing.T) {
				rig.awaitHealth(t, "zai")
				if newDone == nil {
					t.Fatal("no takeover Run was started")
				}
				select {
				case err := <-newDone:
					t.Fatalf("the new Run stopped early with %v", err)
				default:
				}
			})
		})

		f.Scenario("a takeover whose setup is slow refuses no dial", func(s *bdd.S) {
			var rig *daemonRig
			var refused <-chan int
			s.Given("a daemon serves zai", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				if err := router.SetEnabled(rig.env, "zai", true); err != nil {
					t.Fatal(err)
				}
				rig.start()
				rig.awaitHealth(t, "zai")
			})
			s.When("a hammer dials through a takeover whose setup stalls three seconds", func(t *testing.T) {
				stop := make(chan struct{})
				refused = dialHammer(rig.port, stop)
				time.Sleep(50 * time.Millisecond) // the hammer reaches the old daemon first
				startTakeover(s.T, rig, slowKeystoreOf())
				// The old daemon's last answers race the handover: the window begins when health
				// first goes quiet, and ends when the taker serves.
				deadline := time.Now().Add(10 * time.Second)
				for {
					if _, ok := rig.health(t); !ok {
						break
					}
					if time.Now().After(deadline) {
						close(stop)
						t.Fatalf("the old daemon never went quiet; its log:\n%s", rig.logs.joined())
					}
					time.Sleep(20 * time.Millisecond)
				}
				deadline = time.Now().Add(15 * time.Second)
				for {
					if _, ok := rig.health(t); ok {
						break
					}
					if time.Now().After(deadline) {
						close(stop)
						t.Fatalf("the taker never answered health; its log:\n%s", rig.logs.joined())
					}
					time.Sleep(20 * time.Millisecond)
				}
				close(stop)
			})
			s.Then("no dial of the hammer was refused", func(t *testing.T) {
				if n := <-refused; n != 0 {
					t.Fatalf("%d dials were refused during the takeover", n)
				}
			})
			s.And("the old Run returned nil after its drain", func(t *testing.T) {
				awaitReturn(t, rig, "the old Run")
			})
			s.And("the taker serves zai afterwards", func(t *testing.T) {
				rig.awaitHealth(t, "zai")
				if status, body := rig.postModel(t, "glm-5.3"); status != 200 {
					t.Fatalf("after the takeover: %d %q", status, body)
				}
			})
		})
	})
}
