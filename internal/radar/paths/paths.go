// Package paths resolves the paths radar reads and writes: the state dir everything lives
// under (RADAR_HOME, else ~/.agents/radar), the spool, port and autostart files inside it, and
// the provider plugins' state dirs.
package paths

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/adapters"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// StateDir is where everything radar writes lives (0700 dir, 0600 files); RADAR_HOME overrides it.
func StateDir(ctx context.Context, env domain.EnvLookup) string {
	if home, ok := env["RADAR_HOME"]; ok {
		return home
	}
	return AgentsStateDir(env, "radar")
}

// AgentsStateDir is the machine-wide state dir: ~/.agents/<name>.
func AgentsStateDir(env domain.EnvLookup, name string) string {
	return adapters.AgentsStateDir(env, name)
}

// SpoolDir is where the routers' request and event lines land.
func SpoolDir(ctx context.Context, env domain.EnvLookup) string {
	return filepath.Join(StateDir(ctx, env), "spool")
}

// ServerInfoPath is the live server's address and pid.
func ServerInfoPath(ctx context.Context, env domain.EnvLookup) string {
	return filepath.Join(StateDir(ctx, env), "server.json")
}

// HookErrorLog collects the one line a failing hook leaves behind.
func HookErrorLog(ctx context.Context, env domain.EnvLookup) string {
	return filepath.Join(StateDir(ctx, env), "hook-errors.log")
}

// HistoryPath is the persistent history database: past sessions, their subagents and jobs, and
// each request's text.
func HistoryPath(ctx context.Context, env domain.EnvLookup) string {
	return filepath.Join(StateDir(ctx, env), "history.db")
}

// LedgerPath is the request ledger: one row per model request, joined across the router's
// route lines and the sessions' transcripts. It sits in the state dir's ledger directory,
// which the ownership marker already counts as radar's own.
func LedgerPath(ctx context.Context, env domain.EnvLookup) string {
	return filepath.Join(StateDir(ctx, env), "ledger", "ledger.db")
}

// QuotaPath is the newest plan-limits snapshot the sessions post (Claude Code's rate limits,
// read off its statusline payload — never any credential).
func QuotaPath(ctx context.Context, env domain.EnvLookup) string {
	return filepath.Join(StateDir(ctx, env), "quota.json")
}

// ClaudeConfigDir is where Claude Code transcripts live, under $CLAUDE_CONFIG_DIR/projects
// (default ~/.claude/projects).
func ClaudeConfigDir(env domain.EnvLookup) string {
	if dir, ok := env["CLAUDE_CONFIG_DIR"]; ok {
		return dir
	}
	return filepath.Join(homeDir(env), ".claude")
}

// CodexHome is the Codex CLI's home directory: $CODEX_HOME, else ~/.codex — its rollout
// files live under sessions/. An override that names nothing counts as unset.
func CodexHome(env domain.EnvLookup) string {
	if home, ok := env["CODEX_HOME"]; ok && home != "" {
		return home
	}
	return filepath.Join(homeDir(env), ".codex")
}

// OpencodeDBPath is opencode's SQLite database: $OPENCODE_DATA names the file outright,
// else $XDG_DATA_HOME/opencode/opencode.db, else ~/.local/share/opencode/opencode.db. An
// override that names nothing counts as unset.
func OpencodeDBPath(env domain.EnvLookup) string {
	if file, ok := env["OPENCODE_DATA"]; ok && file != "" {
		return file
	}
	if data, ok := env["XDG_DATA_HOME"]; ok && data != "" {
		return filepath.Join(data, "opencode", "opencode.db")
	}
	return filepath.Join(homeDir(env), ".local", "share", "opencode", "opencode.db")
}

// ZaiStateDir is the zai plugin's state dir, the one its jobs live in.
func ZaiStateDir(ctx context.Context, env domain.EnvLookup) string {
	return ProviderStateDir(ctx, env, "zai")
}

// ProviderStateDir is a provider plugin's state dir: $<NAME>_STATE_DIR, else ~/.agents/<name> —
// where its jobs live. It resolves through the same state-root rule the plugin's hooks, commands
// and router use, so radar always reads the dir they write.
func ProviderStateDir(ctx context.Context, env domain.EnvLookup, name string) string {
	return adapters.ResolveStateRoot(ctx, env, adapters.StateRootRule{
		EnvVar:     strings.ToUpper(name) + "_STATE_DIR",
		DataPrefix: name + "-",
		XdgName:    name,
	}, stderrLog)
}

// StateMarker is the marker that says "this directory is radar's own state dir". Only a
// directory carrying it may ever be deleted recursively: RADAR_HOME can name anything, $HOME
// included.
const StateMarker = ".radar-state"

