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
		// ended outranks everything: a SessionEnd seen, or the machine reports the pid gone
		{"a young SessionEnd", Facts{LastEvent: "SessionEnd", LastEventAt: ago(time.Minute), Ended: true}, StateEnded},
		{"a pid gone under a young working event", Facts{LastEvent: "UserPromptSubmit", LastEventAt: ago(time.Minute), Ended: true}, StateEnded},
		{"a pid gone with an open item", Facts{LastEvent: "PermissionRequest", LastEventAt: ago(time.Minute), OpenItem: true, Ended: true}, StateEnded},
		{"a pid gone over an idle session", Facts{LastEvent: "Stop", LastEventAt: ago(time.Minute), Ended: true}, StateEnded},
		{"a pid gone over a permission request", Facts{LastEvent: "PermissionRequest", LastEventAt: ago(time.Minute), Ended: true}, StateEnded},

		// needs-you: an open needs-you item for the session
		{"an open item with a young working event", Facts{LastEvent: "UserPromptSubmit", LastEventAt: ago(5 * time.Minute), OpenItem: true}, StateNeedsYou},
		{"an open item over an idle session", Facts{LastEvent: "Stop", LastEventAt: ago(time.Minute), OpenItem: true}, StateNeedsYou},
		{"an open item over a session quiet for an hour", Facts{LastEvent: "UserPromptSubmit", LastEventAt: ago(time.Hour), OpenItem: true}, StateNeedsYou},

		// needs-you: the last event itself waits on the owner — a permission request, or a
		// Notification that asks — with no later prompt or tool event
		{"a PermissionRequest just now", Facts{LastEvent: "PermissionRequest", LastEventAt: ago(2 * time.Second)}, StateNeedsYou},
		{"a PermissionRequest half an hour old with nothing after", Facts{LastEvent: "PermissionRequest", LastEventAt: ago(30 * time.Minute)}, StateNeedsYou},
		{"a Notification that asks", Facts{LastEvent: "Notification", LastEventAt: ago(time.Minute), Asking: true}, StateNeedsYou},
		{"a Notification that asked an hour ago and was never answered", Facts{LastEvent: "Notification", LastEventAt: ago(time.Hour), Asking: true}, StateNeedsYou},
		{"an asking Notification over an idle session", Facts{LastEvent: "Notification", LastEventAt: ago(time.Minute), Asking: true, OpenItem: true}, StateNeedsYou},

		// working: a live session whose last event is young — a prompt or tool call keeps it
		// working, and so does any other young sign of life
		{"UserPromptSubmit five minutes ago", Facts{LastEvent: "UserPromptSubmit", LastEventAt: ago(5 * time.Minute)}, StateWorking},
		{"PreToolUse just now", Facts{LastEvent: "PreToolUse", LastEventAt: ago(time.Second)}, StateWorking},
		{"PostToolUse under the limit", Facts{LastEvent: "PostToolUse", LastEventAt: ago(10*time.Minute - time.Second)}, StateWorking},
		{"a SessionStart half a minute ago", Facts{LastEvent: "SessionStart", LastEventAt: ago(30 * time.Second)}, StateWorking},
		{"a Notification a minute ago that asks nothing", Facts{LastEvent: "Notification", LastEventAt: ago(time.Minute)}, StateWorking},

		// idle: last event is Stop or SubagentStop with nothing after
		{"Stop", Facts{LastEvent: "Stop", LastEventAt: ago(time.Minute)}, StateIdle},
		{"SubagentStop", Facts{LastEvent: "SubagentStop", LastEventAt: ago(3 * time.Minute)}, StateIdle},
		{"a Stop two hours old stays idle, never stuck", Facts{LastEvent: "Stop", LastEventAt: ago(2 * time.Hour)}, StateIdle},

		// stuck: working, but no event for 10 min
		{"a UserPromptSubmit exactly ten minutes old is no longer young", Facts{LastEvent: "UserPromptSubmit", LastEventAt: ago(10 * time.Minute)}, StateStuck},
		{"a UserPromptSubmit eleven minutes ago", Facts{LastEvent: "UserPromptSubmit", LastEventAt: ago(11 * time.Minute)}, StateStuck},
		{"a SessionStart fifteen minutes ago", Facts{LastEvent: "SessionStart", LastEventAt: ago(15 * time.Minute)}, StateStuck},
		{"a Notification an hour ago that asks nothing", Facts{LastEvent: "Notification", LastEventAt: ago(time.Hour)}, StateStuck},
		{"a torn timestamp reads as long ago", Facts{LastEvent: "UserPromptSubmit", LastEventAt: time.Time{}}, StateStuck},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StateOf(tc.f, stateNow); got != tc.want {
				t.Errorf("StateOf(%+v) = %q, want %q", tc.f, got, tc.want)
			}
		})
	}
}
