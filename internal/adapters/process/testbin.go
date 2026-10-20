package process

import (
	"path/filepath"
	"strings"
)

// IsTestBinary says whether an executable is one `go test` built: its name ends in .test. A test
// binary started as a daemon re-runs its own suite, detached and with the environment of the
// code that reached here — on a developer machine, the owner's real home — so every daemon
// spawn refuses one.
func IsTestBinary(executable string) bool {
	return strings.HasSuffix(filepath.Base(executable), ".test")
}
