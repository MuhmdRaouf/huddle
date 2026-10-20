// Package fakeclaude builds the fake `claude` binary used by worker and e2e tests; the zai
// plugin's test tree carries the same fake as a script.
package fakeclaude

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// buildPackage is what Build compiles, relative to the engine root; a var so its failure path
// can be exercised.
var buildPackage = "./testkit/cmd/fakeclaude"

var (
	buildOnce sync.Once
	builtPath string
	buildErr  error
)

// Build compiles the fake claude binary once per test binary and returns its path. Tests point
// their worker's claude binary at it.
func Build(t testing.TB) string {
	t.Helper()
	buildOnce.Do(func() { builtPath, buildErr = compile(buildPackage) })
	if buildErr != nil {
		t.Fatalf("build the fake claude binary: %v", buildErr)
	}
	return builtPath
}

// Run runs a package's tests and then removes the fake binary Build compiled for them: the build
// is shared by every test in the binary, so no single test's cleanup may own it. A package that
// calls Build runs its tests through Run from its TestMain.
func Run(m *testing.M) int {
	code := m.Run()
	if builtPath != "" {
		_ = os.RemoveAll(filepath.Dir(builtPath))
	}
	return code
}

// compile builds pkg from the module root into a fresh temp directory.
func compile(pkg string) (string, error) {
	root, err := moduleRoot()
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "fakeclaude-")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "fakeclaude")
	build := exec.Command("go", "build", "-o", bin, pkg)
	build.Dir = root
	// CGO_ENABLED=0 keeps the helper a static, pure-Go binary.
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CGO_ENABLED=") {
			env = append(env, kv)
		}
	}
	build.Env = append(env, "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("go build %s: %v: %s", pkg, err, out)
	}
	return bin, nil
}

// moduleRoot walks up from the working directory to the go.mod and steps into the engine root
// (internal/core), where Build compiles from: go test runs each package's binary with the
// package directory as its cwd.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "internal", "core"), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above %s", dir)
		}
		dir = parent
	}
}
