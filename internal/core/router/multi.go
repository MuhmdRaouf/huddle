// The one router every provider plugin shares: huddle-router holds one lane per enabled provider
// (the same routerWorker a per-provider router serves with) and one Anthropic lane, picks the
// lane from the request's model, and serves both base-URL mode and HTTPS-proxy mode. No peers and
// no hop: a model no lane claims goes to Anthropic, and each lane's key, budget and refusals are
// its own.
package router

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// MultiName is the router's name: in errors, in the health answer and as the plugin of the
// Anthropic lane's spool events.
const MultiName = "huddle-router"

// MultiHealthPath answers GET with the router's name and the providers it serves.
const MultiHealthPath = "/huddle-router/health"

// GatewayPrefix is the path under which the mounted gateway serves: a harness points its base
// URL at the router plus this prefix.
const GatewayPrefix = "/gateway"

// MultiOptions is what the multi router needs to know: where Anthropic is, one lane per enabled
// provider, and the plugs the multi router owns (the CA, the proxy environment, the log, the
// spool).
type MultiOptions struct {
	// Port is the canonical port the base URL names; 0 takes any Host (tests).
	Port int
	// ExtraPorts are the loopback ports this daemon also serves — the 0.0.1 ones it adopted —
	// and their Hosts pass the same guard the canonical one does.
	ExtraPorts []int
	// Anthropic is where every request for a model no lane claims goes, unchanged.
	Anthropic *url.URL
	// Lanes is one entry per enabled provider; Port, Token and Peers are ignored.
	Lanes []RouterOptions
	// CADir is the local CA's directory; "" never decrypts (every CONNECT is a blind tunnel).
	CADir string
	// Env is the proxy environment upstream requests and blind tunnels honour.
	Env ProxyEnv
	// Dial opens a blind tunnel's socket; nil dials through the user's own proxy when one applies,
	// else directly.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Log receives one line per request: method, path, model, route, status. Never headers or
	// bodies.
	Log func(line string)
	// Events receives one spool event per forwarded request and the health events, when Radar's
	// spool is reachable.
	Events func(event domain.SpoolEvent)
	// BodyLimit is the largest request body; 0 is the default 64 MiB.
	BodyLimit int64
	// Now is the clock, epoch milliseconds; nil is the wall clock.
	Now func() int64
	// Gateway serves every path under GatewayPrefix with the prefix removed; nil answers those paths 404.
	Gateway http.Handler
}

// Multi is the one router serving every provider. CreateMulti builds it; listen on the server
// (127.0.0.1) after.
type Multi struct {
	// o is what the router was built with: Reload builds fresh lanes with the same plugs.
	o MultiOptions
	// hostPorts is every loopback port a request's Host may name: the canonical one and the
	// adopted 0.0.1 ones; nil is any Host.
	hostPorts []int
	// lanes is the immutable set of provider lanes in force; Reload swaps it, and every
	// request reads it once.
	lanes     atomic.Pointer[multiLanes]
	anthropic *routerWorker // the lane for every model no provider claims
	proxy     *TLSProxy
	gateway   http.Handler // serves GatewayPrefix, or nil
	limit     int64
	// streak is the Anthropic path's failure count, shared by the base-URL mode (the Anthropic
	// lane) and the proxy mode (the decrypted route and the pass-through).
	streak *FailStreak
}

// multiLanes is one immutable set of provider lanes, in Lanes order.
type multiLanes struct{ lanes []*routerWorker }

// CreateMulti builds the multi router over the lanes given.
func CreateMulti(opts MultiOptions) (*http.Server, *Multi) {
	limit := opts.BodyLimit
	if limit == 0 {
		limit = BodyLimitBytes
	}
	now := opts.Now
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	note := opts.Log
	if note == nil {
		note = func(string) {}
	}
	m := &Multi{o: opts, limit: limit, gateway: opts.Gateway, streak: &FailStreak{}}
	if opts.Port != 0 {
		m.hostPorts = append([]int{opts.Port}, opts.ExtraPorts...)
	}
	m.lanes.Store(&multiLanes{lanes: m.buildLanes(opts.Lanes)})
	// The Anthropic lane claims nothing, so its requests always take the Anthropic route, and it
	// never reads a key (there is none to read).
	m.anthropic = newRouterWorker(RouterOptions{
		Provider:      domain.Provider{Name: MultiName, Display: "Anthropic"},
		Anthropic:     opts.Anthropic,
		Env:           opts.Env,
		Log:           opts.Log,
		Events:        opts.Events,
		BodyLimit:     opts.BodyLimit,
		AnthropicFail: m.noteAnthropicFailure,
		AnthropicOk:   m.noteAnthropicOk,
	}, nil)
	m.proxy = &TLSProxy{
		Name:          MultiName,
		CADir:         opts.CADir,
		Anthropic:     opts.Anthropic,
		Env:           opts.Env,
		Dial:          opts.Dial,
		BodyLimit:     limit,
		Now:           now,
		Note:          note,
		Route:         m.route,
		AnthropicFail: func(code string) { m.noteAnthropicFailure(code, "") },
		AnthropicOk:   m.noteAnthropicOk,
	}
	return Harden(&http.Server{Handler: http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		// A CONNECT names the tunnel's target in the authority, so the Host check below (a
		// DNS-rebinding guard for pages a browser could reach) cannot apply to it; only a proxy
		// client sends one.
		if req.Method == http.MethodConnect {
			m.proxy.Connect(rw, req)
			return
		}
		res := NewResponse(rw)
		if m.hostPorts != nil && RefuseHost(req, res, MultiName, m.hostPorts) {
			return
		}
		// The mounted gateway answers its own paths, and adds no key: the client's credentials
		// go through unchanged. The raw writer passes through, so the gateway streams through
		// its own flush writer.
		if req.URL.Path == GatewayPrefix || strings.HasPrefix(req.URL.Path, GatewayPrefix+"/") {
			if m.gateway == nil {
				AnthropicError(res, http.StatusNotFound, "not_found_error", "huddle-router: no gateway configured")
				return
			}
			http.StripPrefix(GatewayPrefix, m.gateway).ServeHTTP(rw, req)
			return
		}
		if req.Method == http.MethodGet && req.URL.Path == MultiHealthPath && req.URL.RawQuery == "" {
			jsonAnswer(res, 200, struct {
				OK        bool     `json:"ok"`
				Name      string   `json:"name"`
				Providers []string `json:"providers"`
			}{OK: true, Name: MultiName, Providers: m.Providers()})
			return
		}
		Receive(req, res, MultiName, m.limit, func(body []byte) {
			m.route(req, res, body)
		})
	})}), m
}

