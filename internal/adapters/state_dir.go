package adapters

import (
	"os"
	"path/filepath"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// AgentsStateDir is where a machine-wide state dir lives: ~/.agents/<name>.
func AgentsStateDir(env domain.EnvLookup, name string) string {
	return filepath.Join(homeDir(env), ".agents", name)
}

// homeDir is $HOME, else the OS's idea of the user's home (Node's homedir()); an unreadable home
// is the empty string, so the joins below stay relative rather than failing.
func homeDir(env domain.EnvLookup) string {
	if home, ok := env["HOME"]; ok {
		return home
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// exists reports whether path is there, following a symlink (Stat, not Lstat).
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
