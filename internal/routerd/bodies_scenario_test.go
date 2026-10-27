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
// non-200 is an error, so a budget is never met by a fast error. It answers an error rather
// than failing the test because a run's two requests stream side by side, off the test
// goroutine.
func firstByteOf(url, body string) (time.Duration, error) {
	request, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("build the request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	start := time.Now()
	resp, err := client.Do(request)
	if err != nil {
		return 0, fmt.Errorf("post %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		answer, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("%s = %d %q", url, resp.StatusCode, string(answer))
	}
	one := make([]byte, 1)
	if _, err := io.ReadFull(resp.Body, one); err != nil {
		return 0, fmt.Errorf("read the first byte of %s: %w", url, err)
	}
	return time.Since(start), nil
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

// bodiesFirstByteRuns is how many paired runs each size takes; the median of the paired deltas
// is compared, the way the binary e2e takes a median.
const bodiesFirstByteRuns = 11

// bodiesFirstByteBudget is the most the tap may add to a first byte.
const bodiesFirstByteBudget = 5 * time.Millisecond

// medianOf answers the middle value of the sorted slice; with the odd run count there is
// always exactly one.
func medianOf(values []time.Duration) time.Duration {
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	return sorted[len(sorted)/2]
}

// TestTheTapKeepsTheOverheadBound proves the tap stays off the first byte: a 100 KiB and a
// 2 MiB request through the router with the tap on answer as fast as the same request through
// the same router with the tap off, by the median of eleven paired runs. Two daemons serve, one
// with a keystore and one without, so both paths of a pair meet one router hop and the delta is
// the tap's alone and never the hop's. The two requests of a pair stream at the same moment, so
// the machine's load lands on both alike, the way the binary e2e measures.
func TestTheTapKeepsTheOverheadBound(t *testing.T) {
	// A timing test: it runs alone, never beside the package's other tests.
	if testkit.RaceEnabled {
		t.Skip("the race build inflates every hop; the budget is a release-build gate")
	}
	bdd.Feature(t, "the router's bodies", func(f *bdd.F) {
		f.Scenario("a 100 KiB and a 2 MiB request gain at most five milliseconds of first byte from the tap", func(s *bdd.S) {
			var tapped, untapped *daemonRig
			s.Given("two daemons serve zai, one with a keystore and one without", func(t *testing.T) {
				tapped = newDaemonRig(s.T)
				if err := router.SetEnabled(tapped.env, "zai", true); err != nil {
					t.Fatal(err)
				}
				startBodiesDaemon(s.T, tapped, bodiesKeystoreOf(bodiesTestKey))
				tapped.awaitHealth(t, "zai")
				untapped = newDaemonRig(s.T)
				if err := router.SetEnabled(untapped.env, "zai", true); err != nil {
					t.Fatal(err)
				}
				startBodiesDaemon(s.T, untapped, nil)
				untapped.awaitHealth(t, "zai")
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
					tappedURL := fmt.Sprintf("http://127.0.0.1:%d/v1/messages", tapped.port)
					untappedURL := fmt.Sprintf("http://127.0.0.1:%d/v1/messages", untapped.port)
					deltas := make([]time.Duration, 0, bodiesFirstByteRuns)
					for range bodiesFirstByteRuns {
						var tappedFirst, untappedFirst time.Duration
						var tappedErr, untappedErr error
						var pair sync.WaitGroup
						pair.Add(2)
						go func() {
							defer pair.Done()
							tappedFirst, tappedErr = firstByteOf(tappedURL, body)
						}()
						go func() {
							defer pair.Done()
							untappedFirst, untappedErr = firstByteOf(untappedURL, body)
						}()
						pair.Wait()
						if tappedErr != nil {
							t.Fatalf("the tapped request failed: %v", tappedErr)
						}
						if untappedErr != nil {
							t.Fatalf("the untapped request failed: %v", untappedErr)
						}
						deltas = append(deltas, tappedFirst-untappedFirst)
					}
					overhead := medianOf(deltas)
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

// scanRouteEvents reads every route event in the spool, raw, oldest file first; a route event
// without a body id is exactly what capture off must leave.
func scanRouteEvents(spoolDir string) []string {
	var lines []string
	entries, err := os.ReadDir(spoolDir)
	if err != nil {
		return lines
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
				Event string `json:"event"`
			}
			if json.Unmarshal(scanner.Bytes(), &line) != nil || line.Event != "route" {
				continue
			}
			lines = append(lines, string(scanner.Bytes()))
		}
		_ = file.Close()
	}
	return lines
}

// waitRouteEvents polls for the spool to hold n route events and answers them.
func waitRouteEvents(t *testing.T, spoolDir string, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		events := scanRouteEvents(spoolDir)
		if len(events) >= n {
			return events
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d route events never landed in %s (have %d)", n, spoolDir, len(events))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// settingsFileOf is where the daemon reads the hub's capture switch: the huddle home's data
// dir, resolved the hub's way — $HUDDLE_DATA, else the home root, which with nothing set is
// $HOME/.config/huddle of the daemon's own env.
func settingsFileOf(rig *daemonRig) string {
	return filepath.Join(rig.env["HOME"], ".config", "huddle", "settings.json")
}

// awaitCaptureLine waits for the daemon to log the switch within the two seconds the real
// poll promises, and fails with its log when it does not.
func awaitCaptureLine(t *testing.T, rig *daemonRig, line string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(rig.logs.joined(), line) {
		if time.Now().After(deadline) {
			t.Fatalf("the daemon never logged %q; its log:\n%s", line, rig.logs.joined())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestCaptureOffStopsTheBodies runs the capture switch end to end: capture off in the hub's
// settings file stops the bodies within a poll while the route events flow, and capture back
// on keeps the next request's body.
func TestCaptureOffStopsTheBodies(t *testing.T) {
	bdd.Feature(t, "the router's bodies", func(f *bdd.F) {
		f.Scenario("capture off keeps no body while its route event lands; back on, the next body is kept", func(s *bdd.S) {
			var rig *daemonRig
			var daemon *bodiesDaemon
			var settings string
			s.Given("a daemon serves zai with a keystore", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				if err := router.SetEnabled(rig.env, "zai", true); err != nil {
					t.Fatal(err)
				}
				settings = settingsFileOf(rig)
				daemon = startBodiesDaemon(s.T, rig, bodiesKeystoreOf(bodiesTestKey))
				rig.awaitHealth(t, "zai")
			})
			s.When("the hub's settings turn capture off", func(t *testing.T) {
				if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(settings, []byte("{\"capture\": false}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				awaitCaptureLine(t, rig, "huddle-router: bodies capture off")
			})
			var sent string
			s.Then("a request through the lane keeps no body and its route event lands without one", func(t *testing.T) {
				request, err := json.Marshal(bodiesRequest{
					Model:    "glm-5.3",
					Messages: []bodiesMessage{{Role: "user", Content: "capture off body"}},
				})
				if err != nil {
					t.Fatal(err)
				}
				sent = string(request)
				if status, body := postBodyOf(t, fmt.Sprintf("http://127.0.0.1:%d/v1/messages", rig.port), sent); status != 200 {
					t.Fatalf("glm-5.3 = %d %q", status, body)
				}
				events := waitRouteEvents(t, filepath.Join(rig.env["RADAR_HOME"], "spool"), 1)
				var line map[string]any
				if err := json.Unmarshal([]byte(events[0]), &line); err != nil {
					t.Fatal(err)
				}
				for _, member := range []string{"body_id", "body"} {
					if _, ok := line[member]; ok {
						t.Errorf("the route event with capture off carries %s: %s", member, events[0])
					}
				}
			})
			s.And("capture back on keeps the next request's body", func(t *testing.T) {
				if err := os.WriteFile(settings, []byte("{\"capture\": true}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				awaitCaptureLine(t, rig, "huddle-router: bodies capture on")
				request, err := json.Marshal(bodiesRequest{
					Model:    "glm-5.3",
					Messages: []bodiesMessage{{Role: "user", Content: "capture on body"}},
				})
				if err != nil {
					t.Fatal(err)
				}
				if status, body := postBodyOf(t, fmt.Sprintf("http://127.0.0.1:%d/v1/messages", rig.port), string(request)); status != 200 {
					t.Fatalf("glm-5.3 = %d %q", status, body)
				}
				events := waitRouteEvents(t, filepath.Join(rig.env["RADAR_HOME"], "spool"), 2)
				var first, second struct {
					BodyID *string `json:"body_id"`
				}
				if err := json.Unmarshal([]byte(events[0]), &first); err != nil {
					t.Fatal(err)
				}
				if first.BodyID != nil {
					t.Errorf("the capture-off event kept a body id: %s", events[0])
				}
				if err := json.Unmarshal([]byte(events[1]), &second); err != nil {
					t.Fatal(err)
				}
				if second.BodyID == nil {
					t.Fatalf("the capture-on event has no body id: %s", events[1])
				}
				daemon.stop(t)
				ctx := context.Background()
				store, err := ledger.OpenBodies(paths.RouterBodiesPath(ctx, rig.env), bodiesTestKey)
				if err != nil {
					t.Fatalf("open router.db: %v", err)
				}
				defer store.Close()
				input, ok, err := store.Body(ctx, *second.BodyID, "input")
				if err != nil {
					t.Fatalf("read the input body: %v", err)
				}
				if !ok {
					t.Fatal("the capture-on request's input body is not in router.db")
				}
				if string(input) == sent {
					t.Error("the capture-on request's input body is the capture-off request's")
				}
				if string(input) != rig.zai.requests()[len(rig.zai.requests())-1].body {
					t.Errorf("the kept input is %d bytes, want the %d the upstream saw", len(input), len(rig.zai.requests()[len(rig.zai.requests())-1].body))
				}
				output, ok, err := store.Body(ctx, *second.BodyID, "output")
				if err != nil {
					t.Fatalf("read the answer body: %v", err)
				}
				if !ok || !strings.Contains(string(output), "zai-1") || !strings.Contains(string(output), "zai-2") {
					t.Errorf("the answer body lost the stream: %q", string(output))
				}
			})
		})
	})
}
