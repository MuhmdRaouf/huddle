// The gateway server: a loopback endpoint other agents point their base URL at. It speaks the
// Anthropic Messages API and the OpenAI Chat Completions and Responses APIs, forwards each
// request unchanged to the upstream its route names, streams the answer back unchanged, and
// appends one radar route line per request with model, status, latency and token usage. Nothing
// else is kept: no request body, no header value, no byte of any answer.
package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// pluginGateway is the plugin name the gateway's route lines carry; radar reads it as the via.
const pluginGateway = "gateway"

// harnessHeader is the header a client names itself with, one word on the route line.
const harnessHeader = "X-Huddle-Harness"

// maxRequestBytes bounds what the gateway reads of a request before answering 413; a context
// window is megabytes, so a body past this is no model call.
const maxRequestBytes = 64 << 20

// hopHeaders are the connection-scoped headers that never hop between client and upstream.
var hopHeaders = map[string]bool{
	"Connection": true, "Proxy-Connection": true, "Keep-Alive": true,
	"Proxy-Authenticate": true, "Proxy-Authorization": true, "Te": true,
	"Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
}

// Server is one gateway: a config, the route-line writer, and the client it forwards with.
type Server struct {
	config Config
	// record appends one route line per request; a failing disk never reaches a client.
	record func(domain.RouteEvent)
	// now stamps the route line and times the latency; nil reads the wall clock.
	now func() time.Time
	// client forwards; a fresh one per server, never the process-wide default.
	client *http.Client
}

// NewServer builds a gateway over config. record takes one event per request; now, when nil,
// reads the wall clock.
func NewServer(config Config, record func(domain.RouteEvent), now func() time.Time) *Server {
	if now == nil {
		now = time.Now
	}
	return &Server{
		config: config,
		record: record,
		now:    now,
		client: &http.Client{},
	}
}

// kindOfPath answers which API surface a path speaks: Anthropic's `/v1/messages` and OpenAI's
// `/v1/chat/completions` and `/v1/responses`, matched by suffix so a client's version prefix
// rides along.
func kindOfPath(path string) (apiKind, bool) {
	switch {
	case strings.HasSuffix(path, "/chat/completions"):
		return kindChat, true
	case strings.HasSuffix(path, "/responses"):
		return kindResponses, true
	case strings.HasSuffix(path, "/messages"):
		return kindAnthropic, true
	}
	return "", false
}

// ServeHTTP answers one request: refused when a stranger's Host sent it, answered locally on
// the health path, forwarded everywhere else.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !loopbackHost(r.Host) {
		// A Host another machine would resolve is a rebinding attempt; the gateway is loopback only.
		w.WriteHeader(http.StatusMisdirectedRequest)
		return
	}
	if r.URL.Path == "/health" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
		return
	}
	kind, ok := kindOfPath(r.URL.Path)
	if !ok {
		http.Error(w, "gateway: this path speaks no API the gateway forwards", http.StatusNotFound)
		return
	}
	s.forward(w, r, kind)
}

