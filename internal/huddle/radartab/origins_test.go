package radartab

// The tab's write guard: radar's own rule takes writes from its loopback origin alone, but
// huddle serves the tab on its hub listener too — the embedding server hands the tab the
// origins its own write guard accepts, and the tab takes exactly those writes.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestTheTabTakesTheWritesTheEmbeddingServerAllows mounts the tab behind predicates naming a
// hub's spellings and proves a write under the hub's name and origin reaches radar's routes
// while one from an origin nobody vouches for is still refused.
func TestTheTabTakesTheWritesTheEmbeddingServerAllows(t *testing.T) {
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
	// the shape huddle's own write guard has: an Origin passes when its host is one the
	// server answers
	originOK := func(origin string) bool {
		return allowed[strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://")]
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
		OriginAllowed:  originOK,
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
	budgets := `{"budgets":[{"id":"a","scope":"total","period":"week","limitUsd":3,"action":"warn"}]}`
	save := func(host, origin string) int {
		request, err := http.NewRequest(http.MethodPut, base+Prefix+"/api/budgets",
			strings.NewReader(budgets))
		if err != nil {
			t.Fatalf("build the request: %v", err)
		}
		request.Host = host
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", origin)
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("the write as %s from %s failed: %v", host, origin, err)
		}
		_ = response.Body.Close()
		return response.StatusCode
	}
	const hubOrigin = "https://hub.example.test:8443"
	if code := save("hub.example.test:8443", hubOrigin); code != http.StatusOK {
		t.Fatalf("the write under the hub's name and origin answered %d, want 200", code)
	}
	if code := save("127.0.0.1:"+strconv.Itoa(port),
		fmt.Sprintf("http://127.0.0.1:%d", port)); code != http.StatusOK {
		t.Fatalf("the loopback write answered %d, want 200", code)
	}
	if code := save("hub.example.test:8443", "https://evil.example"); code != http.StatusForbidden {
		t.Fatalf("the write from an origin nobody vouches for answered %d, want the 403", code)
	}
	if held := tab.Insights.Budgets(); len(held) != 1 {
		t.Fatalf("the tab holds %d budgets, want the one the saves landed", len(held))
	}
}
