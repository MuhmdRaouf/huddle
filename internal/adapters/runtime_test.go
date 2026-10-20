package adapters

import (
	"os"
	"testing"
)

// The runtime line names no JS runtime: huddle is one static binary, so Runtime names the
// binary itself, its build-time version and its path.

func TestRuntime(t *testing.T) {
	t.Run("defaults to this process", func(t *testing.T) {
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		want := "huddle dev (" + self + ")"
		if got := Runtime(); got != want {
			t.Fatalf("Runtime() = %q, want %q", got, want)
		}
	})
	t.Run("a version and path, or no parentheses without a path", func(t *testing.T) {
		if got := RuntimeLabel("0.1.0", "/bin/huddle"); got != "huddle 0.1.0 (/bin/huddle)" {
			t.Fatalf("RuntimeLabel(0.1.0) = %q", got)
		}
		if got := RuntimeLabel("0.1.0", ""); got != "huddle 0.1.0" {
			t.Fatalf("RuntimeLabel(0.1.0, \"\") = %q", got)
		}
	})
}
