// tmpguard runs the test suite with TMPDIR pointed at one fresh empty
// directory and fails listing every entry the suite leaves there. A test
// that keeps a temp directory of its own fails the run, so a day of runs
// cannot fill the machine's temp volume. The directory is removed after
// the run, leaks listed or not.
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
	test := exec.Command("go", append([]string{"test"}, args...)...)
	test.Stdout = stdout
	test.Stderr = stderr
	test.Env = withTmpDir(os.Environ(), guardDir)
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
	if rmErr := os.RemoveAll(guardDir); rmErr != nil {
		fmt.Fprintf(stderr, "tmpguard: remove %s: %v\n", guardDir, rmErr)
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
	return code
}

// freshGuardDir makes the run's empty TMPDIR in the system's default temp location.
func freshGuardDir() (string, error) {
	return os.MkdirTemp("", "tmpguard-")
}

// withTmpDir replaces TMPDIR in the process env, so the suite's every throwaway lands in dir.
func withTmpDir(entries []string, dir string) []string {
	kept := make([]string, 0, len(entries)+1)
	for _, entry := range entries {
		if strings.HasPrefix(entry, "TMPDIR=") {
			continue
		}
		kept = append(kept, entry)
	}
	return append(kept, "TMPDIR="+dir)
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
