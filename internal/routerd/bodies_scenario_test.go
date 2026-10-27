package routerd

// The daemon's bodies, end to end: with a keystore every request's and answer's body lands in
// router.db under the route event's body id, the spent ones are swept at start, the tap adds
// nothing to the first byte, and with no keystore the same router serves with bodies off.

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/radar/ledger"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/testkit"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// bodiesTestKey is the 32 raw bytes every scenario's keystore holds, so the store a scenario
// opens itself reads what the daemon's store wrote.
var bodiesTestKey = []byte("bodies-scenario-key-0123456789ab")

// bodiesKeystore is a KeySource holding one fixed encoding: the fake every scenario's daemon
// starts with.
type bodiesKeystore struct{ value string }

func bodiesKeystoreOf(key []byte) *bodiesKeystore {
	return &bodiesKeystore{value: base64.RawStdEncoding.EncodeToString(key)}
}

func (k *bodiesKeystore) Get(ctx context.Context) (string, bool) {
	return k.value, true
}

func (k *bodiesKeystore) Set(ctx context.Context, value string) error {
	return nil
}

// bodiesDaemon is one daemon Run a scenario started itself, with the BodyKey the rig's own
// start does not set.
type bodiesDaemon struct {
	cancel  context.CancelFunc
	done    <-chan error
	mu      sync.Mutex
	stopped bool
}

// startBodiesDaemon runs the daemon with the scenario's key source until the test ends.
func startBodiesDaemon(t *testing.T, rig *daemonRig, key ledger.KeySource) *bodiesDaemon {
	t.Helper()
	options := Options{
		Env:     rig.env,
		Port:    rig.port,
		Version: "test",
		Log:     rig.logs.add,
		Poll:    30 * time.Millisecond,
		BodyKey: key,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, options) }()
	daemon := &bodiesDaemon{cancel: cancel, done: done}
	t.Cleanup(func() {
		daemon.mu.Lock()
		stopped := daemon.stopped
		daemon.mu.Unlock()
		if stopped {
			return
		}
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	})
	return daemon
}

// stop ends the daemon and waits for it: a stop closes the tap, so the bodies are only all on
// disk once this answers.
func (d *bodiesDaemon) stop(t *testing.T) {
	t.Helper()
	d.cancel()
	select {
	case err := <-d.done:
		if err != nil {
			t.Errorf("the daemon stopped with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("the daemon never stopped")
	}
	d.mu.Lock()
	d.stopped = true
	d.mu.Unlock()
}

// bodiesRequest is the JSON a scenario posts; typed so the padding lands in one place.
type bodiesRequest struct {
	Model    string          `json:"model"`
	Messages []bodiesMessage `json:"messages"`
}

type bodiesMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// postBodyOf posts one raw body and answers the status and the whole answer.
func postBodyOf(t *testing.T, url, body string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer anthropic-oauth")
	request.Header.Set("X-Api-Key", "anthropic-key")
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Do(request)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the answer of %s: %v", url, err)
	}
	return resp.StatusCode, string(answer)
}

// firstByteOf posts one raw body and answers how long the first byte of the answer took; a
// non-200 fails, so a budget is never met by a fast error.
func firstByteOf(t *testing.T, url, body string) time.Duration {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	start := time.Now()
	resp, err := client.Do(request)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		answer, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s = %d %q", url, resp.StatusCode, string(answer))
	}
	one := make([]byte, 1)
	if _, err := io.ReadFull(resp.Body, one); err != nil {
		t.Fatalf("read the first byte of %s: %v", url, err)
	}
	return time.Since(start)
}

