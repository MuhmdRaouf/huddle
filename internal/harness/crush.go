// Crush keeps its MCP servers under mcp in crush.json, a stdio record with the binary, its
// args and its env spelled out.
package harness

import (
	"encoding/json"
	"path/filepath"
)

// crush is the Crush adapter.
var crush = jsonAdapter{
	name:   "crush",
	parent: "mcp",
	path: func(env Env) string {
		return filepath.Join(xdgDir(env, "crush"), "crush.json")
	},
	detect: func(env Env) bool {
		return env.which("crush") || dirThere(xdgDir(env, "crush"))
	},
	build: crushBuild,
}

// crushEntry is the record Crush reads.
type crushEntry struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// crushBuild marshals the Crush shape.
func crushBuild(e Entry) ([]byte, error) {
	env := e.Env
	if env == nil {
		env = map[string]string{}
	}
	return json.Marshal(crushEntry{Type: "stdio", Command: e.Command, Args: e.Args, Env: env})
}
