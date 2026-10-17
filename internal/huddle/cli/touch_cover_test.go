package cli

// The paths a touch is read through: what an absolute and a relative spelling resolve to.

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolvePathAnswersAnAbsoluteCleanPath pins the resolver: an absolute path is cleaned as
// it is, a relative one is read against the working directory.
func TestResolvePathAnswersAnAbsoluteCleanPath(t *testing.T) {
	if got := resolvePath("/repo/./src/../src/a.ts"); got != filepath.Clean("/repo/src/a.ts") {
		t.Errorf("resolvePath(absolute) = %q", got)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got := resolvePath("src/a.ts"); got != filepath.Join(wd, "src/a.ts") {
		t.Errorf("resolvePath(relative) = %q, want it against %q", got, wd)
	}
}
