package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"

	"github.com/MuhmdRaouf/huddle/internal/testkit/fakesecret"
)

// The content store keeps no secret: PutContent redacts the text it is given before it
// gzips it, so no side of a content row holds a known secret shape. The secrets are made-up
// fakes from fakesecret; every file is a temp file.

// toolResultSide prints text the way a transcript's content capture does: a JSON array
// holding one tool result block.
func toolResultSide(t *testing.T, text string) string {
	t.Helper()
	printed, err := json.Marshal([]map[string]string{{"type": "tool_result", "text": text}})
	if err != nil {
		t.Fatalf("encode the side: %v", err)
	}
	return string(printed)
}

// openWithRequest opens a history holding the node and the request every content row needs.
func openWithRequest(t *testing.T) *History {
	t.Helper()
	h := openHistoryAt(t)
	if err := h.UpsertNode(nodeUpsert()); err != nil {
		t.Fatalf("upsert the node: %v", err)
	}
	if err := h.PutRequest("s1/main", requestRecord()); err != nil {
		t.Fatalf("put the request: %v", err)
	}
	return h
}

// redactionsOf answers the replacement counts the content row of requestID records.
func redactionsOf(t *testing.T, h *History, requestID string) (in, out int64) {
	t.Helper()
	row, err := h.db.Get("SELECT in_redactions, out_redactions FROM content WHERE request_id = ?", requestID)
	if err != nil || row == nil {
		t.Fatalf("read the redaction counts of %s: %v, %v", requestID, row, err)
	}
	in, _ = row["in_redactions"].(int64)
	out, _ = row["out_redactions"].(int64)
	return in, out
}

func TestPutContentRedactsEveryShape(t *testing.T) {
	layouts := []struct{ name, before, after string }{
		{"between spaces", "output: ", " done"},
		{"after a newline", "first line\n", "\nlast line"},
	}
	for _, fake := range fakesecret.All() {
		for _, layout := range layouts {
			t.Run(fake.Shape+" "+layout.name, func(t *testing.T) {
				h := openWithRequest(t)
				input := toolResultSide(t, layout.before+fake.Text+layout.after)
				output := toolResultSide(t, "ran "+fake.Text)
				if err := h.PutContent("r1", ContentSides{Input: &input, Output: &output}); err != nil {
					t.Fatalf("put the content: %v", err)
				}
				got, err := h.Content("r1")
				if err != nil || got == nil || got.Input == nil || got.Output == nil {
					t.Fatalf("read the content: %+v, %v", got, err)
				}
				for side, stored := range map[string]string{"input": *got.Input, "output": *got.Output} {
					for _, secret := range fake.Secrets {
						if strings.Contains(stored, secret) {
							t.Fatalf("the %s side holds %q verbatim: %s", side, secret, stored)
						}
					}
				}
				wantIn := toolResultSide(t, layout.before+fake.Want+layout.after)
				wantOut := toolResultSide(t, "ran "+fake.Want)
				if *got.Input != wantIn || *got.Output != wantOut {
					t.Fatalf("stored sides = %s and %s, want %s and %s", *got.Input, *got.Output, wantIn, wantOut)
				}
				if in, out := redactionsOf(t, h, "r1"); in != 1 || out != 1 {
					t.Fatalf("redaction counts = %d and %d, want 1 and 1", in, out)
				}
			})
		}
	}
}

func TestPutContentKeepsOrdinaryTextExact(t *testing.T) {
	plain := []string{
		"the token budget is 1024 and max_tokens=1024",
		"built 0123456789abcdef0123456789abcdef01234567 from a1b2c3d4",
		"KEY_COUNT=5 and TOKEN_LIMIT=10 and monkey=banana and KEYBOARD=us",
		"API_KEY=$API_KEY and SECRET_KEY=<your key here>",
		"-----BEGIN PUBLIC KEY-----\nabcdef\n-----END PUBLIC KEY----- and a " + fakesecret.PrivateKeyHeader() + " header",
		"ghp_short xoxb- eyJ alone AKIA1234 and sk-short",
	}
	h := openWithRequest(t)
	for i, text := range plain {
		id := "r" + string(rune('a'+i))
		side := toolResultSide(t, text)
		if err := h.PutContent("r1", ContentSides{Input: &side}); err != nil {
			t.Fatalf("put %q: %v", text, err)
		}
		got, err := h.Content("r1")
		if err != nil || got == nil || got.Input == nil {
			t.Fatalf("read %s: %+v, %v", id, got, err)
		}
		if *got.Input != side {
			t.Fatalf("ordinary text changed: stored %s, want %s", *got.Input, side)
		}
		if in, _ := redactionsOf(t, h, "r1"); in != 0 {
			t.Fatalf("ordinary text %q recorded %d redactions, want 0", text, in)
		}
		if err := h.db.Exec("DELETE FROM content"); err != nil {
			t.Fatalf("clear the content: %v", err)
		}
	}
}

