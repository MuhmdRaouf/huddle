package insights

// The owner's Insights switches: the defaults when the file says nothing readable, the fields a
// file names over their defaults, and a write that keeps the keys the switches do not own.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// answersDefaults fails when the switches are not tips for me on and tips to sessions off.
func answersDefaults(t *testing.T, got Settings) {
	t.Helper()
	if !got.TipsForMe {
		t.Error("TipsForMe: the default is on")
	}
	if got.TipsToSessions {
		t.Error("TipsToSessions: the default is off")
	}
}

func TestReadSettings(t *testing.T) {
	t.Run("no file answers the defaults", func(t *testing.T) {
		answersDefaults(t, ReadSettings(filepath.Join(t.TempDir(), "insights.json")))
	})

	t.Run("a file that does not parse answers the defaults", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "insights.json")
		if err := os.WriteFile(path, []byte(`{"tips_for_me":`), 0o600); err != nil {
			t.Fatal(err)
		}
		answersDefaults(t, ReadSettings(path))
	})

	t.Run("a file that is not an object answers the defaults", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "insights.json")
		if err := os.WriteFile(path, []byte(`[]`), 0o600); err != nil {
			t.Fatal(err)
		}
		answersDefaults(t, ReadSettings(path))
	})

	t.Run("a field the file leaves out keeps its default", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "insights.json")
		if err := os.WriteFile(path, []byte(`{"tips_to_sessions":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
		got := ReadSettings(path)
		if !got.TipsForMe {
			t.Error("TipsForMe: the file names tips_to_sessions only, tips for me keeps its default on")
		}
		if !got.TipsToSessions {
			t.Error("TipsToSessions: the file turns it on")
		}
	})
}

func TestWriteSettings(t *testing.T) {
	t.Run("a write round-trips through a read at mode 0600", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "insights.json")
		if err := WriteSettings(path, Settings{TipsForMe: false, TipsToSessions: true}); err != nil {
			t.Fatal(err)
		}
		got := ReadSettings(path)
		if got.TipsForMe {
			t.Error("TipsForMe: the write turned it off and the read lost it")
		}
		if !got.TipsToSessions {
			t.Error("TipsToSessions: the write turned it on and the read lost it")
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("mode = %o, want 600", info.Mode().Perm())
		}
	})

	t.Run("a key the switches do not own survives the write", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "insights.json")
		if err := os.WriteFile(path, []byte(`{"muted":{"x":1}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := WriteSettings(path, DefaultSettings()); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Muted          json.RawMessage `json:"muted"`
			TipsForMe      bool            `json:"tips_for_me"`
			TipsToSessions bool            `json:"tips_to_sessions"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("the file no longer parses: %v", err)
		}
		if string(got.Muted) != `{"x":1}` {
			t.Errorf("muted = %s, want {\"x\":1} kept as it was", got.Muted)
		}
		if !got.TipsForMe || got.TipsToSessions {
			t.Errorf("switches = %s, want the defaults written over their fields", raw)
		}
	})
}
