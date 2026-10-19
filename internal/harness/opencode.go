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

// openCodeEntry is the record opencode reads: a local server runs one argv. The timeout is
// opencode's per-request ms, 5000 when the entry names none — shorter than a wait's long
// poll, so the entry always names one that outlasts it.
type openCodeEntry struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command"`
	Environment map[string]string `json:"environment"`
	Timeout     int64             `json:"timeout"`
}

// openCodeRequestTimeoutMS bounds one bridge call: a wait holds its request this long, then
// answers timeout and the agent calls wait again.
const openCodeRequestTimeoutMS = 120000

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
		Timeout:     openCodeRequestTimeoutMS,
	})
}
