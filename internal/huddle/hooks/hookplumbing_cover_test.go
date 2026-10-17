package hooks

// The hooks' shared plumbing at its edges: a stop with no identity, a record that cannot be
// written, a courtesy log whose home is a file, stdin that says nothing, an answer that cannot
// be encoded, a session start with no server and one that refuses the join, and a subagent's
// plain tool call.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

func TestTheStopHookWithNoChannelOrIdentityEndsAtOnce(t *testing.T) {
	for _, env := range []map[string]string{
		{"HUDDLE_CHANNEL": "", "HUDDLE_AS": "me"},
		{"HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": ""},
	} {
		stdout, _, _ := runHook(t, "stop", `{"session_id":"s-1"}`, env)
		if stdout != "" {
			t.Errorf("stop with %v printed %q, want silence", env, stdout)
		}
	}
}

func TestAStopRecordThatCannotBeWrittenNeverBlocks(t *testing.T) {
	// the write itself fails: the state dir is read-only
	locked := t.TempDir()
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	if writeRaised(filepath.Join(locked, "stop-raised.json"), jsfmt.Object(nil, nil), "c/s", []int64{7}) {
		t.Error("writeRaised in a read-only home answered true, want false")
	}
	// a key that is not an object, and no value at all, are simply not seen
	if _, ok := fieldOf(nil, "key"); ok {
		t.Error("fieldOf(nil) answered ok")
	}
	if _, ok := fieldOf(jsfmt.Array(nil), "key"); ok {
		t.Error("fieldOf of an array answered ok")
	}
	if value, ok := fieldOf(mustObject(t, `{"key":[1,2]}`), "missing"); ok || value != nil {
		t.Error("fieldOf of a missing key answered a value")
	}
}

// mustObject decodes one JSON object for the fieldOf cases.
func mustObject(t *testing.T, raw string) *jsfmt.Value {
	t.Helper()
	value, err := jsfmt.DecodeValue([]byte(raw))
	if err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return value
}

func TestTheCourtesyLogSurvivesAHomeThatIsAFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := &cli.World{Getenv: func(key string) string {
		if key == "HUDDLE_HOME" {
			return file
		}
		return ""
	}}
	LogHookError(context.Background(), w, "probe", fmt.Errorf("boom")) // silence is right
}

func TestReadInputKeepsItsDestinationOnNothingAndOnSilence(t *testing.T) {
	var fromNothing hookInput
	readInput(strings.NewReader(""), 50, &fromNothing)
	if fromNothing != (hookInput{}) {
		t.Errorf("input = %+v, want it untouched by an empty stdin", fromNothing)
	}
	pipeReader, pipeWriter := io.Pipe()
	t.Cleanup(func() { _ = pipeReader.Close(); _ = pipeWriter.Close() })
	var fromSilence hookInput
	t0 := time.Now()
	readInput(pipeReader, 30, &fromSilence)
	if elapsed := time.Since(t0); elapsed > 2*time.Second {
		t.Errorf("readInput took %s, want it to give up at its deadline", elapsed)
	}
	if fromSilence != (hookInput{}) {
		t.Errorf("input = %+v, want it untouched by a silent stdin", fromSilence)
	}
}

func TestAnAnswerThatCannotBeEncodedPrintsNothing(t *testing.T) {
	var out strings.Builder
	writeJSON(&out, make(chan int))
	if out.Len() != 0 {
		t.Errorf("writeJSON of the unwritable printed %q, want silence", out.String())
	}
}

func TestTheSessionStartHookWithNoServerSaysSoAndNamesNoAddress(t *testing.T) {
	stdout, elapsed, _ := runHook(t, "session-start", `{"source":"startup","session_id":"s-x"}`,
		map[string]string{"HUDDLE_URL": "", "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "me"})
	if elapsed > 5*time.Second {
		t.Errorf("session-start took %s, want it inside its budget", elapsed)
	}
	neverBlocks(t, stdout)
	if !strings.Contains(stdout, "does not answer") || !strings.Contains(stdout, "none yet") {
		t.Errorf("the hook said %q, want the no-address line", stdout)
	}
}

func TestTheSessionStartHookSaysAJoinFailureAndItsExtraChannels(t *testing.T) {
	t.Run("a join the server refuses is named, not worked around", func(t *testing.T) {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"error":"the channel is closed"}`)
		}))
		t.Cleanup(s.Close)
		stdout, _, _ := runHook(t, "session-start", `{"source":"startup","session_id":"s-x"}`,
			map[string]string{"HUDDLE_URL": s.URL, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "me"})
		neverBlocks(t, stdout)
		if !strings.Contains(stdout, "joining channel hooks as me failed: the channel is closed") {
			t.Errorf("the hook said %q, want the join failure", stdout)
		}
	})
	t.Run("a joined start names the extra channels it listens in", func(t *testing.T) {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"result":{},"text":"you are in"}`)
		}))
		t.Cleanup(s.Close)
		stdout, _, _ := runHook(t, "session-start", `{"source":"startup","session_id":"s-x"}`,
			map[string]string{"HUDDLE_URL": s.URL, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "me",
				"HUDDLE_LISTEN": "other"})
		neverBlocks(t, stdout)
		if !strings.Contains(stdout, "and in other") {
			t.Errorf("the hook said %q, want the extra channel named", stdout)
		}
	})
}

func TestASubagentsPlainToolCallTakesNoFeed(t *testing.T) {
	input := `{"session_id":"s-sub","agent_id":"ag-1","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`
	stdout, _, _ := runHook(t, "listen", input,
		map[string]string{"HUDDLE_URL": downURL(t), "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "me"})
	if stdout != "" {
		t.Errorf("listen printed %q for a subagent's plain tool call, want silence", stdout)
	}
}
