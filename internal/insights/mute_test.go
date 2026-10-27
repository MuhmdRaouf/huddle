package insights

// Muting: a "not useful" mutes one rule for 30 days in the file's muted key, the switches and
// every other key survive the write, and Forget deletes the file, the only learning Insights
// keeps.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mutedEntries answers the muted array the file holds, for the tests that pin the file itself.
func mutedEntries(t *testing.T, path string) []struct {
	Rule  string `json:"rule"`
	Until int64  `json:"until"`
} {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Muted []struct {
			Rule  string `json:"rule"`
			Until int64  `json:"until"`
		} `json:"muted"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("the file no longer parses: %v", err)
	}
	return file.Muted
}

func TestMute(t *testing.T) {
	now := fixedNow()

	t.Run("a mute into a missing file holds the rule for 30 days", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "insights.json")
		if err := Mute(path, RuleSameCommand, now); err != nil {
			t.Fatal(err)
		}
		if !MutedRules(path, now)[RuleSameCommand] {
			t.Error("same_command: the rule is muted right after the mute")
		}
		if !MutedRules(path, now.Add(29*24*time.Hour))[RuleSameCommand] {
			t.Error("same_command: still muted at 29 days")
		}
		if MutedRules(path, now.Add(30*24*time.Hour+time.Millisecond))[RuleSameCommand] {
			t.Error("same_command: no longer muted at 30 days and 1 ms")
		}
	})

	t.Run("a mute keeps the switches and a switch write keeps the mute", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "insights.json")
		if err := WriteSettings(path, Settings{TipsForMe: false, TipsToSessions: false}); err != nil {
			t.Fatal(err)
		}
		if err := Mute(path, RuleLaneFailing, now); err != nil {
			t.Fatal(err)
		}
		if got := ReadSettings(path); got.TipsForMe || got.TipsToSessions {
			t.Errorf("switches = %+v, want both off, as the write left them", got)
		}
		if err := WriteSettings(path, DefaultSettings()); err != nil {
			t.Fatal(err)
		}
		if !MutedRules(path, now)[RuleLaneFailing] {
			t.Error("lane_failing: the switch write dropped the mute")
		}
	})

	t.Run("muting a rule twice keeps one entry with the later until", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "insights.json")
		if err := Mute(path, RuleSameCommand, now); err != nil {
			t.Fatal(err)
		}
		later := now.Add(time.Hour)
		if err := Mute(path, RuleSameCommand, later); err != nil {
			t.Fatal(err)
		}
		entries := mutedEntries(t, path)
		if len(entries) != 1 {
			t.Fatalf("muted holds %d entries, want 1", len(entries))
		}
		if entries[0].Rule != RuleSameCommand {
			t.Errorf("rule = %s, want same_command", entries[0].Rule)
		}
		if want := later.Add(30 * 24 * time.Hour).UnixMilli(); entries[0].Until != want {
			t.Errorf("until = %d, want %d, the second mute's", entries[0].Until, want)
		}
		if !MutedRules(path, now.Add(30*24*time.Hour+time.Millisecond))[RuleSameCommand] {
			t.Error("same_command: no longer muted at the first mute's run-out, the later until lost")
		}
	})

	t.Run("muting a second rule keeps the first", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "insights.json")
		if err := Mute(path, RuleSameCommand, now); err != nil {
			t.Fatal(err)
		}
		if err := Mute(path, RuleLaneFailing, now); err != nil {
			t.Fatal(err)
		}
		entries := mutedEntries(t, path)
		if len(entries) != 2 {
			t.Fatalf("muted holds %d entries, want 2", len(entries))
		}
		if entries[0].Rule != RuleSameCommand || entries[1].Rule != RuleLaneFailing {
			t.Errorf("rules = %s, %s, want same_command then lane_failing, in order",
				entries[0].Rule, entries[1].Rule)
		}
		if !MutedRules(path, now)[RuleSameCommand] || !MutedRules(path, now)[RuleLaneFailing] {
			t.Error("want both rules muted")
		}
	})

	t.Run("the next mute drops an entry that has run out", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "insights.json")
		if err := Mute(path, RuleSameCommand, now.Add(-31*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := Mute(path, RuleLaneFailing, now); err != nil {
			t.Fatal(err)
		}
		entries := mutedEntries(t, path)
		if len(entries) != 1 || entries[0].Rule != RuleLaneFailing {
			t.Errorf("muted = %+v, want lane_failing only, same_command run out", entries)
		}
	})

	t.Run("the file lands at mode 0600 and no temp file stays", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "insights.json")
		if err := Mute(path, RuleToolFailing, now); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("mode = %o, want 600", info.Mode().Perm())
		}
		left, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(left) != 1 {
			t.Errorf("the folder holds %d files, want insights.json alone", len(left))
		}
	})

	t.Run("a muted key holding a string answers an empty map", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "insights.json")
		if err := os.WriteFile(path, []byte(`{"muted":"nope"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := MutedRules(path, now); len(got) != 0 {
			t.Errorf("muted = %v, want an empty map", got)
		}
	})

	t.Run("an unreadable file answers an empty map", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "insights.json")
		if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o000); err != nil {
			t.Fatal(err)
		}
		if got := MutedRules(path, now); len(got) != 0 {
			t.Errorf("muted = %v, want an empty map", got)
		}
	})
}