// noteAnthropicFailure counts one Anthropic-path upstream failure on the router's streak: the
// third within a minute raises one health event with the code and a fix hint on the log. Never
// on the request's path past a few integer operations.
func (m *Multi) noteAnthropicFailure(code, model string) {
	if !m.streak.Fail(time.Now().UnixMilli(), code) {
		return
	}
	m.noteAnthropicHealth(domain.HealthUpstreamFailing, code, model)
}

// noteAnthropicOk counts one Anthropic-path answer: the first success after a raise raises the
// recovery.
func (m *Multi) noteAnthropicOk() {
	if !m.streak.Ok(time.Now().UnixMilli()) {
		return
	}
	m.noteAnthropicHealth(domain.HealthUpstreamRecovered, "", "")
}

// noteAnthropicHealth appends one health event of the Anthropic path's own, plugin "anthropic",
// and says so on the log.
func (m *Multi) noteAnthropicHealth(event domain.RouterHealth, code, model string) {
	if m.o.Events != nil {
		m.o.Events(domain.HealthEvent("anthropic", event, code, model, time.Now().UnixMilli()))
	}
	if m.o.Log == nil {
		return
	}
	if event == domain.HealthUpstreamFailing {
		m.o.Log(fmt.Sprintf("%s: Anthropic path failing (%s): %s", MultiName, code, domain.FailHint(code)))
		return
	}
	m.o.Log(MultiName + ": Anthropic path recovered")
}

// Drain waits for the CONNECT tunnels the router carries to end, up to ctx: a server's Shutdown
// never waits on a hijacked connection.
func (m *Multi) Drain(ctx context.Context) error { return m.proxy.Drain(ctx) }

// Providers is the names of the lanes the router serves, in the order they were given.
func (m *Multi) Providers() []string {
	names := make([]string, 0, len(m.lanes.Load().lanes))
	for _, lane := range m.lanes.Load().lanes {
		names = append(names, lane.provider.Name)
	}
	return names
}

// Reload swaps in a fresh set of provider lanes, built with the router's own plugs: a request
// already routed keeps its lane, and the next one picks from the new set.
func (m *Multi) Reload(lanes []RouterOptions) {
	m.lanes.Store(&multiLanes{lanes: m.buildLanes(lanes)})
}

// buildLanes builds the workers over the lanes given, the way CreateMulti first did: the
// multi-level plugs (Anthropic, environment, log, events) are the router's, and everything the
// multi router does not serve a lane with is dropped.
func (m *Multi) buildLanes(lanes []RouterOptions) []*routerWorker {
	built := make([]*routerWorker, 0, len(lanes))
	for _, lane := range lanes {
		laneOpts := lane
		laneOpts.Port = 0
		laneOpts.Token = ""
		laneOpts.Peers = nil
		laneOpts.Anthropic = m.o.Anthropic
		laneOpts.Env = m.o.Env
		if laneOpts.Log == nil {
			laneOpts.Log = m.o.Log
		}
		if laneOpts.Events == nil {
			laneOpts.Events = m.o.Events
		}
		built = append(built, newRouterWorker(laneOpts, nil))
	}
	return built
}

// laneFor is the lane a model goes to: the one claiming the id exactly, else the one with the
// longest matching prefix, else the Anthropic lane.
func (m *Multi) laneFor(model string) *routerWorker {
	if model != "" {
		lanes := m.lanes.Load().lanes
		for _, lane := range lanes {
			for _, id := range lane.claim.IDs {
				if model == id {
					return lane
				}
			}
		}
		best, bestLength := (*routerWorker)(nil), 0
		for _, lane := range lanes {
			for _, prefix := range lane.claim.Prefixes {
				if strings.HasPrefix(model, prefix) && len(prefix) > bestLength {
					best, bestLength = lane, len(prefix)
				}
			}
		}
		if best != nil {
			return best
		}
	}
	return m.anthropic
}

// route serves one request on the lane its model picks.
func (m *Multi) route(req *http.Request, res *Response, body []byte) {
	node := parseBodyNode(body)
	m.laneFor(modelIn(node)).forwardParsed(req, res, body, node)
}

// jsonAnswer writes one JSON answer.
func jsonAnswer(res *Response, status int, value any) {
	res.Header().Set("Content-Type", "application/json")
	res.WriteHeader(status)
	res.End([]byte(marshalCompact(value)))
}

// modelOrDash is the model a log line names, "-" when the request carried none.
func modelOrDash(model string) string {
	if model == "" {
		return "-"
	}
	return model
}
