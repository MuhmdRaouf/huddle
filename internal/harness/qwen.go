// Qwen Code is a Gemini CLI fork: the same mcpServers table in ~/.qwen/settings.json.
package harness

import "path/filepath"

// qwen is the Qwen Code adapter.
var qwen = jsonAdapter{
	name:   "qwen",
	parent: "mcpServers",
	path: func(env Env) string {
		return filepath.Join(homeDir(env, ".qwen"), "settings.json")
	},
	detect: func(env Env) bool {
		return env.which("qwen") || dirThere(homeDir(env, ".qwen"))
	},
	build: stdioBuild,
}
