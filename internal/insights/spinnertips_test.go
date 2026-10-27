package insights

// The spinner tips: the file Claude Code reads through the spinnerTipsOverride entry. Only
// whole-huddle tips become entries, one line each at Claude Code's own limits, and the file
// lands at mode 0600 through a rename, so Claude Code never reads half a file.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// tipIDShape matches an entry id: the huddle prefix and 16 lowercase hex characters.
var tipIDShape = regexp.MustCompile(`^huddle-[0-9a-f]{16}$`)

func TestSpinnerTipsFile(t *testing.T) {
	got := SpinnerTipsFile("/h")
	if want := filepath.Join("/h", ".agents", "huddle", "spinner-tips.json"); got != want {
		t.Errorf("SpinnerTipsFile(/h) = %q, want %q", got, want)
	}
	if base := filepath.Base(got); !strings.HasSuffix(base, "spinner-tips.json") {
		t.Errorf("base name = %q, want one ending in spinner-tips.json", base)
	}
}

func TestSpinnerTipsOf(t *testing.T) {
	t.Run("tips for me off answers an empty, non-nil slice", func(t *testing.T) {
		got := SpinnerTipsOf([]Tip{{ID: "r:s", Sentence: "Something."}}, Settings{})
		if got == nil {
			t.Fatal("the slice is nil, want an empty one")
		}
		if len(got) != 0 {
			t.Errorf("len = %d, want 0", len(got))
		}
	})

	t.Run("two owner tips and one session tip answer two entries in order", func(t *testing.T) {
		tips := []Tip{
			{ID: "lane_failing:zai", Rule: RuleLaneFailing, Sentence: "First owner tip."},
			{ID: "long_day:2026-10-26", Rule: RuleLongDay, Sentence: "Second owner tip."},
			{ID: "same_command:s1:k1", Rule: RuleSameCommand, Session: "s1", Sentence: "Session tip."},
		}
		got := SpinnerTipsOf(tips, Settings{TipsForMe: true})
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2: the session tip belongs to its session alone", len(got))
		}
		for i, entry := range got {
			if !tipIDShape.MatchString(entry.ID) {
				t.Errorf("entry %d id = %q, want huddle- plus 16 hex characters", i, entry.ID)
			}
			if entry.CooldownSessions != 3 {
				t.Errorf("entry %d cooldownSessions = %d, want 3", i, entry.CooldownSessions)
			}
			if entry.Priority != 0 {
				t.Errorf("entry %d priority = %d, want 0", i, entry.Priority)
			}
			if entry.Text != tips[i].Sentence {
				t.Errorf("entry %d text = %q, want %q", i, entry.Text, tips[i].Sentence)
			}
		}
		if got[0].ID == got[1].ID {
			t.Errorf("both entries have id %q, want one id per tip id", got[0].ID)
		}
		again := SpinnerTipsOf(tips, Settings{TipsForMe: true})
		for i := range got {
			if again[i].ID != got[i].ID {
				t.Errorf("entry %d id = %q on the second call, want %q every call", i, again[i].ID, got[i].ID)
			}
		}
	})

	t.Run("every run of whitespace, newlines included, becomes one space", func(t *testing.T) {
		tips := []Tip{{ID: "r:x", Sentence: "one\n  two\t\tthree\r\nfour"}}
		got := SpinnerTipsOf(tips, Settings{TipsForMe: true})
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1", len(got))
		}
		if got[0].Text != "one two three four" {
			t.Errorf("text = %q, want %q", got[0].Text, "one two three four")
		}
	})

	t.Run("a 600-rune sentence is cut to 500 runes ending in an ellipsis", func(t *testing.T) {
		tips := []Tip{{ID: "r:x", Sentence: strings.Repeat("a", 600)}}
		got := SpinnerTipsOf(tips, Settings{TipsForMe: true})
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1", len(got))
		}
		runes := []rune(got[0].Text)
		if len(runes) != 500 {
			t.Fatalf("runes = %d, want 500", len(runes))
		}
		if last := runes[len(runes)-1]; last != '…' {
			t.Errorf("last rune = %q, want an ellipsis", last)
		}
	})

	t.Run("250 owner tips answer 200 entries", func(t *testing.T) {
		tips := make([]Tip, 0, 250)
		for i := 0; i < 250; i++ {
			tips = append(tips, Tip{ID: fmt.Sprintf("rule:tip-%d", i), Sentence: fmt.Sprintf("Tip number %d.", i)})
		}
		got := SpinnerTipsOf(tips, Settings{TipsForMe: true})
		if len(got) != 200 {
			t.Fatalf("len = %d, want 200", len(got))
		}
		for i, entry := range got {
			if want := fmt.Sprintf("Tip number %d.", i); entry.Text != want {
				t.Fatalf("entry %d text = %q, want %q, the tip in position %d", i, entry.Text, want, i)
			}
		}
	})
}

