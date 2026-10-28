// The Claude agents folder sync: every session start, huddle lays one tiny agent file per Claude
// model family into Claude Code's agents folder (see SyncClaudeAgents), so the agent panel names
// the model each subagent runs on. Which files exist, which names and what bytes is the pure
// package internal/agents' decision; this file only reads the folder, applies the plan and writes
// it — guarded, so a file that is the user's is never written, rewritten or removed.
package adapters

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/adapters/fsutil"
	"github.com/MuhmdRaouf/huddle/internal/agents"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// SyncClaudeAgents keeps one agent file per Claude model family in Claude Code's agents folder
// (ClaudeConfigDir(env)/agents), so the agent panel names the model each subagent runs on. It reads
// the folder's .md files, applies agents.Decide, writes with fsutil.WriteFileAtomic and removes only
// files that still carry the marker. It touches nothing else.
func SyncClaudeAgents(ctx context.Context, env domain.EnvLookup) error {
	folder := filepath.Join(ClaudeConfigDir(env), "agents")
	existing, firstErr := readAgentFiles(folder)
	// Whatever read failed, the rest of the pass still runs: the guards below keep anything that
	// could not be read from ever being replaced.
	plan := agents.Decide(agents.Newest(agents.ClaudeIDs()), existing)
	for _, file := range plan.Write {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := writeAgentFile(ctx, folder, file); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("agents: %s: %w", file.Name, err)
		}
	}
	for _, name := range plan.Remove {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := removeAgentFile(folder, name); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("agents: %s: %w", name, err)
		}
	}
	return firstErr
}

// readAgentFiles reads the regular .md files directly in the folder — no recursion, symlinks and
// directories skipped. A missing folder reads as empty; a file that vanishes between the listing
// and the read just is not there. The first failure — the folder's or a file's — is returned with
// whatever did read; the guards in writeAgentFile and removeAgentFile keep the rest safe.
func readAgentFiles(folder string) ([]agents.Existing, error) {
	entries, err := os.ReadDir(folder)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("agents: %s: %w", folder, err)
	}
	var existing []agents.Existing
	var firstErr error
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(folder, entry.Name()))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("agents: %s: %w", entry.Name(), err)
			}
			continue
		}
		existing = append(existing, agents.Existing{Name: entry.Name(), Body: raw})
	}
	return existing, firstErr
}

// writeAgentFile writes one planned file atomically with mode 0644 (a plain readable file, not a
// state file), creating the folder on the first write. The commit is guarded: between the read and
// the rename, the target must be absent or still carry the marker — a file that turned into the
// user's is never replaced.
func writeAgentFile(ctx context.Context, folder string, file agents.File) error {
	path := filepath.Join(folder, file.Name)
	_, err := fsutil.WriteFileAtomic(ctx, path, file.Body, fsutil.WriteOptions{
		Mode:  fsutil.Perm(0o644),
		Mkdir: true,
		CommitIf: func() bool {
			raw, err := os.ReadFile(path)
			if err != nil {
				return os.IsNotExist(err)
			}
			return agents.Managed(raw)
		},
	})
	return err
}

// removeAgentFile removes one planned file only after re-reading it: a file that no longer
// carries the marker is the user's now and stays.
func removeAgentFile(folder, name string) error {
	path := filepath.Join(folder, name)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !agents.Managed(raw) {
		return nil
	}
	return os.Remove(path)
}
