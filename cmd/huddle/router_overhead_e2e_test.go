package main

// The router's latency gate, end to end: a fake upstream streams 200 SSE chunks 10 ms apart, and
// the same stream is read three ways at once — straight from the fake, through huddle-router's
// provider lane, and through its decrypted CONNECT api.anthropic.com path. What the router adds
// to the first byte and to each chunk's trip must stay under the budgets below, so a session
// never feels the router. The daemon is this test's own child on a free port and a temp home.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/pki"
	"github.com/MuhmdRaouf/huddle/internal/testkit"
	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

const (
	// firstByteBudget is the most the router may add to the first streamed byte: the median
	// over overheadRuns of each run's paired first-byte delta, the routed path's first byte
	// minus the direct path's in that same run.
	firstByteBudget = 5 * time.Millisecond
	// chunkBudget is the most the router may add to one chunk's trip (the median over
	// overheadRuns of each run's median chunk).
	chunkBudget = time.Millisecond

	overheadChunks = 200
	overheadGap    = 10 * time.Millisecond
	overheadRuns   = 20
)

// overheadFake streams one Anthropic-shaped answer per request: message_start at once, then
// overheadChunks text deltas overheadGap apart, each carrying the moment it was written on the
// test's monotonic clock, then message_stop.
func overheadFake(t *testing.T, base time.Time) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		write := func(event, data string) bool {
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
				return false
			}
			flusher.Flush()
			return true
		}
		if !write("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`) {
			return
		}
		for range overheadChunks {
			time.Sleep(overheadGap)
			at := time.Since(base).Nanoseconds()
			delta := fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"@%d@"}}`, at)
			if !write("content_block_delta", delta) {
				return
			}
		}
		write("message_stop", `{"type":"message_stop"}`)
	}))
	t.Cleanup(server.Close)
	return server
}

// streamTiming is one stream read on one path: how long its first byte took after the request
// went out, and the median of its chunks' trips from the fake's write to the reader.
type streamTiming struct {
	firstByte time.Duration
	chunk     time.Duration
}

// readStream posts one request for model to target with client and times the answer's stream.
// filler pads the user message with that many bytes of "x", so the caller sets the body's size.
func readStream(client *http.Client, target, model string, filler int, base time.Time) (streamTiming, error) {
	body := fmt.Sprintf(`{"model":%q,"max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi%s"}]}`, model, strings.Repeat("x", filler))
	start := time.Now()
	resp, err := client.Post(target, "application/json", strings.NewReader(body))
	if err != nil {
		return streamTiming{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		text, _ := io.ReadAll(resp.Body)
		return streamTiming{}, fmt.Errorf("%s answered %d: %s", target, resp.StatusCode, text)
	}
	reader := bufio.NewReader(resp.Body)
	timing := streamTiming{}
	trips := make([]time.Duration, 0, overheadChunks)
	for {
		line, err := reader.ReadSlice('\n')
		if timing.firstByte == 0 && len(line) > 0 {
			timing.firstByte = time.Since(start)
		}
		if marked := bytes.Index(line, []byte(`"text":"@`)); marked >= 0 {
			arrived := time.Since(base)
			rest := line[marked+len(`"text":"@`):]
			end := bytes.IndexByte(rest, '@')
			if end < 0 {
				return streamTiming{}, fmt.Errorf("a chunk without its mark: %q", line)
			}
			sent, parseErr := strconv.ParseInt(string(rest[:end]), 10, 64)
			if parseErr != nil {
				return streamTiming{}, parseErr
			}
			trips = append(trips, arrived-time.Duration(sent))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return streamTiming{}, err
		}
	}
	if len(trips) != overheadChunks {
		return streamTiming{}, fmt.Errorf("%s streamed %d chunks, want %d", target, len(trips), overheadChunks)
	}
	timing.chunk = median(trips)
	return timing, nil
}

func median(values []time.Duration) time.Duration {
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	return sorted[len(sorted)/2]
}

// medianInt64 is the median of a slice of counts, 0 when there is none.
func medianInt64(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	return sorted[len(sorted)/2]
}

// spoolRoute is one route event the daemon wrote: the route the request took and the router's
// own microseconds for it, nil when the event carries none.
type spoolRoute struct {
	route    string
	routerUs *int64
}

