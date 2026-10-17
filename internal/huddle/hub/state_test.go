package hub

// The registry's state rules (SPEC 5.2): the state is computed when read, never stored. The
// table walks every rule with a fixed clock — the ages are counted from it, so nothing here
// reads the wall.

import (
	"testing"
	"time"
)

// stateNow is the clock every case ages its events against.
var stateNow = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

// ago answers a time age before the fixed clock.
func ago(age time.Duration) time.Time { return stateNow.Add(-age) }

func TestStateOfFollowsTheRulesOfTheSpec(t *testing.T) {
	cases := []struct {
		name string
		f    Facts
		want string
	}{
		// needs-you: an open needs-you item for the session
		{"an open item with a young working event", Facts{LastEvent: "UserPromptSubmit", LastEventAt: ago(5 * time.Minute), OpenItem: true}, StateNeedsYou},
		{"an open item over an idle session", Facts{LastEvent: "Stop", LastEventAt: ago(time.Minute), OpenItem: true}, StateNeedsYou},
		{"an open item over a session quiet for an hour", Facts{LastEvent: "UserPromptSubmit", LastEventAt: ago(time.Hour), OpenItem: true}, StateNeedsYou},

		// working: last event is UserPromptSubmit, PreToolUse or PostToolUse, younger than 10 min
		{"UserPromptSubmit five minutes ago", Facts{LastEvent: "UserPromptSubmit", LastEventAt: ago(5 * time.Minute)}, StateWorking},
		{"PreToolUse just now", Facts{LastEvent: "PreToolUse", LastEventAt: ago(time.Second)}, StateWorking},
		{"PostToolUse under the limit", Facts{LastEvent: "PostToolUse", LastEventAt: ago(10*time.Minute - time.Second)}, StateWorking},
		{"a working event exactly ten minutes old is no longer young", Facts{LastEvent: "UserPromptSubmit", LastEventAt: ago(10 * time.Minute)}, StateStale},

		// idle: last event is Stop or SubagentStop
		{"Stop", Facts{LastEvent: "Stop", LastEventAt: ago(time.Minute)}, StateIdle},
		{"SubagentStop", Facts{LastEvent: "SubagentStop", LastEventAt: ago(3 * time.Minute)}, StateIdle},
		{"a Stop two hours old stays idle, never stale", Facts{LastEvent: "Stop", LastEventAt: ago(2 * time.Hour)}, StateIdle},

		// stale: no event for 10 min and not idle
		{"a UserPromptSubmit eleven minutes ago", Facts{LastEvent: "UserPromptSubmit", LastEventAt: ago(11 * time.Minute)}, StateStale},
		{"a SessionStart fifteen minutes ago", Facts{LastEvent: "SessionStart", LastEventAt: ago(15 * time.Minute)}, StateStale},
		{"a Notification an hour ago", Facts{LastEvent: "Notification", LastEventAt: ago(time.Hour)}, StateStale},

		// ended: SessionEnd seen, or the machine reports the pid gone — over everything else
		{"a young SessionEnd", Facts{LastEvent: "SessionEnd", LastEventAt: ago(time.Minute), Ended: true}, StateEnded},
		{"a pid gone under a young working event", Facts{LastEvent: "UserPromptSubmit", LastEventAt: ago(time.Minute), Ended: true}, StateEnded},
		{"a pid gone with an open item", Facts{LastEvent: "PermissionRequest", LastEventAt: ago(time.Minute), OpenItem: true, Ended: true}, StateEnded},
		{"a pid gone over an idle session", Facts{LastEvent: "Stop", LastEventAt: ago(time.Minute), Ended: true}, StateEnded},

		// a young event of none of the named kinds still belongs to a live session: every
		// session reads one of the five states
		{"a SessionStart half a minute ago", Facts{LastEvent: "SessionStart", LastEventAt: ago(30 * time.Second)}, StateWorking},
		{"a Notification a minute ago", Facts{LastEvent: "Notification", LastEventAt: ago(time.Minute)}, StateWorking},
		{"a PermissionRequest just now", Facts{LastEvent: "PermissionRequest", LastEventAt: ago(2 * time.Second)}, StateWorking},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StateOf(tc.f, stateNow); got != tc.want {
				t.Errorf("StateOf(%+v) = %q, want %q", tc.f, got, tc.want)
			}
		})
	}
}
