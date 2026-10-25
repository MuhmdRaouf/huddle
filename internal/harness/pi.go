// pi reads its user-level MCP servers from mcp.json in its agent directory, and a server
// there takes precedence over one an extension registers under the same name, so huddle's
// entry in that file is the one pi runs, naming pi. pi's model requests reach huddle-router
// through models.json in the same directory: a provider record for the built-in anthropic
// provider that names only baseUrl and headers keeps pi's own anthropic models and points
// them at the router's /gateway, plain HTTP on loopback, so no proxy and no CA are involved.
// The gateway forwards each request with the client's own key to Anthropic, and the harness
// header names pi on radar's route line.
package harness

import (
	"bytes"
	"encoding/json"
	"path/filepath"
)

// pi is the pi adapter: the MCP server in the agent directory's mcp.json.
var pi = jsonAdapter{
	name:   "pi",
	parent: "mcpServers",
	path: func(env Env) string {
		return filepath.Join(piAgentDir(env), "mcp.json")
	},
	detect: func(env Env) bool {
		return env.which("pi") || dirThere(homeDir(env, ".pi"))
	},
	build: piBuild,
}

// piRouting is pi's routing: providers.anthropic in the agent directory's models.json.
var piRouting = providerEdit{
	ledger: "pi-routing",
	parent: "providers",
	path: func(env Env) string {
		return filepath.Join(piAgentDir(env), "models.json")
	},
	build: piProviderBuild,
	ours:  piProviderOurs,
}

// piAgentDir answers pi's agent directory: $PI_CODING_AGENT_DIR, else ~/.pi/agent.
func piAgentDir(env Env) string {
	if dir := env.lookup("PI_CODING_AGENT_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(homeDir(env, ".pi"), "agent")
}

// piEntry is the stdio record pi's mcp.json reads. The timeout is pi's per-request limit in
// seconds, 60 when the entry names none — shorter than a wait's long poll.
type piEntry struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Timeout int64             `json:"timeout"`
}

// piBuild marshals the pi shape.
func piBuild(e Entry) ([]byte, error) {
	env := e.Env
	if env == nil {
		env = map[string]string{}
	}
	return json.Marshal(piEntry{
		Command: e.Command, Args: e.Args, Env: env, Timeout: bridgeRequestTimeoutMS / 1000,
	})
}

// piProvider is the provider record huddle writes. pi's Anthropic client posts to
// baseUrl + "/v1/messages", so the base URL is the gateway itself.
type piProvider struct {
	BaseURL string            `json:"baseUrl"`
	Headers map[string]string `json:"headers"`
}

// piProviderBuild marshals the provider record for the entry's gateway.
func piProviderBuild(entry Entry) ([]byte, error) {
	return json.Marshal(piProvider{
		BaseURL: entry.Gateway,
		Headers: map[string]string{harnessHeaderName: entry.Env["HUDDLE_HARNESS"]},
	})
}

// piProviderOurs answers whether a provider record is one huddle wrote, whatever gateway it
// names: exactly the fields huddle writes, the header naming this harness.
func piProviderOurs(raw []byte, entry Entry) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var found piProvider
	if dec.Decode(&found) != nil {
		return false
	}
	return len(found.Headers) == 1 && found.Headers[harnessHeaderName] == entry.Env["HUDDLE_HARNESS"]
}
