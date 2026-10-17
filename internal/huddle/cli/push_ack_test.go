package cli

// The bridge's push ack (SPEC 6.4): while its live stream is up, the bridge tells the registry
// its channel pushes flow — on the stream's hello and on each heartbeat — so the hub delivers
// the owner's words by push instead of waiting for the session's next prompt.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTheBridgeAcksItsPushesToTheRegistry(t *testing.T) {
	var mu sync.Mutex
	acks := []map[string]any{}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/registry/push-ack":
			raw, _ := io.ReadAll(r.Body)
			body := map[string]any{}
			_ = json.Unmarshal(raw, &body)
			mu.Lock()
			acks = append(acks, body)
			mu.Unlock()
			w.Header().Set("content-type", "application/json")
			fmt.Fprint(w, `{"ok":true}`)
		case strings.HasSuffix(r.URL.Path, "/live"):
			w.Header().Set("content-type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "data: {\"type\":\"hello\",\"data\":{\"last\":5}}\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			// the heartbeat the server sends while the stream is idle: the ack rides it
			select {
			case <-r.Context().Done():
			case <-time.After(150 * time.Millisecond):
			}
			fmt.Fprint(w, ": ping\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			<-r.Context().Done()
		case strings.HasSuffix(r.URL.Path, "/op/inbox"):
			w.Header().Set("content-type", "application/json")
			fmt.Fprint(w, `{"result":[]}`)
		case strings.HasSuffix(r.URL.Path, "/op/events"):
			w.Header().Set("content-type", "application/json")
			fmt.Fprint(w, `{"result":[]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	b := runMcpBridge(t, "", map[string]string{
		"HUDDLE_URL": fake.URL, "HUDDLE_CHANNEL": "q", "HUDDLE_AS": "b", "HUDDLE_TOKEN": mcpRootToken,
		// this machine exports the XDG dirs: the world must read throwaway ones
		"XDG_CONFIG_HOME": t.TempDir(), "XDG_DATA_HOME": t.TempDir(), "RADAR_HOME": t.TempDir(),
	})
	// the hello's ack and the heartbeat's: two prove the proof stays fresh while the stream is up
	deadline := time.Now().Add(8 * time.Second)
	for {
		mu.Lock()
		count := len(acks)
		mu.Unlock()
		if count >= 2 {
			break
		}
		if time.Now().After(deadline) {
			b.cancel()
			t.Fatalf("the bridge acked %d times, want its hello and a heartbeat", count)
		}
		time.Sleep(25 * time.Millisecond)
	}
	mu.Lock()
	for i, ack := range acks {
		if ack["channel"] != "q" || ack["as"] != "b" {
			t.Errorf("ack %d = %v, want channel q as b", i, ack)
		}
	}
	mu.Unlock()
	// the bridge goes before the fake server closes: its held-open stream is the fake's to end
	b.cancel()
	fake.Close()
}
