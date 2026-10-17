// Claude Code's own session registry: while an interactive session runs, Claude Code keeps
// one `<config dir>/sessions/<pid>.json` describing it (session id, name, busy/idle status).
// That registry — not a SessionEnd hook — is what decides which sessions are live: a session
// radar never saw end can still be dead, and only a live pid proves an open terminal. The
// `*.key` files beside the session files hold secrets and are never read.
package ingest

import (
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/adapters/process"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// RegistryRescanMs is how often the registry is rescanned: a session's live state never lags
// more than this.
const RegistryRescanMs int64 = 5_000

// PidAlive says whether a pid belongs to a living process; injected so tests decide who is
// alive. A nil PidAlive reads the OS's own answer: signal 0, with EPERM counting as alive.
type PidAlive func(pid int64) bool

// RegistryDir is where Claude Code keeps the per-session registry files (the same config dir
// as projects/).
func RegistryDir(env domain.EnvLookup) string {
	return filepath.Join(paths.ClaudeConfigDir(env), "sessions")
}

// nonEmptyString reads one of a record's fields as a non-empty string; an empty one counts
// as absent, the way the registry's own readers guard.
func nonEmptyString(record map[string]any, key string) (string, bool) {
	value, ok := record[key].(string)
	if !ok || value == "" {
		return "", false
	}
	return value, true
}

// stringPtr reads a value as a non-empty string, else nothing.
func stringPtr(value any) *string {
	if text, ok := value.(string); ok && text != "" {
		return &text
	}
	return nil
}

// RegistrySessionOf reads one registry file's text as a session entry; ok is false when the
// text is not one (broken JSON, a scalar, a missing session id, a pid that is not a positive
// whole number).
func RegistrySessionOf(text string) (shared.RegistrySession, bool) {
	value, err := domain.ParseJSON(text)
	if err != nil {
		return shared.RegistrySession{}, false
	}
	record, ok := value.(map[string]any)
	if !ok {
		return shared.RegistrySession{}, false
	}
	sessionID, ok := nonEmptyString(record, "sessionId")
	if !ok {
		return shared.RegistrySession{}, false
	}
	pid, isNumber := record["pid"].(float64)
	if !isNumber || pid != math.Trunc(pid) || pid <= 0 || pid >= math.MaxInt64 {
		return shared.RegistrySession{}, false
	}
	session := shared.RegistrySession{
		PID:        int64(pid),
		SessionID:  sessionID,
		Name:       stringPtr(record["name"]),
		NameSource: stringPtr(record["nameSource"]),
		Status:     stringPtr(record["status"]),
		Cwd:        stringPtr(record["cwd"]),
	}
	if startedAt, ok := record["startedAt"].(float64); ok &&
		startedAt >= math.MinInt64 && startedAt < math.MaxInt64 {
		session.StartedAt = ptrOf(int64(startedAt))
	}
	return session, true
}

// ptrOf is the usual pointer-of helper.
func ptrOf[T any](value T) *T { return &value }

// ReadRegistry lists every live session in the registry: each `*.json` parsed, malformed
// files skipped, and only entries whose pid is still alive kept. The `*.key` files beside
// them are never opened.
func ReadRegistry(env domain.EnvLookup, alive PidAlive) []shared.RegistrySession {
	if alive == nil {
		alive = process.IsAlive
	}
	dir := RegistryDir(env)
	names, err := os.ReadDir(dir)
	if err != nil {
		return nil // no registry (or an unreadable one): nothing is live, and no session file is invented
	}
	var entries []shared.RegistrySession
	for _, name := range names {
		if strings.HasSuffix(name.Name(), ".key") || !strings.HasSuffix(name.Name(), ".json") {
			continue
		}
		text, err := os.ReadFile(filepath.Join(dir, name.Name()))
		if err != nil {
			continue // one unreadable file never stops the rest
		}
		if entry, ok := RegistrySessionOf(string(text)); ok && alive(entry.PID) {
			entries = append(entries, entry)
		}
	}
	return entries
}
