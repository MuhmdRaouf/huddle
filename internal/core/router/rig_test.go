// The router tests' shared rig: the reference provider, the recording upstream and the event
// collector the multi-router and daemon suites build their lanes from.
package router

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

var routerProvider = domain.DefineProvider(domain.ProviderSpec{
	Name:         "zai",
	Display:      "Z.ai GLM",
	Slash:        "/zai:",
	AgentPrefix:  "zai:",
	BranchPrefix: "zai/",
	Harness:      "zai",
	ArtifactsEnv: "ZAI_ARTIFACTS",
	EnvPrefix:    "ZAI",
	Catalog: map[domain.ModelTier]domain.ModelRef{
		domain.ModelTierMain: {
			Tier:            domain.ModelTierMain,
			ID:              "glm-5.3",
			Label:           "GLM 5.3",
			MaxOutputTokens: i64PtrLocal(131072),
		},
		domain.ModelTierFlash: {
			Tier:            domain.ModelTierFlash,
			ID:              "glm-5.3-flash",
			Label:           "GLM 5.3 Flash",
			MaxOutputTokens: i64PtrLocal(131072),
		},
	},
	TierNames:  map[domain.ModelTier]string{domain.ModelTierMain: "glm", domain.ModelTierFlash: "flash"},
	PingTier:   domain.ModelTierFlash,
	BaseURL:    "https://api.z.ai/api/anthropic",
	Auth:       domain.AuthBearer,
	KeyEnv:     []string{"ZAI_API_KEY"},
	BillingURL: "https://z.ai/manage-apikey/billing",
	// Z.ai's probe: the web_search server tool answers 500 on glm-5.3-flash and works on glm-5.3.
	WebSearchModel: strPtrLocal("glm-5.3"),
	Router: domain.ProviderRouter{
		Port:          18787,
		Label:         "dev.muhmdraouf.zai-router",
		HealthPath:    "/zai-router/health",
		ModelPrefixes: []string{"glm-"},
	},
})

type seenRequest struct {
	path    string
	headers http.Header
	body    string
}

// seenList records requests concurrently.
type seenList struct {
	mu    sync.Mutex
	items []seenRequest
}

func (s *seenList) record(path string, headers http.Header, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, seenRequest{path: path, headers: headers, body: body})
}

func (s *seenList) list() []seenRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]seenRequest(nil), s.items...)
}

func (s *seenList) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}

type eventLog struct {
	mu     sync.Mutex
	events []domain.SpoolEvent
}

func (l *eventLog) record(event domain.SpoolEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *eventLog) all() []domain.SpoolEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]domain.SpoolEvent(nil), l.events...)
}

func (l *eventLog) routes() []domain.RouteEvent {
	var routes []domain.RouteEvent
	for _, event := range l.all() {
		if route, ok := event.(domain.RouteEvent); ok {
			routes = append(routes, route)
		}
	}
	return routes
}

// reservePort takes a loopback port nothing else will take while the test runs, then leaves it
// free.
func reservePort(t *testing.T) int {
	t.Helper()
	return ports.Free(t)
}

// serveRouter listens the router server on its port and closes it with the test.
func serveRouter(t *testing.T, server *http.Server, port int) {
	t.Helper()
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
}

// recordingUpstream is a fake upstream that records what it got and answers with its name,
// streamed in two chunks 20 ms apart.
func recordingUpstream(t *testing.T, name, prefix string, seen *seenList) *url.URL {
	t.Helper()
	port := serve(t, func(w http.ResponseWriter, req *http.Request) {
		body, err := ReadBody(req, BodyLimitBytes)
		if err != nil {
			return
		}
		res := NewResponse(w)
		seen.record(pathOf(req), req.Header, string(body))
		res.Header().Set("Content-Type", "text/event-stream")
		res.WriteHeader(200)
		res.Write([]byte("data: " + name + "-1\n\n"))
		time.Sleep(20 * time.Millisecond)
		res.End([]byte("data: " + name + "-2\n\n"))
	}, nil)
	return mustURL(t, fmt.Sprintf("http://127.0.0.1:%d%s", port, prefix))
}

// pathOf is the request's path with its query.
func pathOf(req *http.Request) string {
	if req.URL.RawQuery != "" {
		return req.URL.Path + "?" + req.URL.RawQuery
	}
	return req.URL.Path
}

// rigOptions is everything a test changes about the rig, plus the pieces its inline variants

// answer is one answered request as the tests read it.
type answer struct {
	status int
	header http.Header
	body   string
}

// postRawBody posts one exact body with headers on a connection of its own.
func postRawBody(port int, path, body string, headers map[string]string) (answer, error) {
	return doBody(http.MethodPost, port, path, body, headers)
}

// getBody reads one path with GET, as the health checks do.
func getBody(port int, path string) (answer, error) {
	return doBody(http.MethodGet, port, path, "", nil)
}

func doBody(method string, port int, path, body string, headers map[string]string) (answer, error) {
	request, err := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), strings.NewReader(body))
	if err != nil {
		return answer{}, err
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return answer{}, err
	}
	defer response.Body.Close()
	text, err := io.ReadAll(response.Body)
	if err != nil {
		return answer{}, err
	}
	return answer{status: response.StatusCode, header: response.Header, body: string(text)}, nil
}

func strPtrLocal(text string) *string { return &text }

func i64PtrLocal(value int64) *int64 { return &value }

// proxyAnswer is one answered proxied request as the CONNECT tests read it.
type proxyAnswer struct {
	status int
	header http.Header
	body   string
}

// postViaProxy sends one POST through a proxy client.
func postViaProxy(client *http.Client, rawURL, body string, headers map[string]string) (proxyAnswer, error) {
	return doViaProxy(http.MethodPost, client, rawURL, body, headers)
}

func doViaProxy(method string, client *http.Client, rawURL, body string, headers map[string]string) (proxyAnswer, error) {
	request, err := http.NewRequest(method, rawURL, strings.NewReader(body))
	if err != nil {
		return proxyAnswer{}, err
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	answer, err := client.Do(request)
	if err != nil {
		return proxyAnswer{}, err
	}
	defer answer.Body.Close()
	text, err := io.ReadAll(answer.Body)
	if err != nil {
		return proxyAnswer{status: answer.StatusCode, header: answer.Header}, err
	}
	return proxyAnswer{status: answer.StatusCode, header: answer.Header, body: string(text)}, nil
}

// connectThrough opens one CONNECT tunnel to target through the port's proxy and answers the
// proxy's own response.
func connectThrough(t *testing.T, port int, target string) (net.Conn, *http.Response) {
	t.Helper()
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	request := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	answer, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	return conn, answer
}

// refuseBody is the body of a refused CONNECT, read to the end.
func refuseBody(t *testing.T, answer *http.Response) string {
	t.Helper()
	text, err := io.ReadAll(answer.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(text)
}
