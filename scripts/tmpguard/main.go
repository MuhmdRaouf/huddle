// tmpguard runs the test suite with TMPDIR pointed at one fresh empty
// directory and fails listing every entry the suite leaves there. A test
// that keeps a temp directory of its own fails the run, so a day of runs
// cannot fill the machine's temp volume. The directory is removed after
// the run, leaks listed or not.
//
// It also fails on the run's own processes that outlived it (internal/testkit/leftover): an
// executable under the run's temp dir — a *.test binary, or a binary the suite built there —
// started after the run began is the suite's work still running detached. One whose HOME is a
// temp dir is killed; one whose HOME is not is reported and left alone.
//
// It also fails on a notifier the suite started: the run gets a directory of osascript,
// osacompile and notify-send shims (internal/testkit/isoenv) that the test kit puts first on the
// PATH of every process and child it builds. Each shim records its argv and fails, and every
// recorded spawn is named in the failure. A test never reaches the owner's desktop.
//
// The directory comes from the system's default temp location, never from
// inside the module: the suite's "outside a repository" cases run git and
// the project detection in temp dirs, and a temp dir under the module
// would sit inside the repository they expect to be out of.
//
// Usage: go run ./scripts/tmpguard [go test flags and packages]
// Everything it is given goes to `go test` unchanged; with no packages
// named it tests ./...
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/leftover"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	patterns := 0
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			patterns++
		}
	}
	if patterns == 0 {
		args = append(args, "./...")
	}
	guardDir, err := freshGuardDir()
	if err != nil {
		fmt.Fprintf(stderr, "tmpguard: %v\n", err)
		return 1
	}
	shimDir, err := freshShimDir()
	if err != nil {
		fmt.Fprintf(stderr, "tmpguard: %v\n", err)
		_ = os.RemoveAll(guardDir)
		return 1
	}
	began := time.Now()
	test := exec.Command("go", append([]string{"test"}, args...)...)
	test.Stdout = stdout
	test.Stderr = stderr
	test.Env = withEnv(os.Environ(), "TMPDIR="+guardDir, isoenv.ShimsEnv+"="+shimDir)
	code := 0
	if err := test.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			fmt.Fprintf(stderr, "tmpguard: run go test: %v\n", err)
			code = 1
		}
	}
	left, listErr := leftovers(guardDir)
	escaped := leftover.Sweep(guardDir, time.Since(began), stderr)
	spawns, spawnsErr := isoenv.Spawns(shimDir)
	for _, dir := range []string{guardDir, shimDir} {
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			fmt.Fprintf(stderr, "tmpguard: remove %s: %v\n", dir, rmErr)
		}
	}
	if spawnsErr != nil {
		fmt.Fprintf(stderr, "tmpguard: read the notifier record: %v\n", spawnsErr)
		return 1
	}
	if len(spawns) > 0 {
		fmt.Fprintf(stderr, "tmpguard: the suite started %d desktop notifier programs (a test must never reach the owner's desktop; HUDDLE_NOTIFY_LOG is set by internal/testkit/isoenv for every test process and child):\n", len(spawns))
		for _, spawn := range spawns {
			fmt.Fprintf(stderr, "  %s\n", spawn)
		}
		return 1
	}
	if listErr != nil {
		fmt.Fprintf(stderr, "tmpguard: %v\n", listErr)
		return 1
	}
	if len(left) > 0 {
		fmt.Fprintf(stderr, "tmpguard: the suite left %d entries in TMPDIR (%s must end empty; a test with a *testing.T takes t.TempDir(), a TestMain removes its own root):\n", len(left), guardDir)
		for _, name := range left {
			fmt.Fprintf(stderr, "  %s\n", name)
		}
		return 1
	}
	if escaped {
		return 1
	}
	return code
}

// freshGuardDir makes the run's empty TMPDIR in the system's default temp location.
func freshGuardDir() (string, error) {
	return os.MkdirTemp("", "tmpguard-")
}

// freshShimDir makes the run's notifier shims, outside the run's TMPDIR so that stays empty.
func freshShimDir() (string, error) {
	dir, err := os.MkdirTemp("", "tmpguard-shims-")
	if err != nil {
		return "", err
	}
	if err := isoenv.InstallShims(dir); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// withEnv replaces each named variable in the process env with the given NAME=value entry, so
// the suite's every throwaway lands in the run's TMPDIR and its test kit finds the run's shims.
func withEnv(entries []string, set ...string) []string {
	kept := make([]string, 0, len(entries)+len(set))
	for _, entry := range entries {
		replaced := false
		for _, with := range set {
			name, _, _ := strings.Cut(with, "=")
			if strings.HasPrefix(entry, name+"=") {
				replaced = true
			}
		}
		if !replaced {
			kept = append(kept, entry)
		}
	}
	return append(kept, set...)
}

// leftovers names the entries the suite left in the run's TMPDIR.
func leftovers(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}
