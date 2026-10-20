package render

import (
	"os"
	"path/filepath"
	"testing"
)

// pluginsRoot is where the provider plugins' own directories sit, relative to this package: the
// repository's plugins/ folder.
const pluginsRoot = "../../../plugins"

// requirePluginDirs skips a drift test until the provider plugin directories are in this repository;
// the tests compare the compiled provider table against them.
func requirePluginDirs(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(pluginsRoot, "zai-plugin-cc")); err != nil {
		t.Skip("the provider plugin directories are not in this repository yet")
	}
}
