// opencode keeps its MCP servers under mcp in opencode.json, the command the whole argv array
// and the env under "environment".
package harness

import (
	"encoding/json"
	"path/filepath"
)

// opencode is the opencode adapter.
var opencode = jsonAdapter{
	name:   "opencode",
	parent: "mcp",
	path: func(env Env) string {
		return filepath.Join(xdgDir(env, "opencode"), "opencode.json")
	},
	detect: func(env Env) bool {
		return env.which("opencode") || dirThere(xdgDir(env, "opencode"))
	},
	build: openCodeBuild,
}

// openCodeEntry is the record opencode reads: a local server runs one argv.
type openCodeEntry struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command"`
	Environment map[string]string `json:"environment"`
}

// openCodeBuild marshals the opencode shape.
func openCodeBuild(e Entry) ([]byte, error) {
	env := e.Env
	if env == nil {
		env = map[string]string{}
	}
	return json.Marshal(openCodeEntry{
		Type:        "local",
		Command:     append([]string{e.Command}, e.Args...),
		Environment: env,
	})
}
