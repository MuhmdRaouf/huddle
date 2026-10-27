package insights

import (
	"fmt"
	"time"
)

// The points context_checkpoint fires at.
const (
	contextCheckpointPercent = 70               // the context depth the rule fires strictly past
	contextCheckpointQuiet   = 30 * time.Minute // the newest commit or handoff age under which the session is already wrapping up
	contextCheckpointLive    = 2 * time.Hour    // the newest report age over which the session is no longer live
)

// contextCheckpointTips gives each live session deep in its window with nothing fresh to show
// for it one tip: its context is strictly past 70%, it reported within the last 2 h, and its
// newest commit and its newest handoff digest are both older than 30 min (or the window saw
// none). A session that committed or handed off recently is already wrapping up.
func contextCheckpointTips(in Input) []Tip {
	var tips []Tip
	for _, cp := range in.Checkpoints {
		if cp.ContextPercent <= contextCheckpointPercent {
			continue
		}
		if in.Now.Sub(cp.LastEventAt) > contextCheckpointLive {
			continue
		}
		if !cp.LastCommitAt.IsZero() && in.Now.Sub(cp.LastCommitAt) <= contextCheckpointQuiet {
			continue
		}
		if !cp.LastHandoffAt.IsZero() && in.Now.Sub(cp.LastHandoffAt) <= contextCheckpointQuiet {
			continue
		}
		pct := int(cp.ContextPercent)
		sentence := fmt.Sprintf("Context is at %d%% with nothing committed yet. A good point to finish this step and commit.", pct)
		if !cp.LastCommitAt.IsZero() {
			sentence = fmt.Sprintf("Context is at %d%% with nothing committed in %d min. A good point to finish this step and commit.",
				pct, int(in.Now.Sub(cp.LastCommitAt)/time.Minute))
		}
		tips = append(tips, Tip{
			ID:       fmt.Sprintf("%s:%s", RuleContextCheckpoint, cp.Session),
			Rule:     RuleContextCheckpoint,
			Session:  cp.Session,
			Sentence: sentence,
			Count:    1,
		})
	}
	return tips
}
