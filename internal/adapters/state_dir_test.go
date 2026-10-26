package adapters

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

func TestHomeDir(t *testing.T) {
	t.Run("takes HOME from the env map, empty or not", func(t *testing.T) {
		if got := homeDir(domain.EnvLookup{"HOME": ""}); got != "" {
			t.Fatalf("homeDir(empty HOME) = %q, want empty", got)
		}
	})

	t.Run("falls back to the process home when the map has no HOME", func(t *testing.T) {
		t.Setenv("HOME", "/process-home")
		if got := homeDir(domain.EnvLookup{}); got != "/process-home" {
			t.Fatalf("homeDir(no map HOME) = %q, want /process-home", got)
		}
	})

	t.Run("is empty when no home can be named", func(t *testing.T) {
		t.Setenv("HOME", "/restored-later")
		if err := os.Unsetenv("HOME"); err != nil {
			t.Fatalf("unsetenv HOME: %v", err)
		}
		if got := homeDir(domain.EnvLookup{}); got != "" {
			t.Fatalf("homeDir(no HOME anywhere) = %q, want empty", got)
		}
	})
}

func TestAgentsStateDir(t *testing.T) {
	env := domain.EnvLookup{"HOME": "/h", "XDG_STATE_HOME": "/xdg"}
	if got := AgentsStateDir(env, "acme"); got != filepath.Join("/h", ".agents", "acme") {
		t.Fatalf("agentsStateDir = %q, want %q", got, filepath.Join("/h", ".agents", "acme"))
	}
}