func TestWriteSpinnerTips(t *testing.T) {
	t.Run("a write into a missing directory creates it and lands at mode 0600", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "deep", "deeper", "spinner-tips.json")
		tips := []SpinnerTip{{ID: "huddle-0123456789abcdef", Text: "One line.", CooldownSessions: 3}}
		if err := WriteSpinnerTips(path, tips); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var file map[string]json.RawMessage
		if err := json.Unmarshal(raw, &file); err != nil {
			t.Fatalf("the file does not parse: %v", err)
		}
		if len(file) != 1 {
			t.Fatalf("the file holds %d top-level keys, want only tips", len(file))
		}
		var entries []map[string]json.RawMessage
		if err := json.Unmarshal(file["tips"], &entries); err != nil {
			t.Fatalf("tips does not hold a list: %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("tips holds %d entries, want 1", len(entries))
		}
		entry := entries[0]
		if len(entry) != 4 {
			t.Fatalf("the entry holds %v, want exactly the fields id, text, cooldownSessions and priority", entry)
		}
		for _, field := range []struct {
			name string
			want string
		}{
			{"id", `"huddle-0123456789abcdef"`},
			{"text", `"One line."`},
			{"cooldownSessions", "3"},
			{"priority", "0"},
		} {
			got, ok := entry[field.name]
			if !ok {
				t.Errorf("the entry names no %s field", field.name)
				continue
			}
			if string(got) != field.want {
				t.Errorf("%s = %s, want %s", field.name, got, field.want)
			}
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("mode = %o, want 600", info.Mode().Perm())
		}
		left, err := os.ReadDir(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		if len(left) != 1 || left[0].Name() != "spinner-tips.json" {
			t.Errorf("the directory holds %d files, want only spinner-tips.json", len(left))
		}
	})

	t.Run("an empty list writes {\"tips\":[]}", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "spinner-tips.json")
		if err := WriteSpinnerTips(path, []SpinnerTip{}); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != `{"tips":[]}` {
			t.Errorf("the file holds %s, want {\"tips\":[]}", raw)
		}
	})

	t.Run("a second write replaces the first", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "spinner-tips.json")
		first := []SpinnerTip{{ID: "huddle-first", Text: "First."}}
		second := []SpinnerTip{{ID: "huddle-second", Text: "Second."}, {ID: "huddle-third", Text: "Third."}}
		if err := WriteSpinnerTips(path, first); err != nil {
			t.Fatal(err)
		}
		if err := WriteSpinnerTips(path, second); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var file struct {
			Tips []SpinnerTip `json:"tips"`
		}
		if err := json.Unmarshal(raw, &file); err != nil {
			t.Fatalf("the file does not parse: %v", err)
		}
		if len(file.Tips) != 2 || file.Tips[0].ID != second[0].ID || file.Tips[1].Text != second[1].Text {
			t.Errorf("the file holds %s, want the second list alone", raw)
		}
	})
}
