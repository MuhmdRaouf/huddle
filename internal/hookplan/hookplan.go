// Package hookplan holds the plan of the plugin's merged hook process: one process per Claude
// Code event runs, in order, the parts the plugin's three parts used to register as separate
// hook lines — huddle's own handler for the event, huddle's report, then radar's hook, then
// kanban's session start (SessionStart only). Each part owns a deadline; the parts' deadlines of
// one event sum inside the timeout hooks.json grants the event, so the merged process never
// holds a session longer than the separate processes could. approve and permit keep their own
// hook lines (they answer Claude Code, they do not only observe) and are no events here.
package hookplan

import "time"

// Part is one handler an event hook runs: its name — the word the logs, the tests and the wiring
// in cmd/huddle know it by — and the deadline it owns. A part that overruns its deadline is
// dropped with its output; the next part still runs and the exit code never changes.
type Part struct {
	Name   string
	Budget time.Duration
}

// Events maps every event the plugin serves to its parts, in the order they run.
var Events = map[string][]Part{
	"session-start": {
		{"session-start", 3000 * time.Millisecond},
		{"report", 1900 * time.Millisecond},
		{"radar", 1600 * time.Millisecond},
		{"kanban", 2500 * time.Millisecond},
	},
	"user-prompt-submit": {
		{"listen", 2600 * time.Millisecond},
		{"report", 1300 * time.Millisecond},
		{"radar", 600 * time.Millisecond},
	},
	"pre-tool-use": {
		{"report", 4500 * time.Millisecond},
	},
	"post-tool-use": {
		{"listen", 3000 * time.Millisecond},
		{"report", 1500 * time.Millisecond},
	},
	"notification": {
		{"report", 1900 * time.Millisecond},
		{"radar", 2600 * time.Millisecond},
	},
	"permission-request": {
		{"report", 4500 * time.Millisecond},
	},
	"stop": {
		{"stop", 6000 * time.Millisecond},
		{"report", 1900 * time.Millisecond},
		{"radar", 1500 * time.Millisecond},
	},
	"subagent-start": {
		{"radar", 4500 * time.Millisecond},
	},
	"subagent-stop": {
		{"report", 1900 * time.Millisecond},
		{"radar", 2600 * time.Millisecond},
	},
	"session-end": {
		{"report", 1900 * time.Millisecond},
		{"radar", 2600 * time.Millisecond},
	},
	"pre-compact": {
		{"radar", 4500 * time.Millisecond},
	},
}

// Budget answers the deadline the named part owns on the named event.
func Budget(event, part string) (time.Duration, bool) {
	for _, listed := range Events[event] {
		if listed.Name == part {
			return listed.Budget, true
		}
	}
	return 0, false
}
