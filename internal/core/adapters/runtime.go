package adapters

import (
	"fmt"
	"os"
)

// version is the engine's version: "dev" until SetVersion stamps it.
var version = "dev"

// SetVersion stamps the version the engine's runtime lines report.
func SetVersion(v string) { version = v }

// Runtime names what the CLI runs on, for setup's runtime line: plugin-core is one static binary
// that runs on no runtime, so the line names the binary itself with its version and path
// (runtimeLabel's "name version (path)").
func Runtime() string {
	self, err := os.Executable()
	path := ""
	if err == nil {
		path = self
	}
	return RuntimeLabel(version, path)
}

// RuntimeLabel is the runtime line for a version and executable path: "plugin-core <version>
// (<path>)", without the parentheses when there is no path to name.
func RuntimeLabel(version, path string) string {
	if path == "" {
		return "plugin-core " + version
	}
	return fmt.Sprintf("plugin-core %s (%s)", version, path)
}
