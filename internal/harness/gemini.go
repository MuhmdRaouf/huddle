// The Gemini CLI keeps its MCP servers under mcpServers in ~/.gemini/settings.json.
package harness

import "path/filepath"

// gemini is the Gemini CLI adapter.
var gemini = jsonAdapter{
	name:   "gemini",
	parent: "mcpServers",
	path: func(env Env) string {
		return filepath.Join(homeDir(env, ".gemini"), "settings.json")
	},
	detect: func(env Env) bool {
		return env.which("gemini") || dirThere(homeDir(env, ".gemini"))
	},
	build: stdioBuild,
}
