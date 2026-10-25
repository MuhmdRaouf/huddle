// Package hookplan holds the plan of the plugin's merged hook process: one process per Claude
// Code event runs, in order, the parts the plugin's three parts used to register as separate
// hook lines — huddle's own handler for the event, huddle's report, then radar's hook, then
// kanban's session start (SessionStart only) — and the parts of the agent-team events:
// huddle's task line and kanban's task card (TaskCreated), huddle's report (TeammateIdle). Each part owns a deadline; the parts' deadlines of
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

// Events maps every event the plugin serves to its parts, in the order they run. Each call builds
// a fresh table, so no caller can change another's plan.
func Events() map[string][]Part {
	return map[string][]Part{
		"session-start": {
			{"session-start", 3000 * time.Millisecond},
			{"report", 1900 * time.Millisecond},
			{"radar", 1600 * time.Millisecond},
			{"kanban", 2500 * time.Millisecond},
			// The five provider plugins' session-start work in one pass; a cold daemon start
			// takes what the start itself budgets (4 s).
			{"providers", 4000 * time.Millisecond},
		},
		"user-prompt-submit": {
			{"listen", 2600 * time.Millisecond},
			{"report", 1300 * time.Millisecond},
			{"radar", 600 * time.Millisecond},
		},
		"pre-tool-use": {
			{"reserve", 300 * time.Millisecond},
			{"report", 4100 * time.Millisecond},
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
			// The providers' what-did-this-agent-run-on line, the five in one pass.
			{"usage", 1500 * time.Millisecond},
		},
		"session-end": {
			{"report", 1900 * time.Millisecond},
			{"radar", 2600 * time.Millisecond},
		},
		"pre-compact": {
			{"radar", 4500 * time.Millisecond},
		},
		// Claude Code's agent-team events: a created task goes to the channel, then to the
		// project's board; a teammate about to go idle is reported under its session.
		"task-created": {
			{"task", 2000 * time.Millisecond},
			{"kanban-task", 1500 * time.Millisecond},
		},
		"teammate-idle": {
			{"report", 1900 * time.Millisecond},
		},
	}
}

// Parts answers the parts the named event runs, in order; nil for an event the plugin does not serve.
func Parts(event string) []Part {
	return Events()[event]
}

// Budget answers the deadline the named part owns on the named event.
func Budget(event, part string) (time.Duration, bool) {
	for _, listed := range Parts(event) {
		if listed.Name == part {
			return listed.Budget, true
		}
	}
	return 0, false
}
