// Package insights turns what huddle already counts into one-sentence tips: pure rules over
// tool runs, permission asks, lane requests and needs-you items, no model. A tip's id is stable
// for one occurrence, so a dismissal holds until the same thing happens again.
package insights

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"
)

// ToolRun is one tool call a session made, as Radar's history holds it.
type ToolRun struct {
	Session  string // the session that ran it
	Tool     string // the tool's name, "Bash"
	InputKey string // Radar's hash of the tool and its input: the same key is the same call
	Command  string // the Bash command; "" for other tools
	OK       bool   // the tool answered without an error
	Error    string // the first line of the tool's error when it failed; "" when it did not
	At       time.Time
}

// PermissionAsk is one permission prompt the hub recorded, with the answer it got.
type PermissionAsk struct {
	Session   string
	Tool      string
	Command   string // the Bash command; "" for other tools
	Decision  string // "allow", "deny", or "" while unanswered
	CreatedAt time.Time
	DecidedAt time.Time // zero while unanswered
}

// Item is one needs-you item the hub raised: a permission ask, a question, a finished turn.
type Item struct {
	Session   string
	Kind      string // "permission", "question", "turn", as the hub names them
	CreatedAt time.Time
	DecidedAt time.Time // zero while unanswered
}

// Request is one model request a lane served, as Radar's history holds it.
type Request struct {
	Provider string // the lane: "anthropic", "zai", "kimi", …
	Status   int    // the answer's HTTP status; 0 when the upstream never answered
	At       time.Time
}

// SessionSpan is one session's life: its first event to its end, a zero End while it still runs.
type SessionSpan struct {
	Session    string
	Start, End time.Time
}

// Input is everything Detect reads. Now is the clock, so the rules are pure.
type Input struct {
	Now      time.Time
	Tools    []ToolRun
	Asks     []PermissionAsk
	Requests []Request
	Items    []Item
	Spans    []SessionSpan
}

// Tip is one sentence the owner can act on, with the numbers behind it.
type Tip struct {
	ID       string // stable for one occurrence: "<rule>:<what it is about>"
	Rule     string // RuleSameCommand, RuleAlwaysAllowed, RuleToolFailing, RuleLaneFailing, RuleLongestWait or RuleLongDay
	Session  string // the session a tip is about; "" for a tip about the whole huddle
	Sentence string
	Count    int           // the runs or asks behind the tip
	Wait     time.Duration // always_allowed: the median wait; longest_wait: the wait on one item
}

const (
	RuleSameCommand   = "same_command"
	RuleAlwaysAllowed = "always_allowed"
	RuleToolFailing   = "tool_failing"
	RuleLaneFailing   = "lane_failing"
	RuleLongestWait   = "longest_wait"
	RuleLongDay       = "long_day"
)

// The points each rule fires at.
const (
	sameCommandRuns   = 4 // the runs of one input that name a kept command
	askWindow         = 7 * 24 * time.Hour
	alwaysAllowedAsks = 5                // the allowed asks that name one rule
	minMedianWait     = 30 * time.Second // the waiting that makes an allow rule worth it
)

