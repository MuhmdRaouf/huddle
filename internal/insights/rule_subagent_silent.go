package insights

import (
	"fmt"
	"time"
)

// The point subagent_silent fires at.
const subagentSilentAfter = 20 * time.Minute // the run time over which a start with no stop event is a tip

// subagentSilentTips names each subagent that started and has no stop event and has been
// running for more than 20 minutes: one tip per run, the id carrying the session and the
// agent, so one dismissal holds until that run goes silent again.
func subagentSilentTips(in Input) []Tip {
	var tips []Tip
	for _, run := range in.Subagents {
		if !run.Stopped.IsZero() {
			continue
		}
		age := in.Now.Sub(run.Started)
		if age <= subagentSilentAfter {
			continue
		}
		name := run.Name
		if name == "" {
			name = run.Agent
		}
		tips = append(tips, Tip{
			ID:       fmt.Sprintf("%s:%s:%s", RuleSubagentSilent, run.Session, run.Agent),
			Rule:     RuleSubagentSilent,
			Session:  run.Session,
			Sentence: fmt.Sprintf("Subagent `%s` started %s ago and has not stopped.", name, formatWait(age)),
			Count:    1,
			Wait:     age,
		})
	}
	return tips
}