// routeBodyID answers the body id of the first route event in the spool, waited for because
// the route line lands with the answer.
func routeBodyID(t *testing.T, spoolDir string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if id := scanBodyID(spoolDir); id != "" {
			return id
		}
		if time.Now().After(deadline) {
			t.Fatal("no route event with a body id in the spool")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// scanBodyID reads every spool line for the first route event carrying a body id; "" when the
// spool has none yet.
func scanBodyID(spoolDir string) string {
	entries, err := os.ReadDir(spoolDir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		file, err := os.Open(filepath.Join(spoolDir, entry.Name()))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for scanner.Scan() {
			var line struct {
				Event  string  `json:"event"`
				BodyID *string `json:"body_id"`
			}
			if json.Unmarshal(scanner.Bytes(), &line) != nil || line.Event != "route" || line.BodyID == nil {
				continue
			}
			_ = file.Close()
			return *line.BodyID
		}
		_ = file.Close()
	}
	return ""
}

// TestTheRouterKeepsEveryBody runs a request through a lane and proves both its sides are in
// router.db under the route event's one body id once the router stops.
func TestTheRouterKeepsEveryBody(t *testing.T) {
	t.Parallel()
	bdd.Feature(t, "the router's bodies", func(f *bdd.F) {
		f.Scenario("a request through a lane keeps its input and its answer under one body id", func(s *bdd.S) {
			var rig *daemonRig
			var daemon *bodiesDaemon
			var sent string
			s.Given("a daemon serves zai with a keystore", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				if err := router.SetEnabled(rig.env, "zai", true); err != nil {
					t.Fatal(err)
				}
				daemon = startBodiesDaemon(s.T, rig, bodiesKeystoreOf(bodiesTestKey))
				rig.awaitHealth(t, "zai")
			})
			s.When("a glm request with a body of its own goes through the lane", func(t *testing.T) {
				request, err := json.Marshal(bodiesRequest{
					Model:    "glm-5.3",
					Messages: []bodiesMessage{{Role: "user", Content: "keep this whole body " + strings.Repeat("x", 4096)}},
				})
				if err != nil {
					t.Fatal(err)
				}
				sent = string(request)
				if status, body := postBodyOf(t, fmt.Sprintf("http://127.0.0.1:%d/v1/messages", rig.port), sent); status != 200 {
					t.Fatalf("glm-5.3 = %d %q", status, body)
				}
			})
			s.Then("after the router stops, the route event's body id reads the input and the answer back", func(t *testing.T) {
				daemon.stop(t)
				seen := rig.zai.requests()
				if len(seen) != 1 {
					t.Fatalf("zai saw %d requests", len(seen))
				}
				ctx := context.Background()
				id := routeBodyID(t, filepath.Join(rig.env["RADAR_HOME"], "spool"))
				store, err := ledger.OpenBodies(paths.RouterBodiesPath(ctx, rig.env), bodiesTestKey)
				if err != nil {
					t.Fatalf("open router.db: %v", err)
				}
				defer store.Close()
				input, ok, err := store.Body(ctx, id, "input")
				if err != nil {
					t.Fatalf("read the input body: %v", err)
				}
				if !ok {
					t.Fatal("the input body is not in router.db")
				}
				if string(input) != seen[0].body {
					t.Errorf("the input body is %d bytes, want the %d the upstream saw", len(input), len(seen[0].body))
				}
				output, ok, err := store.Body(ctx, id, "output")
				if err != nil {
					t.Fatalf("read the answer body: %v", err)
				}
				if !ok {
					t.Fatal("the answer body is not in router.db")
				}
				if !strings.Contains(string(output), "zai-1") || !strings.Contains(string(output), "zai-2") {
					t.Errorf("the answer body lost the stream: %q", string(output))
				}
			})
		})
	})
}

// TestTheRouterSweepsOldBodiesAtStart seeds router.db with a body past its seven days and
// proves the starting daemon takes it.
func TestTheRouterSweepsOldBodiesAtStart(t *testing.T) {
	t.Parallel()
	bdd.Feature(t, "the router's bodies", func(f *bdd.F) {
		f.Scenario("a body eight days old is gone once the daemon has started", func(s *bdd.S) {
			var rig *daemonRig
			var path string
			var daemon *bodiesDaemon
			s.Given("router.db holds a body put eight days ago", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				if err := router.SetEnabled(rig.env, "zai", true); err != nil {
					t.Fatal(err)
				}
				ctx := context.Background()
				path = paths.RouterBodiesPath(ctx, rig.env)
				seed, err := ledger.OpenBodies(path, bodiesTestKey)
				if err != nil {
					t.Fatalf("seed router.db: %v", err)
				}
				old := []byte(`{"model":"m","messages":[{"role":"user","content":"a body from last week"}]}`)
				if err := seed.PutBody(ctx, "swept-body", "input", old, time.Now().Add(-8*24*time.Hour)); err != nil {
					t.Fatalf("put the old body: %v", err)
				}
				if err := seed.Close(); err != nil {
					t.Fatalf("close the seed store: %v", err)
				}
			})
			s.When("the daemon starts and serves", func(t *testing.T) {
				daemon = startBodiesDaemon(s.T, rig, bodiesKeystoreOf(bodiesTestKey))
				rig.awaitHealth(t, "zai")
			})
			s.Then("the eight-day body is gone", func(t *testing.T) {
				daemon.stop(t)
				ctx := context.Background()
				store, err := ledger.OpenBodies(path, bodiesTestKey)
				if err != nil {
					t.Fatalf("open router.db: %v", err)
				}
				defer store.Close()
				if _, ok, err := store.Body(ctx, "swept-body", "input"); err != nil {
					t.Fatalf("read the old body: %v", err)
				} else if ok {
					t.Error("the eight-day body is still in router.db")
				}
			})
		})
	})
}