// Detect answers every tip the input supports, ordered by rule then id.
func Detect(in Input) []Tip {
	tips := append(sameCommandTips(in.Tools), alwaysAllowedTips(in)...)
	tips = append(tips, toolFailingTips(in.Tools)...)
	tips = append(tips, laneFailingTips(in)...)
	tips = append(tips, longDayTips(in)...)
	tips = append(tips, longestWaitTips(in)...)
	slices.SortFunc(tips, func(a, b Tip) int {
		if byRule := cmp.Compare(a.Rule, b.Rule); byRule != 0 {
			return byRule
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return tips
}

// keySpan is where one input key's runs sit in a session's ordered runs. A group all in a row
// spans exactly its count; a run of another key between two of them widens the span.
type keySpan struct {
	count       int
	first, last int
}

// sameCommandTips names the command each session keeps coming back to: 4 or more runs of one
// input with a run of another key between two of them. Runs all in a row are the loop alert's
// business, not a tip.
func sameCommandTips(tools []ToolRun) []Tip {
	bySession := map[string][]ToolRun{}
	for _, run := range tools {
		bySession[run.Session] = append(bySession[run.Session], run)
	}
	var tips []Tip
	for session, runs := range bySession {
		// a fresh slice, so Detect never reorders the caller's runs
		ordered := slices.Clone(runs)
		slices.SortStableFunc(ordered, func(a, b ToolRun) int { return a.At.Compare(b.At) })
		spans := map[string]*keySpan{}
		for i, run := range ordered {
			span := spans[run.InputKey]
			if span == nil {
				span = &keySpan{first: i}
				spans[run.InputKey] = span
			}
			span.count++
			span.last = i
		}
		for key, span := range spans {
			if span.count < sameCommandRuns || span.last-span.first+1 == span.count {
				continue
			}
			tips = append(tips, Tip{
				ID:      fmt.Sprintf("%s:%s:%s", RuleSameCommand, session, key),
				Rule:    RuleSameCommand,
				Session: session,
				Sentence: fmt.Sprintf("`%s` ran %d times this session. One narrower run is faster while fixing.",
					toolName(ordered[span.first]), span.count),
				Count: span.count,
			})
		}
	}
	return tips
}

// toolName is how a run's subject reads in a sentence: the Bash command, else the tool's name.
func toolName(run ToolRun) string {
	if run.Tool == "Bash" {
		return run.Command
	}
	return run.Tool
}

// alwaysAllowedTips names a prompt the owner answers the same way every time while it keeps
// costing wait: 5 or more allowed asks of one rule key in the last 7 days whose median wait is
// over 30 s. One deny, one unanswered ask, or a short median gives no tip: a prompt that costs
// no waiting, or that the owner sometimes refuses, is doing its job.
func alwaysAllowedTips(in Input) []Tip {
	windowStart := in.Now.Add(-askWindow)
	byKey := map[string][]PermissionAsk{}
	for _, ask := range in.Asks {
		if ask.CreatedAt.Before(windowStart) || ask.CreatedAt.After(in.Now) {
			continue
		}
		key := ruleKey(ask)
		byKey[key] = append(byKey[key], ask)
	}
	var tips []Tip
	for key, asks := range byKey {
		if len(asks) < alwaysAllowedAsks {
			continue
		}
		waits, allAllowed := waitsOf(asks)
		if !allAllowed {
			continue
		}
		wait := median(waits)
		if wait <= minMedianWait {
			continue
		}
		tips = append(tips, Tip{
			ID:   fmt.Sprintf("%s:%s", RuleAlwaysAllowed, key),
			Rule: RuleAlwaysAllowed,
			Sentence: fmt.Sprintf("You allowed `%s` %d times in 7 days, waiting %s each time. An allow rule would skip the prompt.",
				key, len(asks), formatWait(wait)),
			Count: len(asks),
			Wait:  wait,
		})
	}
	return tips
}

// ruleKey is the rule one ask groups under: a Bash command's first two words stand for every
// `go test …`-shaped call, any other tool stands for itself.
func ruleKey(ask PermissionAsk) string {
	if ask.Tool != "Bash" {
		return ask.Tool
	}
	words := strings.Fields(ask.Command)
	switch len(words) {
	case 0:
		return "Bash"
	case 1:
		return fmt.Sprintf("Bash(%s:*)", words[0])
	default:
		return fmt.Sprintf("Bash(%s %s:*)", words[0], words[1])
	}
}

// waitsOf is each ask's wait, ok only when every ask in the group was allowed.
func waitsOf(asks []PermissionAsk) (waits []time.Duration, ok bool) {
	waits = make([]time.Duration, len(asks))
	for i, ask := range asks {
		if ask.Decision != "allow" {
			return nil, false
		}
		waits[i] = ask.DecidedAt.Sub(ask.CreatedAt)
	}
	return waits, true
}

// median is the middle wait, the mean of the two middle waits on an even count.
func median(waits []time.Duration) time.Duration {
	slices.Sort(waits)
	if len(waits)%2 == 1 {
		return waits[len(waits)/2]
	}
	return (waits[len(waits)/2-1] + waits[len(waits)/2]) / 2
}

// formatWait reads a wait as a person does: "2 h 5 min", "1 min 40 s" or "45 s".
func formatWait(d time.Duration) string {
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%d h %d min", int64(d/time.Hour), int64(d%time.Hour/time.Minute))
	case d >= time.Minute:
		return fmt.Sprintf("%d min %d s", int64(d/time.Minute), int64(d%time.Minute/time.Second))
	default:
		return fmt.Sprintf("%d s", int64(d/time.Second))
	}
}
