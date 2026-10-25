package main

// A router update in the middle of a Claude turn, end to end on the real binary: a fake Anthropic
// streams an answer through the decrypted CONNECT api.anthropic.com path, a start from a second
// copy of the binary takes the listening socket over mid-stream, and the old daemon keeps the
// tunnel it already carries until the answer ends. Every process here is this test's child, every
// home a temp one, and the router home a short one: the handover socket is a unix socket, and
// macOS caps a socket path at 104 bytes. The local CA sits beside the router home, where the
// router loads it from.

import (
	"bufio"
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
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/pki"
	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

const (
	// updateChunks is how many text deltas the long answer streams, updateGap apart: ten seconds
	// of answer, so the handover lands well inside it.
	updateChunks = 200
	updateGap    = 50 * time.Millisecond
	// updateHandoverAt is the chunk after which the test starts the second binary.
	updateHandoverAt = 40
)

// updateFake is a fake Anthropic: each answer streams message_start, as many text deltas as the
// request's max_tokens asks for, updateGap apart, then message_stop.
func updateFake(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxTokens int `json:"max_tokens"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
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
		for i := range body.MaxTokens {
			time.Sleep(updateGap)
			if !write("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"c%d"}}`, i)) {
				return
			}
		}
		write("message_stop", `{"type":"message_stop"}`)
	}))
	t.Cleanup(server.Close)
	return server
}

// streamedTurn is what one streamed answer delivered to the client.
type streamedTurn struct {
	chunks int
	stop   bool
	err    error
}

// streamTurn posts one claude request for chunks deltas through client and reads its stream to
// the end, naming each delta on seen as it arrives (seen may be nil).
func streamTurn(client *http.Client, chunks int, seen chan<- int) streamedTurn {
	body := fmt.Sprintf(`{"model":"claude-opus-5-5","max_tokens":%d,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, chunks)
	resp, err := client.Post("https://"+pki.RouterHost+"/v1/messages", "application/json", strings.NewReader(body))
	if err != nil {
		return streamedTurn{err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		text, _ := io.ReadAll(resp.Body)
		return streamedTurn{err: fmt.Errorf("the router answered %d: %s", resp.StatusCode, text)}
	}
	turn := streamedTurn{}
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: content_block_delta"):
			turn.chunks++
			if seen != nil {
				seen <- turn.chunks
			}
		case strings.HasPrefix(line, "event: message_stop"):
			turn.stop = true
		}
	}
	turn.err = scanner.Err()
	return turn
}

// proxiedClient is a fresh client that reaches api.anthropic.com through the router's CONNECT,
// trusting the router's local CA: a fresh one never reuses another's tunnel.
func proxiedClient(routerURL string, pool *x509.CertPool) *http.Client {
	return &http.Client{Timeout: time.Minute, Transport: &http.Transport{
		Proxy:              func(*http.Request) (*url.URL, error) { return url.Parse(routerURL) },
		TLSClientConfig:    &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		DisableCompression: true,
	}}
}

func TestE2EARouterUpdateKeepsAStreamingTurn(t *testing.T) {
	root := t.TempDir()
	parent := shortRouterHome(t)
	routerHome := filepath.Join(parent, "r")
	port := ports.Free(t)
	anthropic := updateFake(t)
	zaiUp := newSettingsFake(t, "zai")
	binA := filepath.Join(root, "bin-a", "huddle")
	binB := filepath.Join(root, "bin-b", "huddle")
	copyBin(t, binA)
	copyBin(t, binB)
	ca, err := pki.LoadOrCreateRouterCA(context.Background(), filepath.Join(parent, "ca"), time.Now())
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
	env := isoenv.Lookup(isoenv.Environ(t, filepath.Join(root, "user"),
		"RADAR_HOME="+filepath.Join(root, "radar"),
		"HUDDLE_HOME="+filepath.Join(root, "huddle-home"),
		"HUDDLE_NO_PROJECT_CRED=1",
		pki.StoreOffEnv+"=1",
		"HUDDLE_ROUTER_HOME="+routerHome,
		"HUDDLE_ROUTER_PORT="+strconv.Itoa(port),
		"HUDDLE_ROUTER_ANTHROPIC_URL="+anthropic.URL,
		"ZAI_ROUTER_URL="+zaiUp.server.URL,
		"ZAI_API_KEY=router-update-e2e-key",
	))
	pids := newPidWatch(t)
	routerURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	if code, out, errOut := runBin(t, binA, env, root, "router", "start"); code != 0 {
		t.Fatalf("the first start = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	until(t, 15*time.Second, func() bool { return routerHealthy(port) }, "daemon A to answer its health path")
	recordA := awaitHandoverRecord(t, routerHome)
	pids.see(recordA.PID)

	long := proxiedClient(routerURL, pool)
	defer long.CloseIdleConnections()
	seen := make(chan int, updateChunks)
	finished := make(chan streamedTurn, 1)
	go func() { finished <- streamTurn(long, updateChunks, seen) }()
	waitChunk := time.After(30 * time.Second)
	for reached := false; !reached; {
		select {
		case n := <-seen:
			reached = n >= updateHandoverAt
		case turn := <-finished:
			t.Fatalf("the turn ended before the handover: %d chunks, stop %v, err %v", turn.chunks, turn.stop, turn.err)
		case <-waitChunk:
			t.Fatalf("the turn never reached chunk %d", updateHandoverAt)
		}
	}

	code, out, errOut := runBin(t, binB, env, root, "router", "start")
	if code != 0 {
		t.Fatalf("the start from binary B = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(out, fmt.Sprintf("(pid %d → ", recordA.PID)) {
		t.Fatalf("the start from binary B = %q, want the updated line naming pid %d", out, recordA.PID)
	}
	var recordB handoverRecord
	until(t, 10*time.Second, func() bool {
		recordB = awaitHandoverRecord(t, routerHome)
		return recordB.PID != recordA.PID
	}, "daemon B's record")
	pids.see(recordB.PID)

	var turn streamedTurn
	select {
	case turn = <-finished:
	case <-time.After(time.Minute):
		t.Fatal("the turn never ended")
	}
	if turn.err != nil || turn.chunks != updateChunks || !turn.stop {
		t.Fatalf("the turn across the handover read %d of %d chunks, message_stop %v, err %v",
			turn.chunks, updateChunks, turn.stop, turn.err)
	}

	// The old daemon lets go once the tunnel it carried ends: its pid gone, the next request can
	// only be answered by the new one.
	long.CloseIdleConnections()
	until(t, 20*time.Second, func() bool { return syscall.Kill(recordA.PID, 0) != nil }, "daemon A to exit after its drain")
	if syscall.Kill(recordB.PID, 0) != nil {
		t.Fatalf("daemon B (pid %d) is gone", recordB.PID)
	}
	after := proxiedClient(routerURL, pool)
	defer after.CloseIdleConnections()
	if next := streamTurn(after, 3, nil); next.err != nil || next.chunks != 3 || !next.stop {
		t.Fatalf("a request after the handover read %d of 3 chunks, message_stop %v, err %v", next.chunks, next.stop, next.err)
	}
	if record := awaitHandoverRecord(t, routerHome); record.PID != recordB.PID {
		t.Fatalf("router.json names pid %d after the handover, want daemon B's %d", record.PID, recordB.PID)
	}
}
