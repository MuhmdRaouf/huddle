// Harness collectors: Codex rollouts and opencode's database read into the same request
// records the Claude transcripts build, tagged with the harness that made each call.
package ingest

import (
	"context"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// HarnessCollector turns one harness's local session store into request records. The
// watcher calls Collect once per rescanning pass; a collector keeps its own read positions,
// so a record already handed over never comes back.
type HarnessCollector interface {
	Collect(ctx context.Context, sinceCutoff int64) ([]shared.RequestRecord, error)
}

// maxHarnessLineBytes caps the line a collector parses: a rollout or a message payload past
// 32 MiB is skipped, never buffered into a record.
const maxHarnessLineBytes = 32 << 20
