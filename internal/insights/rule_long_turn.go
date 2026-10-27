package insights

import (
	"fmt"
	"time"
)

// longTurnMin is the turn age the rule fires strictly past: past it, a turn still without a
// Stop has outlived the step it started as, and its edits are work worth banking.
const longTurnMin = 45 * time.Minute

// longTurnTips gives each open turn that has run strictly past 45 minutes, edited at least one
// file and committed none of it one tip: checkpoint the work. A turn with no edit has nothing
// to commit, and a commit since the start says the turn already banked what it made.
func longTurnTips(in Input) []Tip {
	var tips []Tip
	for _, turn := range in.Turns {
		if in.Now.Sub(turn.StartedAt) <= longTurnMin {
			continue
		}
		if turn.Edits == 0 {
			continue
		}
		if !turn.LastCommitAt.IsZero() {
			continue
		}
		files := "files"
		if turn.Edits == 1 {
			files = "file"
		}
		tips = append(tips, Tip{
			ID:      fmt.Sprintf("%s:%s:%d", RuleLongTurn, turn.Session, turn.StartedAt.Unix()),
			Rule:    RuleLongTurn,
			Session: turn.Session,
			Sentence: fmt.Sprintf("This turn has edited %d %s over %d min with no commit. A good point to commit what works.",
				turn.Edits, files, int(in.Now.Sub(turn.StartedAt)/time.Minute)),
			Count: turn.Edits,
		})
	}
	return tips
}