// spoolRoutes reads every route event in the daemon's spool, in the day files' order.
func spoolRoutes(t *testing.T, spoolDir string) []spoolRoute {
	t.Helper()
	entries, err := os.ReadDir(spoolDir)
	if err != nil {
		return nil
	}
	var routes []spoolRoute
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		file, err := os.Open(filepath.Join(spoolDir, entry.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", entry.Name(), err)
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for scanner.Scan() {
			var line struct {
				Event    string `json:"event"`
				Route    string `json:"route"`
				RouterUs *int64 `json:"router_us"`
			}
			if json.Unmarshal(scanner.Bytes(), &line) != nil || line.Event != "route" {
				continue
			}
			routes = append(routes, spoolRoute{route: line.Route, routerUs: line.RouterUs})
		}
		_ = file.Close()
	}
	return routes
}

func TestE2ERouterOverhead(t *testing.T) {
	root := t.TempDir()
	routerHome := filepath.Join(root, "router-home")
	caDir := filepath.Join(root, "ca")
	base := time.Now()
	fake := overheadFake(t, base)
	routerPort := ports.Free(t)

	// The CA the router decrypts api.anthropic.com with is the one the client trusts; the
	// router loads this very one from the directory beside its home.
	ca, err := pki.LoadOrCreateRouterCA(context.Background(), caDir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca.CertPEM()) {
		t.Fatal("the CA's PEM did not parse")
	}
	if err := os.MkdirAll(routerHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(routerHome, "providers.json"), []byte(`{"providers":["zai"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := isoenv.Environ(t, filepath.Join(root, "user"),
		"HUDDLE_HOME="+filepath.Join(root, "huddle-home"),
		"RADAR_HOME="+filepath.Join(root, "radar"),
		"HUDDLE_NO_PROJECT_CRED=1",
		pki.StoreOffEnv+"=1",
		"HUDDLE_ROUTER_HOME="+routerHome,
		"HUDDLE_ROUTER_PORT="+strconv.Itoa(routerPort),
		"HUDDLE_ROUTER_ANTHROPIC_URL="+fake.URL,
		"ZAI_ROUTER_URL="+fake.URL,
		"ZAI_API_KEY=test-key",
	)
	routerURL := fmt.Sprintf("http://127.0.0.1:%d", routerPort)
	healthy := func() bool {
		client := &http.Client{Timeout: time.Second}
		resp, err := client.Get(routerURL + "/huddle-router/health")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}
	start := exec.Command(huddleBin, "router", "start")
	start.Env = env
	start.Dir = root
	if out, err := start.CombinedOutput(); err != nil {
		t.Fatalf("router start: %v\n%s", err, out)
	}
	if !pollFor(5*time.Second, healthy) {
		t.Fatal("the daemon never answered its health path")
	}
	daemonPID := 0
	if raw, err := os.ReadFile(filepath.Join(routerHome, "run", "router.json")); err == nil {
		var record struct {
			PID int `json:"pid"`
		}
		if json.Unmarshal(raw, &record) == nil {
			daemonPID = record.PID
		}
	}
	if daemonPID <= 0 {
		t.Fatal("the daemon published no pid")
	}
	t.Cleanup(func() {
		_ = syscall.Kill(daemonPID, syscall.SIGTERM)
		if !pollFor(5*time.Second, func() bool { return !healthy() }) {
			_ = syscall.Kill(daemonPID, syscall.SIGKILL)
			pollFor(2*time.Second, func() bool { return !healthy() })
		}
	})

	plain := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DisableCompression: true}}
	proxied := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		Proxy:              func(*http.Request) (*url.URL, error) { return url.Parse(routerURL) },
		TLSClientConfig:    &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		DisableCompression: true,
	}}
	// route is the route event name the daemon writes for a forwarded request on this path,
	// "" for the direct path that never meets the router.
	paths := []struct {
		name   string
		client *http.Client
		target string
		model  string
		route  string
	}{
		{"direct", plain, fake.URL + "/v1/messages", "claude-opus-5-5", ""},
		{"lane", plain, routerURL + "/v1/messages", "glm-5.3", "provider"},
		{"connect", proxied, "https://" + pki.RouterHost + "/v1/messages", "claude-opus-5-5", "anthropic"},
	}
	// Real Claude Code requests run from a small prompt to several MiB of context, and each size
	// must hold the budgets.
	sizes := []struct {
		name   string
		filler int
	}{
		{"100 B", 0},
		{"100 KiB", 100 << 10},
		{"2 MiB", 2 << 20},
	}

	spoolDir := filepath.Join(root, "radar", "spool")
	// served counts the route events a path's earlier sizes already claimed, so each size reads
	// only its own; every forwarded request of a size writes exactly one.
	served := map[string]int{}
	// own keeps the router's own microseconds each routed path carried, for the closing summary.
	own := map[string][]int64{}

	for _, size := range sizes {
		// The three paths of one run stream at the same moment, so each run's noise lands on all
		// three alike and the gate takes 20 streams' time, not 60.
		runs := make([][]streamTiming, overheadRuns)
		for run := range overheadRuns {
			timings := make([]streamTiming, len(paths))
			errs := make([]error, len(paths))
			var wg sync.WaitGroup
			for i, path := range paths {
				wg.Go(func() {
					timings[i], errs[i] = readStream(path.client, path.target, path.model, size.filler, base)
				})
			}
			wg.Wait()
			for i, err := range errs {
				if err != nil {
					t.Fatalf("run %d, %s, %s: %v", run, paths[i].name, size.name, err)
				}
			}
			runs[run] = timings
		}

		// The daemon appends one route event per forwarded request once its answer ends; wait
		// for this size's own events before the cases read their medians from them.
		var sized map[string][]spoolRoute
		deadline := time.Now().Add(10 * time.Second)
		for {
			sized = map[string][]spoolRoute{}
			counts := map[string]int{}
			for _, event := range spoolRoutes(t, spoolDir) {
				counts[event.route]++
				if counts[event.route] > served[event.route] {
					sized[event.route] = append(sized[event.route], event)
				}
			}
			ready := true
			for _, path := range paths[1:] {
				if len(sized[path.route]) < overheadRuns {
					ready = false
				}
			}
			if ready || time.Now().After(deadline) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		for _, path := range paths[1:] {
			served[path.route] += len(sized[path.route])
		}

		for at := 1; at < len(paths); at++ {
			path := paths[at]
			t.Run(path.name+", "+size.name, func(t *testing.T) {
				// The paired deltas come from the same run on both sides, so one moment's load
				// lands on both and their median is what the router itself adds.
				firstDeltas := make([]time.Duration, 0, overheadRuns)
				chunkOverheads := make([]time.Duration, 0, overheadRuns)
				for _, timings := range runs {
					firstDeltas = append(firstDeltas, timings[at].firstByte-timings[0].firstByte)
					chunkOverheads = append(chunkOverheads, timings[at].chunk-timings[0].chunk)
				}
				firstOverhead := median(firstDeltas)
				chunkOverhead := median(chunkOverheads)
				// Every forwarded request's route event says how long the router itself held it.
				ownUs := make([]int64, 0, overheadRuns)
				for _, event := range sized[path.route] {
					if event.routerUs == nil || *event.routerUs < 1 {
						t.Errorf("%s: the route event carries no router's own time (router_us %v)", path.name, event.routerUs)
						continue
					}
					ownUs = append(ownUs, *event.routerUs)
				}
				own[path.route] = append(own[path.route], ownUs...)
				t.Logf("%s, %s: first byte overhead %v, median of %d paired deltas (budget %v); chunk overhead median %v (budget %v); router %d µs",
					path.name, size.name, firstOverhead, overheadRuns, firstByteBudget,
					chunkOverhead, chunkBudget, medianInt64(ownUs))
				// the race detector slows the router's own work 5-20x and not the fake's, so a race
				// build logs the numbers and holds the budgets to the plain build
				if testkit.RaceEnabled {
					return
				}
				if firstOverhead >= firstByteBudget {
					t.Errorf("%s, %s: the router adds %v to the first byte, budget %v", path.name, size.name, firstOverhead, firstByteBudget)
				}
				if chunkOverhead >= chunkBudget {
					t.Errorf("%s, %s: the router adds %v to each chunk, budget %v", path.name, size.name, chunkOverhead, chunkBudget)
				}
			})
		}
	}

	// One closing line per routed path: the router's own median over every forwarded request of
	// every size.
	for _, path := range paths[1:] {
		t.Logf("%s: router %d µs median over %d route events", path.name, medianInt64(own[path.route]), len(own[path.route]))
	}
}