// bodiesFirstByteRuns is how many runs of each path the overhead takes; the best of each is
// compared, the way the binary e2e measures.
const bodiesFirstByteRuns = 5

// bodiesFirstByteBudget is the most the tap may add to a first byte.
const bodiesFirstByteBudget = 5 * time.Millisecond

// TestTheTapKeepsTheOverheadBound proves the tap stays off the first byte: a 100 KiB and a
// 2 MiB request through the router with the tap on answer as fast as the same request straight
// to the upstream, best of five.
func TestTheTapKeepsTheOverheadBound(t *testing.T) {
	t.Parallel()
	if testkit.RaceEnabled {
		t.Skip("the race build inflates every hop; the budget is a release-build gate")
	}
	bdd.Feature(t, "the router's bodies", func(f *bdd.F) {
		f.Scenario("a 100 KiB and a 2 MiB request gain at most five milliseconds of first byte from the tap", func(s *bdd.S) {
			var rig *daemonRig
			s.Given("a daemon serves zai with a keystore", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				if err := router.SetEnabled(rig.env, "zai", true); err != nil {
					t.Fatal(err)
				}
				startBodiesDaemon(s.T, rig, bodiesKeystoreOf(bodiesTestKey))
				rig.awaitHealth(t, "zai")
			})
			s.Then("neither size's first byte gains more than the budget", func(t *testing.T) {
				for _, size := range []struct {
					name string
					pad  int
				}{
					{"100 KiB", 100 << 10},
					{"2 MiB", 2 << 20},
				} {
					request, err := json.Marshal(bodiesRequest{
						Model:    "glm-5.3",
						Messages: []bodiesMessage{{Role: "user", Content: strings.Repeat("x", size.pad)}},
					})
					if err != nil {
						t.Fatal(err)
					}
					body := string(request)
					via := make([]time.Duration, 0, bodiesFirstByteRuns)
					direct := make([]time.Duration, 0, bodiesFirstByteRuns)
					for i := 0; i < bodiesFirstByteRuns; i++ {
						via = append(via, firstByteOf(t, fmt.Sprintf("http://127.0.0.1:%d/v1/messages", rig.port), body))
						direct = append(direct, firstByteOf(t, rig.zai.server.URL+"/v1/messages", body))
					}
					overhead := slices.Min(via) - slices.Min(direct)
					if overhead > bodiesFirstByteBudget {
						t.Errorf("%s: the tap's first byte costs %s, budget %s",
							size.name, overhead, bodiesFirstByteBudget)
					}
				}
			})
		})
	})
}

// TestNoKeystoreMeansNoBodies runs the daemon where the login keystore is out of reach: it
// serves as always, keeps no router.db, and says bodies are off exactly once. It never touches
// the process HOME, on purpose: the temp HOME lives in the rig's Options.Env alone, so the run
// proves the daemon never consults the runner's real login keystore.
func TestNoKeystoreMeansNoBodies(t *testing.T) {
	rig := newDaemonRig(t)
	if err := router.SetEnabled(rig.env, "zai", true); err != nil {
		t.Fatal(err)
	}
	daemon := startBodiesDaemon(t, rig, nil)
	rig.awaitHealth(t, "zai")
	if status, body := rig.postModel(t, "glm-5.3"); status != 200 {
		t.Fatalf("glm-5.3 with bodies off = %d %q", status, body)
	}
	daemon.stop(t)
	if _, err := os.Stat(paths.RouterBodiesPath(context.Background(), rig.env)); !os.IsNotExist(err) {
		t.Error("router.db exists with no keystore")
	}
	if n := strings.Count(rig.logs.joined(), "bodies off: no keystore"); n != 1 {
		t.Errorf("the bodies-off line is logged %d times, want once:\n%s", n, rig.logs.joined())
	}
}