func TestWithoutMuted(t *testing.T) {
	tips := []Tip{
		{ID: RuleSameCommand + ":s1:go-test", Rule: RuleSameCommand, Sentence: "first"},
		{ID: RuleLaneFailing + ":kimi", Rule: RuleLaneFailing, Sentence: "second"},
		{ID: RuleLongDay + ":2026-10-25", Rule: RuleLongDay, Sentence: "third"},
	}

	t.Run("the muted rule's tips drop and the order holds", func(t *testing.T) {
		got := WithoutMuted(tips, map[string]bool{RuleLaneFailing: true})
		if len(got) != 2 {
			t.Fatalf("answered %d tips, want 2", len(got))
		}
		if got[0].Rule != RuleSameCommand || got[1].Rule != RuleLongDay {
			t.Errorf("rules = %s, %s, want same_command then long_day, in order",
				got[0].Rule, got[1].Rule)
		}
	})

	t.Run("nothing muted answers the input", func(t *testing.T) {
		got := WithoutMuted(tips, nil)
		if len(got) != len(tips) {
			t.Fatalf("answered %d tips, want all %d", len(got), len(tips))
		}
		for i := range tips {
			if got[i] != tips[i] {
				t.Errorf("tip %d = %+v, want %+v, in order", i, got[i], tips[i])
			}
		}
	})

	t.Run("nothing left answers an empty slice, never nil", func(t *testing.T) {
		got := WithoutMuted(tips, map[string]bool{
			RuleSameCommand: true, RuleLaneFailing: true, RuleLongDay: true,
		})
		if got == nil {
			t.Fatal("answered nil, want an empty slice")
		}
		if len(got) != 0 {
			t.Errorf("answered %d tips, want none", len(got))
		}
		if got := WithoutMuted(nil, nil); got == nil {
			t.Fatal("no tips answered nil, want an empty slice")
		}
	})
}

func TestForget(t *testing.T) {
	t.Run("forget removes the file, a second forget is quiet, the defaults come back", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "insights.json")
		if err := WriteSettings(path, Settings{TipsForMe: true, TipsToSessions: true}); err != nil {
			t.Fatal(err)
		}
		if err := Mute(path, RuleLaneFailing, fixedNow()); err != nil {
			t.Fatal(err)
		}
		if err := Forget(path); err != nil {
			t.Fatalf("forget: %v", err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("stat after forget = %v, want the file gone", err)
		}
		if err := Forget(path); err != nil {
			t.Fatalf("second forget: %v, want quiet", err)
		}
		answersDefaults(t, ReadSettings(path))
		if got := MutedRules(path, fixedNow()); len(got) != 0 {
			t.Errorf("muted = %v, want an empty map", got)
		}
	})
}