// A streamed answer is written again as it grows; the count stays the number of secrets in
// the side that stands, not a running total of the writes.
func TestPutContentCountsTheStandingSideOnly(t *testing.T) {
	fakes := fakesecret.All()
	first, second := fakes[0].Text, fakes[6].Text
	h := openWithRequest(t)
	short := toolResultSide(t, "a "+first)
	longer := toolResultSide(t, "a "+first+" b "+second)
	for _, side := range []string{short, short, longer} {
		if err := h.PutContent("r1", ContentSides{Output: &side}); err != nil {
			t.Fatalf("put the output: %v", err)
		}
	}
	if _, out := redactionsOf(t, h, "r1"); out != 2 {
		t.Fatalf("output redactions = %d, want 2", out)
	}
	retry := toolResultSide(t, "x")
	if err := h.PutContent("r1", ContentSides{Output: &retry}); err != nil {
		t.Fatalf("put the retry echo: %v", err)
	}
	if _, out := redactionsOf(t, h, "r1"); out != 2 {
		t.Fatalf("output redactions after a shorter echo = %d, want 2 (the standing side)", out)
	}
	input := toolResultSide(t, "one "+first)
	if err := h.PutContent("r1", ContentSides{Input: &input}); err != nil {
		t.Fatalf("put the input: %v", err)
	}
	in, out := redactionsOf(t, h, "r1")
	if in != 1 || out != 2 {
		t.Fatalf("redaction counts = %d and %d, want 1 and 2: a side written alone leaves the other's count", in, out)
	}
}

