package convo

// The turn reader (turn.go): what a transcript says about the session's current turn — the last
// typed prompt's start, whether a turn_duration ended it, and the permission mode in force. The
// tests read the committed fixture testdata/turn.jsonl and transcripts written into temp dirs.

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// the facts the fixture holds once its torn last line is left unread: the second turn is still
// running from its own prompt, and the last permission-mode line is the mode in force
var wantFixtureFacts = TurnFacts{
	StartedAt:      "2026-10-26T09:02:00.000Z",
	PermissionMode: "bypassPermissions",
}

// turnFactsOf reads one transcript's turn facts for an assertion.
func turnFactsOf(t *testing.T, path string) TurnFacts {
	t.Helper()
	facts, err := Turn(context.Background(), path)
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	return facts
}

func TestTurnAnswersTheRunningTurnAndTheLastMode(t *testing.T) {
	facts := turnFactsOf(t, "testdata/turn.jsonl")
	if facts != wantFixtureFacts {
		t.Errorf("facts = %+v, want %+v", facts, wantFixtureFacts)
	}
}

func TestTurnAnswersTheSameFactsOverTheFileCutShort(t *testing.T) {
	// the fixture cut after its last complete line holds the same facts: the torn prompt on the
	// end changes nothing, read or not read
	whole, err := os.ReadFile("testdata/turn.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "cut.jsonl")
	if err := os.WriteFile(path, whole[:bytes.LastIndexByte(whole, '\n')+1], 0o600); err != nil {
		t.Fatal(err)
	}
	facts := turnFactsOf(t, path)
	if facts != wantFixtureFacts {
		t.Errorf("facts = %+v, want %+v", facts, wantFixtureFacts)
	}
}

func TestTurnAnswersNoTurnForATranscriptWithoutAPrompt(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-10-26T10:00:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"an answer with no prompt behind it"}]}}`,
		`{"type":"system","subtype":"turn_duration","durationMs":12000,"isMeta":false,"timestamp":"2026-10-26T10:00:12.000Z"}`,
		`{"type":"permission-mode","permissionMode":"plan","sessionId":"s-1"}`,
	)
	facts := turnFactsOf(t, path)
	want := TurnFacts{PermissionMode: "plan"}
	if facts != want {
		t.Errorf("facts = %+v, want %+v: no prompt, no turn even with a turn_duration line", facts, want)
	}
}

func TestTurnDoesNotReadATornLastLine(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"user","uuid":"u1","timestamp":"2026-10-26T10:00:00.000Z","message":{"role":"user","content":"the one real prompt"}}`,
	)
	appendRaw(t, path, `{"type":"user","uuid":"u2","timestamp":"2026-10-26T10:05:00.000Z","message":{"role":"user","content":"half a prompt"}}`)
	facts := turnFactsOf(t, path)
	want := TurnFacts{StartedAt: "2026-10-26T10:00:00.000Z"}
	if facts != want {
		t.Errorf("facts = %+v, want %+v: the half line still being written is no prompt", facts, want)
	}
}

func TestTurnAnswersTheOsErrorForAMissingFile(t *testing.T) {
	_, err := Turn(context.Background(), filepath.Join(t.TempDir(), "missing.jsonl"))
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a missing file: err = %v, want the os error", err)
	}
}
