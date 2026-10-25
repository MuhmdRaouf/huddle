package insights

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// The points tool_failing fires at.
const (
	toolFailingRuns   = 3                // the failures in a row that make one tip
	toolFailingWindow = 15 * time.Minute // how long a sequence may take, first to last
	toolFailingShape  = 60               // how much of an error's first line stands for the error
)

// toolFailingTips names a tool a session keeps retrying through the same failure: 3 or more
// consecutive runs of one tool, all failed with the same error shape, the last within 15
// minutes of the first. A run of another tool, a success of the tool, or a different shape
// ends a sequence, and separate sequences are separate tips.
func toolFailingTips(tools []ToolRun) []Tip {
	bySession := map[string][]ToolRun{}
	for _, run := range tools {
		bySession[run.Session] = append(bySession[run.Session], run)
	}
	var tips []Tip
	for session, runs := range bySession {
		// a fresh slice, so Detect never reorders the caller's runs
		ordered := slices.Clone(runs)
		slices.SortStableFunc(ordered, func(a, b ToolRun) int { return a.At.Compare(b.At) })
		for i := 0; i < len(ordered); {
			j := i + 1
			for j < len(ordered) && sameFailure(ordered[j], ordered[i]) {
				j++
			}
			if count := j - i; count >= toolFailingRuns && ordered[j-1].At.Sub(ordered[i].At) <= toolFailingWindow {
				tips = append(tips, Tip{
					ID:      fmt.Sprintf("%s:%s:%s:%d", RuleToolFailing, session, ordered[i].Tool, ordered[i].At.Unix()),
					Rule:    RuleToolFailing,
					Session: session,
					Sentence: fmt.Sprintf("%s failed %d times in a row with %q. Read the first error before retrying.",
						failingSubject(ordered[i]), count, errorShape(ordered[i])),
					Count: count,
				})
			}
			i = j
		}
	}
	return tips
}

// sameFailure is whether one run would keep the sequence another run's failure started: the
// same tool, failed, and the same error shape.
func sameFailure(run, first ToolRun) bool {
	return run.Tool == first.Tool && !run.OK && errorShape(run) == errorShape(first)
}

// failingSubject is how a failing run's subject reads in a sentence: the Bash command in
// backticks when there is one, else the tool's name.
func failingSubject(run ToolRun) string {
	if run.Tool == "Bash" && run.Command != "" {
		return "`" + run.Command + "`"
	}
	return run.Tool
}

// errorShape is the error's first line, cut to 60 characters, so retries of one failure share
// a shape while different failures do not.
func errorShape(run ToolRun) string {
	line := run.Error
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	if runes := []rune(line); len(runes) > toolFailingShape {
		line = string(runes[:toolFailingShape])
	}
	return line
}
