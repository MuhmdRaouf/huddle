// The huddle-router rig: one recording upstream per provider lane, one for Anthropic, each lane
// with its own key func, and one collector for every spool event the router writes. The scenarios
// are multi_scenario_test.go.
package router

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// kimiProvider is the second lane: the zai provider renamed, claiming kimi-main, kimi-flash and
// the kimi- prefix.
func kimiProvider() domain.Provider {
	provider := routerProvider
	provider.Name = "kimi"
	provider.Display = "Kimi"
	provider.Slash = "/kimi:"
	provider.AgentPrefix = "kimi:"
	provider.BranchPrefix = "kimi/"
	provider.Harness = "kimi"
	provider.ArtifactsEnv = "KIMI_ARTIFACTS"
	provider.EnvPrefix = "KIMI"
	provider.KeyEnv = []string{"KIMI_API_KEY"}
	provider.BillingURL = "https://platform.moonshot.ai"
	provider.WebSearchModel = nil
	provider.Catalog = map[domain.ModelTier]domain.ModelRef{
		domain.ModelTierMain:  {Tier: domain.ModelTierMain, ID: "kimi-main", Label: "Kimi Main"},
		domain.ModelTierFlash: {Tier: domain.ModelTierFlash, ID: "kimi-flash", Label: "Kimi Flash"},
	}
	provider.Router = domain.ProviderRouter{
		ModelPrefixes: []string{"kimi-"},
	}
	return provider
}

// multiLane is one provider lane of the rig: what its upstream saw and how often its key func ran.
type multiLane struct {
	provider domain.Provider
	seen     *seenList
	upstream *url.URL
	keyCalls *atomic.Int64
}

// multiRig is the huddle-router on a reserved port: one recording upstream per lane, one for
// Anthropic, and one collector for every spool event the router writes.
type multiRig struct {
	t             *testing.T
	port          int
	lanes         []*multiLane
	anthropic     *url.URL
	anthropicSeen *seenList
	events        *eventLog
}

// multiLaneOpts is one lane a scenario builds: its provider, the key its func reads and the
// budget stop in force.
type multiLaneOpts struct {
	provider domain.Provider
	key      string
	budget   func() (domain.BudgetStop, bool)
	// upstream replaces the lane's recording upstream (seen stays empty); nil keeps it.
	upstream *url.URL
	// fallback is the lane's fallback list.
	fallback []string
}

// multiOpts is what a scenario changes about the rig.
type multiOpts struct {
	lanes []multiLaneOpts
	// portSet: the router answers its reserved port's Host only (otherwise it takes any Host).
	portSet bool
	// gateway is the handler mounted under the router's gateway prefix; nil mounts none.
	gateway http.Handler
	// anthropic replaces the rig's recording Anthropic upstream (the fail-alert scenarios
	// point it at one that fails); nil keeps the recording one.
	anthropic *url.URL
	// log is the router's own log sink; nil discards the lines.
	log func(line string)
}

// newMultiRig builds the router over the lanes given (zai and kimi when none are named) and
// serves it on a reserved port.
func newMultiRig(t *testing.T, opts multiOpts) *multiRig {
	t.Helper()
	lanes := opts.lanes
	if lanes == nil {
		lanes = []multiLaneOpts{{provider: routerProvider, key: "zai-secret"}, {provider: kimiProvider(), key: "kimi-secret"}}
	}
	r := &multiRig{t: t, anthropicSeen: &seenList{}, events: &eventLog{}}
	r.anthropic = recordingUpstream(t, "anthropic", "", r.anthropicSeen)
	if opts.anthropic != nil {
		r.anthropic = opts.anthropic
	}
	laneOptions := make([]RouterOptions, 0, len(lanes))
	for _, lane := range lanes {
		calls := &atomic.Int64{}
		seen := &seenList{}
		upstream := lane.upstream
		if upstream == nil {
			upstream = recordingUpstream(t, lane.provider.Name, "/api/anthropic", seen)
		}
		key := lane.key
		r.lanes = append(r.lanes, &multiLane{provider: lane.provider, seen: seen, upstream: upstream, keyCalls: calls})
		laneOptions = append(laneOptions, RouterOptions{
			Provider:    lane.provider,
			ProviderURL: upstream,
			Key: func(context.Context) (string, error) {
				calls.Add(1)
				return key, nil
			},
			Budget:   lane.budget,
			Fallback: lane.fallback,
		})
	}
	r.port = reservePort(t)
	multiPort := 0
	if opts.portSet {
		multiPort = r.port
	}
	server, _ := CreateMulti(MultiOptions{
		Port:      multiPort,
		Anthropic: r.anthropic,
		Lanes:     laneOptions,
		Events:    r.events.record,
		Gateway:   opts.gateway,
		Log:       opts.log,
	})
	serveRouter(t, server, r.port)
	return r
}

// laneOf is the rig's lane of one provider name.
func (r *multiRig) laneOf(name string) *multiLane {
	r.t.Helper()
	for _, lane := range r.lanes {
		if lane.provider.Name == name {
			return lane
		}
	}
	r.t.Fatalf("no lane named %s", name)
	return nil
}

// postModel sends one model request the way Claude Code does and returns the whole answer. An
// empty model sends a body without a model at all.
func (r *multiRig) postModel(t *testing.T, path, model string) answer {
	t.Helper()
	body := "{}"
	if model != "" {
		body = fmt.Sprintf(`{"model":%q,"messages":[]}`, model)
	}
	got, err := postRawBody(r.port, path, body, map[string]string{
		"Content-Type":   "application/json",
		"Authorization":  "Bearer anthropic-oauth",
		"X-Api-Key":      "anthropic-key",
		"Anthropic-Beta": "claude-code-20250219",
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// proxyClientAt is an HTTP client the way Claude Code runs in proxy mode: the router at
// 127.0.0.1:port as its proxy, the router's own CA as a root.
func proxyClientAt(port int, roots *x509.CertPool) *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			Proxy: func(*http.Request) (*url.URL, error) {
				return &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", port)}, nil
			},
			TLSClientConfig:   &tls.Config{RootCAs: roots},
			DisableKeepAlives: true,
		},
	}
}
