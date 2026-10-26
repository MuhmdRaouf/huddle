package ingest

import (
	"context"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// HubEnded answers which of the listed sessions huddle's registry has ended. Radar runs inside
// the hub, and the registry's word on an ended session stands above Claude Code's own files.
type HubEnded interface {
	EndedAmong(ctx context.Context, ids []string) (map[string]bool, error)
}

// withoutEnded answers entries less the sessions ended says have ended. A nil ended, an empty
// list or an error answers entries as they are: radar keeps its own rule when the registry
// cannot answer.
func withoutEnded(ctx context.Context, ended HubEnded, entries []shared.RegistrySession) []shared.RegistrySession {
	if ended == nil || len(entries) == 0 {
		return entries
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.SessionID)
	}
	gone, err := ended.EndedAmong(ctx, ids)
	if err != nil || len(gone) == 0 {
		return entries
	}
	kept := make([]shared.RegistrySession, 0, len(entries))
	for _, entry := range entries {
		if !gone[entry.SessionID] {
			kept = append(kept, entry)
		}
	}
	return kept
}
