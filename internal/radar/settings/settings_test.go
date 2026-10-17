package settings

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// nowMs is the suite's clock: a Thursday, 15:00 local.
var nowMs = time.Date(2026, time.October, 8, 15, 0, 0, 0, time.Local).UnixMilli()

const msPerDay = 86_400_000

// testEnv is a whole throwaway world: HOME and RADAR_HOME under one temp dir, built from
// scratch so no stray variable from the surrounding shell can leak a real path in.
type testEnv struct {
	env   domain.EnvLookup
	home  string
	state string
}

func makeEnv(t *testing.T) testEnv {
	t.Helper()
	home := t.TempDir()
	return testEnv{
		env: domain.EnvLookup{
			"HOME":              home,
			"RADAR_HOME":        filepath.Join(home, "state"),
			"CLAUDE_CONFIG_DIR": filepath.Join(home, "claude"),
			"ZAI_STATE_DIR":     filepath.Join(home, "zai"),
		},
		home:  home,
		state: filepath.Join(home, "state"),
	}
}

func writeText(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustValue(t *testing.T, text string) *jsfmt.Value {
	t.Helper()
	value, err := jsfmt.DecodeValue([]byte(text))
	if err != nil {
		t.Fatalf("bad test JSON %q: %v", text, err)
	}
	return value
}

func TestSettingsAndDismissals(t *testing.T) {
	t.Run("defaults to notifications on and validates updates", func(t *testing.T) {
		ctx := context.Background()
		env := makeEnv(t)
		if got := ReadSettings(ctx, env.env); got != DefaultSettings {
			t.Fatalf("readSettings with no file = %+v, want %+v", got, DefaultSettings)
		}
		next, refusal := ValidateSettings(mustValue(t, `{"notifications":false}`), DefaultSettings)
		if refusal != "" || next != (Settings{Notifications: false, HistoryRetentionDays: 30}) {
			t.Fatalf(`validateSettings({notifications:false}) = (%+v, %q), want notifications off`, next, refusal)
		}
		_, refusal = ValidateSettings(mustValue(t, `{"notifications":"no"}`), DefaultSettings)
		if !strings.Contains(refusal, "true or false") {
			t.Fatalf(`validateSettings({notifications:"no"}) = %q, want it to say true or false`, refusal)
		}
		_, refusal = ValidateSettings(mustValue(t, `{"theme":1}`), DefaultSettings)
		if !strings.Contains(refusal, "unknown") {
			t.Fatalf(`validateSettings({theme:1}) = %q, want it to say unknown`, refusal)
		}
		_, refusal = ValidateSettings(mustValue(t, `[]`), DefaultSettings)
		if !strings.Contains(refusal, "object") {
			t.Fatalf("validateSettings([]) = %q, want it to say object", refusal)
		}
		if err := WriteSettings(ctx, env.env, Settings{Notifications: false, HistoryRetentionDays: 30}); err != nil {
			t.Fatal(err)
		}
		want := "{\n  \"version\": 1,\n  \"notifications\": false,\n  \"historyRetentionDays\": 30\n}\n"
		data, err := os.ReadFile(SettingsPath(ctx, env.env))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != want {
			t.Fatalf("settings.json =\n%s\nwant\n%s", string(data), want)
		}
		if got := ReadSettings(ctx, env.env); got != (Settings{Notifications: false, HistoryRetentionDays: 30}) {
			t.Fatalf("readSettings after write = %+v, want notifications off", got)
		}
	})

	t.Run("takes only the offered history retention days, 0 meaning forever", func(t *testing.T) {
		ctx := context.Background()
		env := makeEnv(t)
		for _, days := range RetentionDays {
			next, refusal := ValidateSettings(
				mustValue(t, fmt.Sprintf(`{"historyRetentionDays":%d}`, days)), DefaultSettings)
			if refusal != "" || next != (Settings{Notifications: true, HistoryRetentionDays: days}) {
				t.Fatalf("validateSettings(historyRetentionDays %d) = (%+v, %q), want it taken", days, next, refusal)
			}
		}
		for _, bad := range []string{`14`, `-1`, `"30"`} {
			_, refusal := ValidateSettings(
				mustValue(t, `{"historyRetentionDays":`+bad+`}`), DefaultSettings)
			if !strings.Contains(refusal, "historyRetentionDays") {
				t.Fatalf("validateSettings(historyRetentionDays %s) = %q, want it to say historyRetentionDays", bad, refusal)
			}
		}
		if err := WriteSettings(ctx, env.env, Settings{Notifications: true, HistoryRetentionDays: 7}); err != nil {
			t.Fatal(err)
		}
		if got := ReadSettings(ctx, env.env); got.HistoryRetentionDays != 7 {
			t.Fatalf("readSettings after write = %+v, want 7 days", got)
		}
		// a file from before the setting existed, or holding a value the dashboard never offered, is the default
		legacy := struct {
			Version              int  `json:"version"`
			Notifications        bool `json:"notifications"`
			HistoryRetentionDays int  `json:"historyRetentionDays"`
		}{Version: 1, Notifications: true, HistoryRetentionDays: 14}
		if err := WriteJSONAtomic(SettingsPath(ctx, env.env), legacy); err != nil {
			t.Fatal(err)
		}
		if got := ReadSettings(ctx, env.env).HistoryRetentionDays; got != DefaultSettings.HistoryRetentionDays {
			t.Fatalf("readSettings(offered 14) = %d, want the default %d", got, DefaultSettings.HistoryRetentionDays)
		}
	})

	t.Run("loads a settings file holding keys it does not know", func(t *testing.T) {
		ctx := context.Background()
		env := makeEnv(t)
		writeText(t, SettingsPath(ctx, env.env),
			`{"version":1,"notifications":false,"historyRetentionDays":90,"theme":"dark"}`)
		if got := ReadSettings(ctx, env.env); got != (Settings{Notifications: false, HistoryRetentionDays: 90}) {
			t.Fatalf("readSettings(unknown keys) = %+v, want notifications off and 90 days", got)
		}
	})

	t.Run("keeps dismissals for a week", func(t *testing.T) {
		ctx := context.Background()
		env := makeEnv(t)
		if got := ReadDismissed(ctx, env.env, nowMs); len(got) != 0 {
			t.Fatalf("readDismissed with no file = %+v, want none", got)
		}
		dismissed := []Dismissal{
			{ID: "fresh", At: float64(nowMs - 1000)},
			{ID: "old", At: float64(nowMs - 8*msPerDay)},
		}
		if err := WriteDismissed(ctx, env.env, dismissed); err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("{\n  \"version\": 1,\n  \"ids\": {\n    \"fresh\": %d,\n    \"old\": %d\n  }\n}\n",
			nowMs-1000, nowMs-8*msPerDay)
		data, err := os.ReadFile(DismissedPath(ctx, env.env))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != want {
			t.Fatalf("dismissed.json =\n%s\nwant\n%s", string(data), want)
		}
		got := ReadDismissed(ctx, env.env, nowMs)
		if len(got) != 1 || got[0].ID != "fresh" {
			t.Fatalf("readDismissed = %+v, want only fresh", got)
		}
		if err := WriteDismissed(ctx, env.env, nil); err != nil {
			t.Fatal(err)
		}
		data, err = os.ReadFile(DismissedPath(ctx, env.env))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "{\n  \"version\": 1,\n  \"ids\": {}\n}\n" {
			t.Fatalf("dismissed.json emptied =\n%s, want empty ids", string(data))
		}
	})
}

func TestTolerantReads(t *testing.T) {
	t.Run("a broken settings file is the defaults", func(t *testing.T) {
		ctx := context.Background()
		env := makeEnv(t)
		writeText(t, SettingsPath(ctx, env.env), "{oops")
		if got := ReadSettings(ctx, env.env); got != DefaultSettings {
			t.Fatalf("readSettings(broken json) = %+v, want the defaults", got)
		}
		writeText(t, SettingsPath(ctx, env.env), "[1]")
		if got := ReadSettings(ctx, env.env); got != DefaultSettings {
			t.Fatalf("readSettings(not an object) = %+v, want the defaults", got)
		}
	})

	t.Run("a broken dismissed file is nothing; an ids array has indices for keys", func(t *testing.T) {
		ctx := context.Background()
		env := makeEnv(t)
		writeText(t, DismissedPath(ctx, env.env), "{oops")
		if got := ReadDismissed(ctx, env.env, nowMs); len(got) != 0 {
			t.Fatalf("readDismissed(broken json) = %+v, want none", got)
		}
		// an array under ids reads the way Object.entries reads it: index keys, item values
		writeText(t, DismissedPath(ctx, env.env), fmt.Sprintf(`{"version":1,"ids":[%d]}`, nowMs-1000))
		got := ReadDismissed(ctx, env.env, nowMs)
		if len(got) != 1 || got[0].ID != "0" || got[0].At != float64(nowMs-1000) {
			t.Fatalf("readDismissed(ids array) = %+v, want the fresh entry under key 0", got)
		}
	})

	t.Run("a write under a path that cannot be a directory fails", func(t *testing.T) {
		ctx := context.Background()
		env := makeEnv(t)
		writeText(t, filepath.Join(env.home, "blocked"), "file")
		blocked := domain.EnvLookup{}
		for key, value := range env.env {
			blocked[key] = value
		}
		blocked["RADAR_HOME"] = filepath.Join(env.home, "blocked", "state")
		if err := WriteSettings(ctx, blocked, DefaultSettings); err == nil {
			t.Fatal("writeSettings under a file = nil, want an error")
		}
	})
}

// The file WriteJSONAtomic leaves is JSON.stringify(v, null, 2) plus a newline: `<`, `>`, `&`
// and the two line separators stay raw, the way the dashboard's own writes leave them.
func TestWriteJSONAtomicKeepsJSONStringifyBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.json")
	value := struct {
		Text string `json:"text"`
	}{Text: "a<b>c&d e f"}
	if err := WriteJSONAtomic(path, value); err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"text\": \"a<b>c&d e f\"\n}\n"
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("file = %q, want %q", got, want)
	}
}
