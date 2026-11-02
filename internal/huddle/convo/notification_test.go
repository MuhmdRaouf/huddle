package convo

// The task notification parser: the tags one notification carries, whichever shape its carrier
// wrote it in.

import (
	"reflect"
	"testing"
)

func TestParseNotificationReadsEveryTag(t *testing.T) {
	text := "<task-notification>\n<task-id>idl1a2b3c4</task-id>\n<tool-use-id>toolu_idle1</tool-use-id>\n<status>completed</status>\n<summary>Background command \"Idle fixture wait\" completed (exit code 3)</summary>\n</task-notification>"
	note, ok := parseNotification(text)
	if !ok {
		t.Fatalf("the notification went unparsed")
	}
	want := notification{
		TaskID:    "idl1a2b3c4",
		ToolUseID: "toolu_idle1",
		Status:    "completed",
		ExitCode:  intShellCode(3),
	}
	if !reflect.DeepEqual(note, want) {
		t.Fatalf("notification:\ngot  %+v\nwant %+v", note, want)
	}
}

func TestParseNotificationReadsAnEventWithoutAToolUse(t *testing.T) {
	text := "<task-notification>\n<task-id>mon4c5d6e</task-id>\n<event>Waiting for the fixture check</event>\n<status>running</status>\n</task-notification>"
	note, ok := parseNotification(text)
	if !ok {
		t.Fatalf("the monitor event went unparsed")
	}
	want := notification{
		TaskID: "mon4c5d6e",
		Event:  "Waiting for the fixture check",
		Status: "running",
	}
	if !reflect.DeepEqual(note, want) {
		t.Fatalf("notification:\ngot  %+v\nwant %+v", note, want)
	}
}

func TestParseNotificationRefusesOtherText(t *testing.T) {
	for _, text := range []string{
		"",
		"Carry on with the plan.",
		"the summary mentions no exit code",
		// the words alone are not a notification
		"the task-notification handling stays silent here",
	} {
		if _, ok := parseNotification(text); ok {
			t.Fatalf("parseNotification(%q) answered ok, want false", text)
		}
	}
}