// A transcript carrying a made-up secret in a prompt, a command, a tool result and a
// thinking block reaches history.db with every secret redacted and the counts recorded.
func TestWriterIngestRedactsTranscriptSecrets(t *testing.T) {
	fakes := fakesecret.All()
	byShape := map[string]fakesecret.Fake{}
	for _, fake := range fakes {
		byShape[fake.Shape] = fake
	}
	prompt := byShape["github ghp_"]
	command := byShape["env password"]
	result := byShape["private key block"]
	thinking := byShape["jwt"]
	answer := byShape["aws key pair"]

	env := writerEnv(t)
	world := newWriterWorld(t)
	h := world.attach(t)
	writerWriteText(t, filepath.Join(paths.ClaudeConfigDir(env), "projects", "proj", "s1.jsonl"), writerJSONL(t,
		map[string]any{
			"type": "user", "timestamp": writerISO(writerT0), "cwd": "/w/app",
			"message": map[string]any{"content": "use " + prompt.Text + " to log in"},
		},
		map[string]any{
			"type": "assistant", "timestamp": writerISO(writerT0 + 1000), "requestId": "req_1",
			"message": map[string]any{
				"model":       "claude-sonnet-5-5",
				"usage":       map[string]any{"input_tokens": 10, "output_tokens": 5},
				"stop_reason": "tool_use",
				"content": []any{
					map[string]any{"type": "thinking", "thinking": "the token is " + thinking.Text},
					map[string]any{"type": "tool_use", "id": "tu1", "name": "Bash", "input": map[string]any{
						"command": "export " + command.Text + " && ./deploy.sh",
					}},
				},
			},
		},
		map[string]any{
			"type": "user", "timestamp": writerISO(writerT0 + 2000),
			"message": map[string]any{
				"content": []any{
					map[string]any{"type": "tool_result", "tool_use_id": "tu1", "content": "cat key.pem\n" + result.Text + "\nok"},
				},
			},
		},
		map[string]any{
			"type": "assistant", "timestamp": writerISO(writerT0 + 3000), "requestId": "req_2",
			"message": map[string]any{
				"model":       "claude-sonnet-5-5",
				"usage":       map[string]any{"input_tokens": 20, "output_tokens": 6},
				"stop_reason": "end_turn",
				"content":     []any{map[string]any{"type": "text", "text": "found " + answer.Text}},
			},
		},
	))
	writerWriteText(t, filepath.Join(paths.SpoolDir(t.Context(), env), "spool.jsonl"),
		writerJSONL(t, map[string]any{"ts": writerISO(writerT0 + 5), "event": "SessionStart", "session_id": "s1"}))
	watcher := ingest.StartWatcher(t.Context(), ingest.WatcherOptions{
		Env: env, Store: world.store, SinceMs: 86_400_000, IntervalMs: 3_600_000,
		Now:   func() int64 { return time.Date(2026, 1, 15, 6, 30, 0, 0, time.UTC).UnixMilli() },
		Alive: func(pid int64) bool { return pid == int64(os.Getpid()) },
	})
	t.Cleanup(watcher.Stop)
	writerWaitFor(t, "initial ingest", func() bool { return !watcher.CatchingUp() })
	world.writer.Flush()

	stored := map[string]string{}
	for _, id := range []string{"req_1", "req_2"} {
		content, err := h.Content(id)
		if err != nil || content == nil {
			t.Fatalf("read the content of %s: %+v, %v", id, content, err)
		}
		stored[id+" input"] = derep(content.Input, "")
		stored[id+" output"] = derep(content.Output, "")
	}
	for _, fake := range []fakesecret.Fake{prompt, command, result, thinking, answer} {
		for _, secret := range fake.Secrets {
			for side, text := range stored {
				if strings.Contains(text, secret) {
					t.Errorf("%s holds the %s secret %q verbatim", side, fake.Shape, secret)
				}
			}
		}
	}
	if !strings.Contains(stored["req_1 input"], "[redacted:github-token]") {
		t.Errorf("req_1 input = %s, want the github label", stored["req_1 input"])
	}
	if in, out := redactionsOf(t, h, "req_1"); in != 1 || out != 2 {
		t.Errorf("req_1 redaction counts = %d and %d, want 1 and 2", in, out)
	}
	if in, out := redactionsOf(t, h, "req_2"); in != 1 || out != 1 {
		t.Errorf("req_2 redaction counts = %d and %d, want 1 and 1", in, out)
	}
}

// A database written before the counts existed gains the two columns; the rows it holds
// are not rewritten, so their counts stay unset.
func TestMigrationLeavesStoredContentAsItWas(t *testing.T) {
	db, _ := buildV5(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	row, err := db.Get("SELECT in_redactions, out_redactions FROM content WHERE request_id = 'r1'")
	if err != nil || row == nil {
		t.Fatalf("read the migrated row: %v, %v", row, err)
	}
	if row["in_redactions"] != nil || row["out_redactions"] != nil {
		t.Errorf("the old row's counts = %v and %v, want unset", row["in_redactions"], row["out_redactions"])
	}
}

// A label can be longer than the short secret it replaces; a streamed answer that grows by
// less than that still replaces the stored chunk.
func TestPutContentGrowthCountsRedactedLength(t *testing.T) {
	h := openWithRequest(t)
	first := toolResultSide(t, "set NPM_TOKEN=abcd")
	grown := toolResultSide(t, "set NPM_TOKEN=abcd now")
	for _, side := range []string{first, grown} {
		if err := h.PutContent("r1", ContentSides{Output: &side}); err != nil {
			t.Fatalf("put the output: %v", err)
		}
	}
	got, err := h.Content("r1")
	if err != nil || got == nil || got.Output == nil {
		t.Fatalf("read the content: %+v, %v", got, err)
	}
	if want := toolResultSide(t, "set NPM_TOKEN=[redacted:env-secret] now"); *got.Output != want {
		t.Fatalf("output = %s, want %s", *got.Output, want)
	}
}
