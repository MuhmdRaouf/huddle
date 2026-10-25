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
	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

const (
	// firstByteBudget is the most the router may add to the first streamed byte (best of
	// firstByteRuns).
	firstByteBudget = 5 * time.Millisecond
	// chunkBudget is the most the router may add to one chunk's trip (the median over
	// overheadRuns of each run's median chunk).
	chunkBudget = time.Millisecond

	overheadChunks = 200
	overheadGap    = 10 * time.Millisecond
	overheadRuns   = 20
	firstByteRuns  = 5
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
func readStream(client *http.Client, target, model string, base time.Time) (streamTiming, error) {
	body := fmt.Sprintf(`{"model":%q,"max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, model)
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
	paths := []struct {
		name   string
		client *http.Client
		target string
		model  string
	}{
		{"direct", plain, fake.URL + "/v1/messages", "claude-opus-5-5"},
		{"lane", plain, routerURL + "/v1/messages", "glm-5.3"},
		{"connect", proxied, "https://" + pki.RouterHost + "/v1/messages", "claude-opus-5-5"},
	}

	// The three paths of one run stream at the same moment, so each run's noise lands on all
	// three alike and the gate takes 20 streams' time, not 60.
	runs := make([][]streamTiming, overheadRuns)
	for run := range overheadRuns {
		timings := make([]streamTiming, len(paths))
		errs := make([]error, len(paths))
		var wg sync.WaitGroup
		for i, path := range paths {
			wg.Go(func() {
				timings[i], errs[i] = readStream(path.client, path.target, path.model, base)
			})
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("run %d, %s: %v", run, paths[i].name, err)
			}
		}
		runs[run] = timings
	}

	for at := 1; at < len(paths); at++ {
		path := paths[at]
		firstBytes := make([]time.Duration, 0, firstByteRuns)
		directFirst := make([]time.Duration, 0, firstByteRuns)
		for _, timings := range runs[:firstByteRuns] {
			firstBytes = append(firstBytes, timings[at].firstByte)
			directFirst = append(directFirst, timings[0].firstByte)
		}
		chunkOverheads := make([]time.Duration, 0, overheadRuns)
		for _, timings := range runs {
			chunkOverheads = append(chunkOverheads, timings[at].chunk-timings[0].chunk)
		}
		firstOverhead := slices.Min(firstBytes) - slices.Min(directFirst)
		chunkOverhead := median(chunkOverheads)
		t.Logf("%s: first byte %v (direct %v, overhead %v, budget %v); chunk overhead median %v (budget %v)",
			path.name, slices.Min(firstBytes), slices.Min(directFirst), firstOverhead, firstByteBudget,
			chunkOverhead, chunkBudget)
		if firstOverhead >= firstByteBudget {
			t.Errorf("%s: the router adds %v to the first byte, budget %v", path.name, firstOverhead, firstByteBudget)
		}
		if chunkOverhead >= chunkBudget {
			t.Errorf("%s: the router adds %v to each chunk, budget %v", path.name, chunkOverhead, chunkBudget)
		}
	}
}
