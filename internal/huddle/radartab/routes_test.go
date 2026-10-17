// Route parity of radar's own route table through huddle's mount: every path radar's
// unknown-route answer names must answer under /api/radar too. A route answered with that
// 404-and-routes-list body means the tab dropped it; any other answer — including a 404 about
// the data, a session or node that is not there — names the route served.
package radartab

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/server"
)

// routeWrites gives the write-only route the method and body its answer takes: a GET on it
// falls through radar's read router to the unknown-route answer, which would name a served
// route missing. The other Routes entries answer GET, the way the dashboard reads them.
var routeWrites = map[string]struct {
	method string
	body   string
}{
	"/api/history/clear": {http.MethodPost, "{}"},
}

// concreteRoute swaps every :param segment for x: an id that names nothing, so a route whose
// answer turns on the id answers about the data, never about the route.
func concreteRoute(route string) string {
	segments := strings.Split(route, "/")
	for at, segment := range segments {
		if strings.HasPrefix(segment, ":") {
			segments[at] = "x"
		}
	}
	return strings.Join(segments, "/")
}

// TestTheTabMountsEveryRadarRoute walks radar's route table against the mounted tab: every
// entry of server.Routes must answer under /api/radar, and never with radar's unknown-route
// list.
func TestTheTabMountsEveryRadarRoute(t *testing.T) {
	env, sinceMs := buildGoldenState(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	port := listener.Addr().(*net.TCPAddr).Port

	lines := &logRecorder{}
	mux := http.NewServeMux()
	tab := Mount(context.Background(), mux, env["RADAR_HOME"], MountOptions{
		Env:            env,
		Version:        "golden",
		Port:           port,
		SinceMs:        sinceMs,
		IntervalMs:     quietMs,
		InsightsTickMs: quietMs,
		NotifyRunner:   func(string, []string) {},
		Log:            lines.record,
	})
	t.Cleanup(tab.Stop)
	if !tab.Owns {
		t.Fatalf("the tab did not take the lock: %s", lines.joined())
	}
	httpSrv := &http.Server{Handler: mux}
	serveDone := make(chan struct{})
	go func() { defer close(serveDone); _ = httpSrv.Serve(listener) }()
	t.Cleanup(func() { _ = httpSrv.Close(); <-serveDone })

	waitFor(t, "the fixture in the store", 15*time.Second, func() bool {
		ids := []string{}
		for _, session := range tab.Store.SessionList() {
			ids = append(ids, session.ID)
		}
		return slices.Contains(ids, "sess-a") && slices.Contains(ids, "zai:goldjob") && !tab.Watcher.CatchingUp()
	})
	if tab.Writer == nil {
		t.Fatal("the history writer did not attach; the history routes have nothing to read")
	}
	tab.Writer.Flush()

	client := &http.Client{Timeout: 30 * time.Second}
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	for _, route := range server.Routes {
		if route == "/api/stream" {
			// a live stream never ends; the golden parity test replays its first frames
			continue
		}
		path := Prefix + concreteRoute(route)
		method, sent := http.MethodGet, ""
		if write, ok := routeWrites[route]; ok {
			method, sent = write.method, write.body
		}
		var body io.Reader
		if sent != "" {
			body = strings.NewReader(sent)
		}
		request, err := http.NewRequest(method, base+path, body)
		if err != nil {
			t.Fatalf("build the %s request: %v", path, err)
		}
		if sent != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("the %s request failed: %v", path, err)
		}
		raw, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil {
			t.Fatalf("read the %s answer: %v", path, err)
		}
		if response.StatusCode != http.StatusNotFound {
			continue
		}
		var answer struct {
			Routes []string `json:"routes"`
		}
		if json.Unmarshal(raw, &answer) == nil && len(answer.Routes) > 0 {
			t.Errorf("%s: the answer is radar's unknown-route list; the tab does not serve %s", path, route)
		}
	}
}