// ownEntries is everything radar itself puts in its state dir; a dir holding only these is ours.
var ownEntries = map[string]struct{}{
	"spool":              {},
	"server.json":        {},
	"hook-errors.log":    {},
	"autostart":          {},
	"port":               {},
	"ledger":             {},
	"budgets.json":       {},
	"budget-status.json": {},
	"settings.json":      {},
	"dismissed.json":     {},
	"notified.json":      {},
	"quota.json":         {},
	"history.db":         {},
	"history.db-wal":     {},
	"history.db-shm":     {},
}

// StateMarkerPath is where the state marker sits.
func StateMarkerPath(ctx context.Context, env domain.EnvLookup) string {
	return filepath.Join(StateDir(ctx, env), StateMarker)
}

// markStateDir marks the state dir as ours when we just created it, or when it holds nothing but
// radar's own files. A pre-existing directory with anything else in it is never marked.
func markStateDir(ctx context.Context, env domain.EnvLookup, created bool) error {
	marker := StateMarkerPath(ctx, env)
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	if !created {
		entries, err := os.ReadDir(StateDir(ctx, env))
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if _, ok := ownEntries[entry.Name()]; !ok {
				return nil // a file radar never wrote: not ours
			}
		}
	}
	return os.WriteFile(marker, []byte("radar state dir: removed with the plugin\n"), 0o600)
}

// PortPath holds the port the dashboard last listened on: kept across stops and reboots, so its
// address does not move.
func PortPath(ctx context.Context, env domain.EnvLookup) string {
	return filepath.Join(StateDir(ctx, env), "port")
}

// SavedPort is the saved port, or false when there is none yet or it cannot be read.
func SavedPort(ctx context.Context, env domain.EnvLookup) (int, bool) {
	text, err := os.ReadFile(PortPath(ctx, env))
	if err != nil {
		return 0, false
	}
	return parsePort(string(text))
}

// SavePort remembers the port the dashboard listens on; never fails (only a convenience).
func SavePort(ctx context.Context, env domain.EnvLookup, port int) {
	if !EnsureStateDirs(ctx, env) {
		return // the next start draws a port again
	}
	_ = os.WriteFile(PortPath(ctx, env), []byte(fmt.Sprintf("%d\n", port)), 0o600)
}

// WantsAutostart says whether a SessionStart hook names radar's page in the huddle hub: only when
// RADAR_AUTOSTART is 1. A session's own context already names the hub, so the line is opt-in.
func WantsAutostart(ctx context.Context, env domain.EnvLookup) bool {
	return env["RADAR_AUTOSTART"] == "1"
}

// EnsureStateDirs creates the state dir (and the spool dir) with 0700, root included; it returns
// false instead of failing when the location cannot be made.
func EnsureStateDirs(ctx context.Context, env domain.EnvLookup) bool {
	dir := StateDir(ctx, env)
	created := !statExists(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false
	}
	if err := markStateDir(ctx, env, created); err != nil {
		return false
	}
	return os.MkdirAll(SpoolDir(ctx, env), 0o700) == nil
}

// FileExists mirrors the filesystem.
func FileExists(ctx context.Context, path string) bool {
	return statExists(path)
}

// parsePort keeps a port file's number only when it is a whole live port: JavaScript's
// Number(file text), integer and 0 < port < 65536.
func parsePort(text string) (int, bool) {
	number, ok := jsNumber(jsfmt.Trim(text))
	if !ok || number != math.Trunc(number) {
		return 0, false
	}
	if number <= 0 || number >= 65536 {
		return 0, false
	}
	return int(number), true
}

// jsNumber parses the number forms Number() accepts from text a file can carry: decimal and hex
// integers, floats and exponents. Anything else — empty text, junk, underscores, the words Go's
// parser alone accepts — is not a number.
func jsNumber(text string) (float64, bool) {
	if strings.HasPrefix(text, "0x") || strings.HasPrefix(text, "0X") {
		value, err := strconv.ParseUint(text[2:], 16, 64)
		if err != nil {
			return 0, false
		}
		return float64(value), true
	}
	for _, r := range text {
		if !strings.ContainsRune("+-.0123456789eE", r) {
			return 0, false
		}
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

// homeDir is $HOME from the env, else the OS's idea of the user's home; an unreadable home is
// the empty string, so joins stay relative rather than failing.
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

// statExists reports whether path is there, following a symlink.
func statExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// stderrLog is the default log (stderr); internal/adapters and the spool have their own twins.
func stderrLog(line string) {
	fmt.Fprintln(os.Stderr, line)
}
