package adapters

import (
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// staleTempAge is how old a temp file's mtime must be before the sweep removes it: far past any
// live write, which lasts milliseconds, so a concurrent session start's temp file is never taken.
const staleTempAge = time.Minute

// agentTempShape is the name fsutil.WriteFileAtomic gives a temp file beside "<name>.md": the
// target, ".tmp-", the writer's pid, a dash and 26 base32 characters.
var agentTempShape = regexp.MustCompile(`^.+\.md\.tmp-[0-9]+-[A-Z2-7]{26}$`)

// sweepAgentTemps removes the temp files a write killed before its rename left in the agents
// folder: regular files in agentTempShape whose mtime is older than staleTempAge before now.
// Every other name stays, and a failure to list or remove is ignored — the next pass tries again.
func sweepAgentTemps(folder string, now time.Time) {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !agentTempShape.MatchString(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) < staleTempAge {
			continue
		}
		_ = os.Remove(filepath.Join(folder, entry.Name()))
	}
}
