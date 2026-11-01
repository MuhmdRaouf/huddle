package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// idleSessionNoise is everything a session id may not carry in a file name; it is the feed's key
// rule, so the waiter reads the feed's own file.
var idleSessionNoise = regexp.MustCompile(`[^\w.-]+`)

func idleSessionKey(session string) string { return idleSessionNoise.ReplaceAllString(session, "") }

// seenCursor answers the last seq of a channel the session's feed has shown, read from the
// feed's file (<home>/seen/<session>.json). The waiter only reads it: moving it would take an
// event away from the next prompt's feed.
func seenCursor(home, session, channel string) (int64, bool) {
	return cursorIn(filepath.Join(home, "seen", idleSessionKey(session)+".json"), channel)
}

// wokenCursor answers the last seq of a channel an earlier waiter of the session delivered
// (<home>/woken/<session>.json). A session that answers a wake without a tool call leaves the
// feed's cursor behind, and this one keeps the next waiter from waking on the same event.
func wokenCursor(home, session, channel string) (int64, bool) {
	return cursorIn(filepath.Join(home, "woken", idleSessionKey(session)+".json"), channel)
}

// wokenKeep is how long a session's woken file stays after it was last written.
const wokenKeep = 30 * 24 * time.Hour

// saveWokenCursor records the last seq delivered in a channel, keeping the session's other
// channels, and removes the woken files nobody has written for thirty days. A failure to write
// costs one repeated wake at worst.
func saveWokenCursor(home, session, channel string, seq int64, now time.Time) {
	file := filepath.Join(home, "woken", idleSessionKey(session)+".json")
	cursors := cursorsIn(file)
	if cursors[channel] >= seq {
		return
	}
	cursors[channel] = seq
	raw, err := json.Marshal(cursors)
	if err != nil || os.MkdirAll(filepath.Dir(file), 0o755) != nil {
		return
	}
	tmp := file + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, file)
	}
	pruneWoken(filepath.Dir(file), now)
}

// pruneWoken removes the cursor files in dir last written more than wokenKeep before now.
func pruneWoken(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err == nil && strings.HasSuffix(entry.Name(), ".json") && now.Sub(info.ModTime()) > wokenKeep {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

// cursorsIn reads one cursor file: the last seq per channel; none when it is missing or garbled.
func cursorsIn(file string) map[string]int64 {
	cursors := map[string]int64{}
	if raw, err := os.ReadFile(file); err == nil {
		if json.Unmarshal(raw, &cursors) != nil || cursors == nil {
			return map[string]int64{}
		}
	}
	return cursors
}

func cursorIn(file, channel string) (int64, bool) {
	seq, ok := cursorsIn(file)[channel]
	return seq, ok
}

// waiterFiles keeps one pid file per session, so one waiter listens for a session at a time.
type waiterFiles struct {
	dir       string
	commandOf func(pid int) (string, bool)
	stop      func(pid int)
}

// waiterRecord is a pid file's content.
type waiterRecord struct {
	PID     int    `json:"pid"`
	Started string `json:"started"`
	Session string `json:"session"`
}

func (f waiterFiles) path(session string) string {
	return filepath.Join(f.dir, idleSessionKey(session)+".json")
}

func (f waiterFiles) read(session string) waiterRecord {
	var record waiterRecord
	if raw, err := os.ReadFile(f.path(session)); err == nil {
		_ = json.Unmarshal(raw, &record)
	}
	return record
}

// isIdleWaitCommand says whether a command line is a waiter's: huddle's hook word and the
// idle-wait event, however the launcher reached the binary.
func isIdleWaitCommand(command string) bool {
	return strings.HasSuffix(strings.TrimSpace(command), "hook idle-wait")
}

// claim makes the pid the session's waiter: it stops the waiter the pid file names, but only
// when that pid's command line is an idle-wait hook (a pid alone proves nothing: it may have
// been reused), then writes the new pid file.
func (f waiterFiles) claim(session string, pid int, now time.Time) error {
	if err := os.MkdirAll(f.dir, 0o755); err != nil {
		return err
	}
	if prev := f.read(session); prev.PID > 0 && prev.PID != pid && prev.Session == session {
		if command, ok := f.commandOf(prev.PID); ok && isIdleWaitCommand(command) {
			f.stop(prev.PID)
		}
	}
	raw, err := json.Marshal(waiterRecord{PID: pid, Started: jsscalar.ISOTime(now), Session: session})
	if err != nil {
		return err
	}
	tmp := f.path(session) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, f.path(session))
}

// release removes the pid file when it still names this pid: a newer waiter's file stays.
func (f waiterFiles) release(session string, pid int) {
	if f.read(session).PID == pid {
		_ = os.Remove(f.path(session))
	}
}

// stopWaiter ends a verified waiter with SIGTERM and gives it two seconds to go.
func stopWaiter(pid int) {
	if syscall.Kill(pid, syscall.SIGTERM) != nil {
		return
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && syscall.Kill(pid, 0) == nil {
		time.Sleep(20 * time.Millisecond)
	}
}
