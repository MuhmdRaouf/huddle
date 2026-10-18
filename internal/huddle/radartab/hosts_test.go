package radartab

// The tab's host guard: radar's own rule answers loopback names on its port alone, but huddle
// serves the tab on its hub listener too, under the name the role certified — the embedding
// server hands the tab the hosts it answers, and the tab answers exactly those.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// TestTheTabAnswersTheHostsTheEmbeddingServerAllows mounts the tab behind a predicate naming a
// hub's spellings and proves a request under the hub's name reaches radar's routes while one
// under a name nobody serves is still refused.
func TestTheTabAnswersTheHostsTheEmbeddingServerAllows(t *testing.T) {
	env, sinceMs := buildGoldenState(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	port := listener.Addr().(*net.TCPAddr).Port

	lines := &logRecorder{}
	mux := http.NewServeMux()
	allowed := map[string]bool{
		fmt.Sprintf("127.0.0.1:%d", port): true,
		"hub.example.test:8443":           true,
	}
	tab := Mount(context.Background(), mux, env["RADAR_HOME"], MountOptions{
		Env:            env,
		Version:        "golden",
		Port:           port,
		SinceMs:        sinceMs,
		IntervalMs:     quietMs,
		InsightsTickMs: quietMs,
		NotifyRunner:   func(string, []string) {},
		Log:            lines.record,
		HostAllowed:    func(host string) bool { return allowed[host] },
	})
	t.Cleanup(tab.Stop)
	if !tab.Owns {
		t.Fatalf("the tab did not take the lock: %s", lines.joined())
	}
	httpSrv := &http.Server{Handler: mux}
	serveDone := make(chan struct{})
	go func() { defer close(serveDone); _ = httpSrv.Serve(listener) }()
	t.Cleanup(func() { _ = httpSrv.Close(); <-serveDone })

	client := &http.Client{Timeout: 30 * time.Second}
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	call := func(host string) int {
		request, err := http.NewRequest(http.MethodGet, base+Prefix+"/api/summary", nil)
		if err != nil {
			t.Fatalf("build the request: %v", err)
		}
		request.Host = host
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("the request as %s failed: %v", host, err)
		}
		_ = response.Body.Close()
		return response.StatusCode
	}
	if code := call("hub.example.test:8443"); code != http.StatusOK {
		t.Fatalf("the request as the hub's own name answered %d, want 200", code)
	}
	if code := call("stranger.example:9"); code != http.StatusForbidden {
		t.Fatalf("the request as a served-nowhere name answered %d, want the 403", code)
	}
}
