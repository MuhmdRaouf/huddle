package routerd

// The daemon's handover: a new Run takes the listening socket from a running one over the
// unix socket, so the port never closes, while the old Run drains the streams it already
// accepted and then lets the router lock go. Written as scenarios over the daemon rig: the old
// and the new Run are goroutines of the test process, each with its own context.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/singleton"
	"github.com/MuhmdRaouf/huddle/internal/core/router"
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

// startTakeover runs one Run with Takeover set until the test ends, and fails when it stops on
// its own. Its context is its own, like a second process's would be.
func startTakeover(t *testing.T, rig *daemonRig) <-chan error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Env: rig.env, Log: rig.logs.add, Poll: 30 * time.Millisecond, Takeover: true})
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
				startTakeover(s.T, rig)
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
				newDone = startTakeover(s.T, rig)
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
	})
}
