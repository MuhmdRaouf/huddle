package hooks

// The Stop hook's digest: built from the session's own transcript — the newest prompt
// as the goal, the files the session edited, the decisions and open questions of its last words
// — with the golden files under testdata/digest pinning the exact bytes. No model call and no
// other transcript text ever reaches it.

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// updateGolden rewrites the golden files, as the repo's other golden tests do:
// go test ./internal/huddle/hooks/ -run TestTheDigest -update
var updateGolden = flag.Bool("update", false, "rewrite the digest golden files")

func TestTheDigestGolden(t *testing.T) {
	for _, name := range []string{"plain", "long", "empty", "injected"} {
		t.Run(name, func(t *testing.T) {
			got := digestOf(filepath.Join("testdata", "digest", name+".jsonl"), "/repo")
			golden := filepath.Join("testdata", "digest", name+".txt")
			if *updateGolden {
				if err := os.WriteFile(golden, []byte(got), 0o600); err != nil {
					t.Fatalf("write the golden: %v", err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read the golden: %v", err)
			}
			if got != string(want) {
				t.Errorf("the digest of %s.jsonl:\n%s\nwant:\n%s", name, got, want)
			}
		})
	}
}

func TestTheDigestNeverPassesFifteenHundredBytes(t *testing.T) {
	got := digestOf(filepath.Join("testdata", "digest", "long.jsonl"), "/repo")
	if len(got) > digestMax {
		t.Errorf("the digest is %d bytes, want at most %d", len(got), digestMax)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a digest cut short ends without …: %q", tailOf(got, 40))
	}
}

// TestTheDigestCarriesNoOtherTranscriptText pins the digest to its four sections: the tool
// results' words (where a command's output lands) never reach it.
func TestTheDigestCarriesNoOtherTranscriptText(t *testing.T) {
	got := digestOf(filepath.Join("testdata", "digest", "plain.jsonl"), "/repo")
	if strings.Contains(got, "SECRET-HUSH-9f2b") {
		t.Errorf("the digest carries a tool result's text: %s", got)
	}
}

func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// TestTheDigestGoalIsWhatThePersonTyped pins the goal's source: a skill body Claude Code
// injects (a user entry marked isMeta) and a command wrapper never become the goal; the last
// prompt the person typed does.
func TestTheDigestGoalIsWhatThePersonTyped(t *testing.T) {
	lines := [][]byte{
		[]byte(`{"type":"user","message":{"role":"user","content":"fix the login bug"}}`),
		[]byte(`{"type":"user","isMeta":true,"message":{"role":"user","content":"Base directory for this skill: /Users/raouf/.claude/plugins/cache/muhmdraouf/huddle/c095e8c180ac/skills/huddle\n\nRead the skill and follow it."}}`),
		[]byte(`{"type":"user","message":{"role":"user","content":"<command-message>huddle:join</command-message>\n<command-name>/huddle:join</command-name>"}}`),
	}
	if got, want := buildDigest(lines, "/repo"), "Goal: fix the login bug"; got != want {
		t.Errorf("buildDigest of the injected turns =\n%s\nwant\n%s", got, want)
	}
}

func TestTheDigestBuilderEdges(t *testing.T) {
	prompt := []byte(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"An array prompt"},{"type":"text","text":"second block"}]}}`)
	edits := [][]byte{
		prompt,
		[]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"NotebookEdit","input":{"notebook_path":"/repo/analysis.ipynb"}}]}}`),
		[]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Edit","input":{"file_path":"src/relative.go"}}]}}`),
		[]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Write","input":{"file_path":"/elsewhere/outside.go"}}]}}`),
	}
	if got, want := buildDigest(edits, "/repo"),
		"Goal: An array prompt\nFiles: analysis.ipynb, src/relative.go, /elsewhere/outside.go"; got != want {
		t.Errorf("buildDigest of the edges =\n%s\nwant\n%s", got, want)
	}
	// a user turn that mixes its words with a tool result is a result turn, not a goal
	mixed := [][]byte{
		[]byte(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"words"},{"type":"tool_result","content":"out"}]}}`),
	}
	if got := buildDigest(mixed, "/repo"); got != "" {
		t.Errorf("a mixed user turn built %q, want no digest", got)
	}
	// content that is neither string nor block array says nothing
	if got := buildDigest([][]byte{[]byte(`{"type":"user","message":{"content":42}}`)}, "/repo"); got != "" {
		t.Errorf("a numeric content built %q, want no digest", got)
	}
	if got := buildDigest([][]byte{[]byte(`{"type":"user"}`)}, "/repo"); got != "" {
		t.Errorf("a contentless line built %q, want no digest", got)
	}
	// a transcript that cannot be read digests to nothing
	if got := digestOf(filepath.Join("testdata", "digest", "no-such.jsonl"), "/repo"); got != "" {
		t.Errorf("a missing transcript built %q", got)
	}
	empty := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := digestOf(empty, "/repo"); got != "" {
		t.Errorf("an empty transcript built %q", got)
	}
	// the window of a long transcript opens mid-line: the partial line is dropped, the rest read
	long := filepath.Join(t.TempDir(), "long.jsonl")
	pad := `{"type":"user","message":{"content":"` + strings.Repeat("pad ", 70000) + `"}}`
	body := pad[:len(pad)-10] + "\n" + `{"type":"user","message":{"content":"the real goal"}}` + "\n"
	if err := os.WriteFile(long, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := digestOf(long, "/repo"); got != "Goal: the real goal" {
		t.Errorf("the tail digest = %q, want the goal past the partial line", got)
	}
	// the cut never splits a letter: a digest of multibyte runes stays valid UTF-8 under the cap
	wide := strings.Repeat("é", digestMax) + " tail"
	capped := capDigest(wide)
	if len(capped) > digestMax || !utf8.ValidString(capped) || !strings.HasSuffix(capped, "…") {
		t.Errorf("capDigest of wide runes = %d bytes, valid %v, suffix … %v",
			len(capped), utf8.ValidString(capped), strings.HasSuffix(capped, "…"))
	}
}

func TestTheDigestOfUnreadableTranscriptsIsNothing(t *testing.T) {
	if got := digestOf("", "/repo"); got != "" {
		t.Errorf("digestOf of no path = %q", got)
	}
	sealed := filepath.Join(t.TempDir(), "sealed.jsonl")
	if err := os.WriteFile(sealed, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(sealed, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(sealed, 0o600) })
	if got := digestOf(sealed, "/repo"); got != "" {
		t.Errorf("digestOf of an unreadable transcript = %q", got)
	}
	if got := digestOf(t.TempDir(), "/repo"); got != "" {
		t.Errorf("digestOf of a directory = %q", got)
	}
	// an assistant turn whose content is no block array says nothing and breaks nothing
	if got := buildDigest([][]byte{
		[]byte(`{"type":"assistant","message":{"content":"plain words"}}`),
		[]byte(`{"type":"user","message":{"content":[{"type":"image"}]}}`),
	}, "/repo"); got != "" {
		t.Errorf("non-array contents built %q, want no digest", got)
	}
}