// forward does the one thing the gateway is for: same bytes in, same bytes out, one line kept.
func (s *Server) forward(w http.ResponseWriter, r *http.Request, kind apiKind) {
	start := s.now()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
	if err != nil || len(body) > maxRequestBytes {
		http.Error(w, "gateway: could not read the request body", http.StatusRequestEntityTooLarge)
		return
	}
	model := modelOf(body)
	upstream := s.config.UpstreamFor(kind, model)
	if upstream == "" {
		http.Error(w, "gateway: no upstream: name one in gateway.json (a route or the family default)", http.StatusBadGateway)
		return
	}

	out, err := http.NewRequestWithContext(r.Context(), r.Method, upstream+r.URL.Path, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "gateway: could not build the upstream request", http.StatusBadGateway)
		return
	}
	if r.URL.RawQuery != "" {
		out.URL.RawQuery = r.URL.RawQuery
	}
	out.Header = forwardHeaders(r.Header)

	resp, err := s.client.Do(out)
	if err != nil {
		// A client that walked away is gone; everyone else learns the upstream never answered.
		if r.Context().Err() == nil {
			http.Error(w, "gateway: upstream unreachable: "+err.Error(), http.StatusBadGateway)
		}
		words := "upstream unreachable: " + err.Error()
		s.record(s.event(model, upstream, http.StatusBadGateway, harnessOf(r.Header), &words, nil, start))
		return
	}
	defer resp.Body.Close()

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	streaming := strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")
	finder := newUsageFinder(kind, streaming)
	_, _ = io.CopyBuffer(flushWriter{w: w}, io.TeeReader(resp.Body, finder), make([]byte, 32<<10))

	var problem *string
	if resp.StatusCode >= http.StatusBadRequest {
		words := fmt.Sprintf("upstream %d", resp.StatusCode)
		problem = &words
	}
	s.record(s.event(model, upstream, int64(resp.StatusCode), harnessOf(r.Header), problem, finder.Usage(), start))
}

// event builds one route line: the metadata only — model, upstream, status, latency, usage,
// the harness the client named. Never a body byte or another header value.
func (s *Server) event(model, upstream string, status int64, harness string, problem *string, usage *Usage, start time.Time) domain.RouteEvent {
	return domain.RouteEvent{
		Ts:        jsscalar.ISOTime(s.now()),
		Event:     domain.EventRoute,
		Plugin:    pluginGateway,
		Model:     model,
		Upstream:  upstream,
		Route:     "gateway",
		Status:    status,
		Error:     problem,
		LatencyMs: s.now().Sub(start).Milliseconds(),
		Usage:     usage.spool(),
		Harness:   harness,
		Via:       pluginGateway,
	}
}

// forwardHeaders copies a request's headers for the upstream: the hop-by-hop ones stay, and so
// do the ones the Connection header named. The Host is rebuilt from the upstream URL.
func forwardHeaders(h http.Header) http.Header {
	named := map[string]bool{}
	for _, token := range strings.Split(h.Get("Connection"), ",") {
		named[strings.ToLower(strings.TrimSpace(token))] = true
	}
	out := make(http.Header, len(h))
	for name, values := range h {
		canonical := http.CanonicalHeaderKey(name)
		if hopHeaders[canonical] || named[strings.ToLower(canonical)] {
			continue
		}
		out[canonical] = append([]string(nil), values...)
	}
	return out
}

// copyHeaders copies an answer's headers to the client, the hop-by-hop ones excepted.
func copyHeaders(dst, src http.Header) {
	for name, values := range src {
		if hopHeaders[http.CanonicalHeaderKey(name)] {
			continue
		}
		dst[name] = append([]string(nil), values...)
	}
}

// flushWriter flushes after every write, so a streamed answer moves event by event.
type flushWriter struct{ w http.ResponseWriter }

func (f flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if flusher, ok := f.w.(http.Flusher); ok {
		flusher.Flush()
	}
	return n, err
}

// modelOf reads the model id a request body names; none said reads as none.
func modelOf(body []byte) string {
	var probe struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &probe) != nil {
		return ""
	}
	return probe.Model
}

// harnessOf is the harness the client named, one word, capped; a client that names none is
// unknown.
func harnessOf(h http.Header) string {
	name := jsscalar.Trim(h.Get(harnessHeader))
	if name == "" {
		return "unknown"
	}
	return jsscalar.UTF16Slice(name, 0, 64)
}

// loopbackHost answers whether the Host header names this machine: 127.0.0.1 or localhost, any
// port.
func loopbackHost(host string) bool {
	name := host
	if at := strings.LastIndex(name, ":"); at > strings.LastIndex(name, "]") {
		name = name[:at]
	}
	name = strings.ToLower(name)
	if len(name) > 1 && name[0] == '[' && name[len(name)-1] == ']' {
		name = name[1 : len(name)-1]
	}
	return name == "127.0.0.1" || name == "localhost"
}
