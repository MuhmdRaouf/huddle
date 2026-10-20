package main

// The merge the event hook answers with: whatever its parts said folds into one JSON object per
// event — the parts' systemMessages joined, their context joined, plain text becoming context —
// so the parts' messages never collide and Claude Code reads one answer.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// readHookOutput is the one object the merge writes, as the test reads it.
type readHookOutput struct {
	SystemMessage string `json:"systemMessage"`
	HookSpecific  *struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// mergeOne runs the merge and decodes its one object.
func mergeOne(t *testing.T, event string, outs ...string) readHookOutput {
	t.Helper()
	raw := mergeHookOutputs(event, [][]byte{[]byte(strings.Join(outs, ""))})
	var merged readHookOutput
	if err := json.Unmarshal(raw, &merged); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return merged
}

func TestMergeHookOutputs(t *testing.T) {
	t.Run("two systemMessages join with one newline", func(t *testing.T) {
		merged := mergeOne(t, "session-start",
			`{"systemMessage":"zai: enabled again and set up again."}`+"\n",
			`{"systemMessage":"radar: watching."}`+"\n")
		if merged.SystemMessage != "zai: enabled again and set up again.\nradar: watching." {
			t.Errorf("systemMessage = %q", merged.SystemMessage)
		}
		if merged.HookSpecific != nil {
			t.Errorf("hookSpecificOutput = %+v, want none", merged.HookSpecific)
		}
	})

	t.Run("a systemMessage and an additionalContext both land", func(t *testing.T) {
		merged := mergeOne(t, "session-start",
			`{"systemMessage":"no zai key found."}`+"\n",
			`{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"the channel picture"}}`+"\n")
		if merged.SystemMessage != "no zai key found." {
			t.Errorf("systemMessage = %q", merged.SystemMessage)
		}
		if merged.HookSpecific == nil {
			t.Fatal("hookSpecificOutput is missing")
		}
		if merged.HookSpecific.HookEventName != "SessionStart" {
			t.Errorf("hookEventName = %q, want SessionStart", merged.HookSpecific.HookEventName)
		}
		if merged.HookSpecific.AdditionalContext != "the channel picture" {
			t.Errorf("additionalContext = %q", merged.HookSpecific.AdditionalContext)
		}
	})

	t.Run("plain text becomes additionalContext named after the event", func(t *testing.T) {
		merged := mergeOne(t, "subagent-stop", "zai: 1 job awaits review (/zai:review j1).\n")
		if merged.SystemMessage != "" {
			t.Errorf("systemMessage = %q", merged.SystemMessage)
		}
		if merged.HookSpecific == nil {
			t.Fatal("hookSpecificOutput is missing")
		}
		if merged.HookSpecific.HookEventName != "SubagentStop" {
			t.Errorf("hookEventName = %q, want SubagentStop", merged.HookSpecific.HookEventName)
		}
		if merged.HookSpecific.AdditionalContext != "zai: 1 job awaits review (/zai:review j1)." {
			t.Errorf("additionalContext = %q", merged.HookSpecific.AdditionalContext)
		}
	})

	t.Run("every part silent answers nothing", func(t *testing.T) {
		if got := mergeHookOutputs("session-start", [][]byte{[]byte(""), []byte("\n")}); got != nil {
			t.Errorf("merge = %q, want nothing", got)
		}
	})

	t.Run("the event's name is Claude Code's, hyphens dropped", func(t *testing.T) {
		for event, want := range map[string]string{
			"session-start":      "SessionStart",
			"user-prompt-submit": "UserPromptSubmit",
			"subagent-stop":      "SubagentStop",
			"pre-compact":        "PreCompact",
		} {
			if got := hookEventName(event); got != want {
				t.Errorf("hookEventName(%q) = %q, want %q", event, got, want)
			}
		}
	})

	t.Run("HTML stays raw in the context", func(t *testing.T) {
		raw := mergeHookOutputs("session-start", [][]byte{[]byte("agents <present> & accounted for\n")})
		if !strings.Contains(string(raw), `"additionalContext":"agents <present> & accounted for"`) {
			t.Errorf("merge escaped the text: %q", raw)
		}
	})

	t.Run("the whole event's output is exactly one JSON object", func(t *testing.T) {
		merged := mergeHookOutputs("session-start", [][]byte{
			[]byte(`{"systemMessage":"a"}` + "\n"),
			[]byte("plain line\n"),
			[]byte(`{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"ctx"}}` + "\n"),
		})
		var value map[string]any
		if err := json.Unmarshal(merged, &value); err != nil {
			t.Fatalf("decode %q: %v", merged, err)
		}
		decoded := readHookOutput{}
		if json.Unmarshal(merged, &decoded) != nil ||
			decoded.SystemMessage != "a" || decoded.HookSpecific == nil ||
			decoded.HookSpecific.AdditionalContext != "plain line\nctx" {
			t.Errorf("merge = %q, want both fields with the text joined", merged)
		}
	})
}

// TestEventHookWritesOneObject drives the wired event hook: two parts that both speak fold into
// the one object on stdout, and nothing is written when both are silent.
func TestEventHookWritesOneObject(t *testing.T) {
	hook := eventHook("session-start")
	var stdout, stderr bytes.Buffer
	code := hook(t.Context(), nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr %q", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("silent session wrote %q", stdout.String())
	}
}
