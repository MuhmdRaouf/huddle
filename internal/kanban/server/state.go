// Package server is the kanban board's local server: the HTTP API and the
// event stream over the watcher, and the start, stop, open and refresh
// lifecycle the cli drives.
package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/adapters/fsutil"
)

// StateDir answers the server state root: $KANBAN_HOME, else the user's
// ~/.agents/kanban-server.
func StateDir(getenv func(string) (string, bool)) string {
	if v, ok := getenv("KANBAN_HOME"); ok && v != "" {
		return v
	}
	home := ""
	if v, ok := getenv(homeEnv()); ok && v != "" {
		home = v
	}
	if home == "" {
		if userHome, err := os.UserHomeDir(); err == nil {
			home = userHome
		}
	}
	if home == "" {
		return "kanban-server"
	}
	return filepath.Join(home, ".agents", "kanban-server")
}

// homeEnv names the environment variable holding the user's home directory.
func homeEnv() string {
	return "HOME"
}

// KeyOf answers a board's state key: the first 12 hex of sha256 of its
// absolute path, so every spelling of one path answers one key.
func KeyOf(boardPath string) string {
	sum := sha256.Sum256([]byte(absPath(boardPath)))
	return hex.EncodeToString(sum[:])[:12]
}

// absPath answers the path as absolute, clean and symlink-resolved, or as clean
// as it came: the hook resolves a board from CLAUDE_PROJECT_DIR while the
// commands resolve theirs from the working directory, and the two spellings
// differ on any symlinked path (macOS spells /private/var as /var), so the
// resolution — not the spelling — decides the key and the health board.
func absPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return resolveSymlinks(abs)
}

// resolveSymlinks resolves the path, and when its end does not exist (a board
// already deleted), resolves the closest existing ancestor and reattaches the
// missing tail: a path's spelling never changes its key, alive or gone.
func resolveSymlinks(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	dir, tail := filepath.Split(path)
	if dir == "" || dir == path {
		return path
	}
	return filepath.Join(resolveSymlinks(filepath.Clean(dir)), tail)
}

// State is <key>.json: the one server a board has. The field order is the
// file's key order.
type State struct {
	Pid       int    `json:"pid"`
	Port      int    `json:"port"`
	Board     string `json:"board"`
	Version   string `json:"version"`
	Token     string `json:"token"`
	StartedAt string `json:"startedAt"`
}

// StatePath answers a board's state file.
func StatePath(dir, key string) string { return filepath.Join(dir, key+".json") }

// LogPath answers a board's server log.
func LogPath(dir, key string) string { return filepath.Join(dir, key+".log") }

// WriteState publishes the state file: one line, private, atomic.
func WriteState(ctx context.Context, dir, key string, st State) error {
	body := append(encodeCompact(st), '\n')
	_, err := fsutil.WriteFileAtomic(ctx, StatePath(dir, key), body,
		fsutil.WriteOptions{Mode: fsutil.Perm(0o600), Mkdir: true})
	return err
}

// ReadState answers the recorded state, nil when it is missing, malformed or
// holds no server.
func ReadState(dir, key string) *State {
	data, err := os.ReadFile(StatePath(dir, key))
	if err != nil {
		return nil
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil
	}
	if st.Port <= 0 || st.Board == "" {
		return nil
	}
	return &st
}

// RemoveState unlinks the state file; nothing to remove is the common case
// after a crash.
func RemoveState(dir, key string) { _ = os.Remove(StatePath(dir, key)) }

// AppendLog appends one line to <key>.log, the one trace every exit leaves.
func AppendLog(dir, key, line string) {
	if dir == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o700)
	file, err := os.OpenFile(LogPath(dir, key), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	fmt.Fprintln(file, strings.TrimRight(line, "\n"))
}
