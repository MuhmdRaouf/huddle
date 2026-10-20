package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// statuslineEnv is one test machine for the statusline word: a temp HOME (no real
// CLAUDE_CONFIG_DIR), the huddle home beside it.
func statuslineEnv(t *testing.T) (Env, string, string) {
	t.Helper()
	home := t.TempDir()
	hud := t.TempDir()
	env := Env{Lookup: func(key string) string {
		if key == "CLAUDE_CONFIG_DIR" {
			return ""
		}
		if key == "HOME" {
			return home
		}
		return ""
	}}
	return env, home, hud
}

// statuslineSettings is the settings file the env resolves.
func statuslineSettings(home string) string {
	return filepath.Join(home, ".claude", "settings.json")
}

// readFile is the tests' one-line file reader.
func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// TestStatusLineInstallWritesAndRemoveRestoresExact walks the round trip the card pins: a
// settings file with a statusline of the user's own gets huddle's entry, and remove gives the
// file back byte for byte.
func TestStatusLineInstallWritesAndRemoveRestoresExact(t *testing.T) {
	env, home, hud := statuslineEnv(t)
	before := "{\n  \"model\": \"opus\",\n  \"statusLine\": {\n    \"type\": \"command\",\n    \"command\": \"my own line\"\n  },\n  \"alwaysThinkingEnabled\": true\n}\n"
	writeConfig(t, statuslineSettings(home), before)

	got, err := InstallStatusLine(hud, env, filepath.Join(home, "bin", "huddle"))
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !got.Changed || got.Already || got.Created {
		t.Fatalf("install = %+v, want changed on an existing file", got)
	}
	after := readFile(t, statuslineSettings(home))
	if !strings.Contains(after, `"huddle statusline"`) && !strings.Contains(after, `statusline`) {
		t.Fatalf("install wrote no statusline word: %s", after)
	}
	// the other members keep their places and their bytes
	if !strings.Contains(after, "\"model\": \"opus\"") || !strings.Contains(after, "\"alwaysThinkingEnabled\": true") {
		t.Fatalf("install disturbed the other keys: %s", after)
	}
	// the previous value is in the ledger, so remove can give it back
	chain := StatusLineChain(hud)
	if chain != "my own line" {
		t.Fatalf("StatusLineChain = %q, want the replaced command", chain)
	}

	removed, err := RemoveStatusLine(hud, env)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !removed.Removed {
		t.Fatalf("remove = %+v, want removed", removed)
	}
	if got := readFile(t, statuslineSettings(home)); got != before {
		t.Fatalf("remove did not restore the file byte for byte:\nwant %q\ngot  %q", before, got)
	}
	if chain := StatusLineChain(hud); chain != "" {
		t.Fatalf("the ledger still records a statusline after remove: %q", chain)
	}
}

// TestStatusLineInstallWithoutAPreviousOne pins the no-key case: install adds the key and says
// so, remove takes the key back out and leaves the rest of the file as it was.
func TestStatusLineInstallWithoutAPreviousOne(t *testing.T) {
	env, home, hud := statuslineEnv(t)
	before := "{\"model\":\"opus\"}\n"
	writeConfig(t, statuslineSettings(home), before)

	got, err := InstallStatusLine(hud, env, filepath.Join(home, "bin", "huddle"))
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !got.Changed {
		t.Fatalf("install = %+v, want changed", got)
	}
	if after := readFile(t, statuslineSettings(home)); !strings.Contains(after, `"statusLine"`) {
		t.Fatalf("install wrote no statusLine: %s", after)
	}
	if chain := StatusLineChain(hud); chain != "" {
		t.Fatalf("StatusLineChain with no previous statusline = %q, want empty", chain)
	}

	if removed, err := RemoveStatusLine(hud, env); err != nil || !removed.Removed {
		t.Fatalf("remove = (%+v, %v), want removed", removed, err)
	}
	if got := readFile(t, statuslineSettings(home)); got != before {
		t.Fatalf("remove did not give the file back:\nwant %q\ngot  %q", before, got)
	}
}

// TestStatusLineInstallCreatesTheFile pins the missing-file case: install writes a settings
// file of one member, remove takes the file away again.
func TestStatusLineInstallCreatesTheFile(t *testing.T) {
	env, home, hud := statuslineEnv(t)
	got, err := InstallStatusLine(hud, env, filepath.Join(home, "bin", "huddle"))
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !got.Created || !got.Changed {
		t.Fatalf("install = %+v, want created", got)
	}
	raw, err := os.ReadFile(statuslineSettings(home))
	if err != nil {
		t.Fatalf("install wrote no file: %v", err)
	}
	if !strings.Contains(string(raw), `"statusLine"`) {
		t.Fatalf("the created file holds no statusLine: %s", raw)
	}
	info, err := os.Stat(statuslineSettings(home))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("settings mode %o, want 0600", info.Mode().Perm())
	}

	removed, err := RemoveStatusLine(hud, env)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !removed.Removed || !removed.FileGone {
		t.Fatalf("remove = %+v, want the created file gone", removed)
	}
	if _, err := os.Stat(statuslineSettings(home)); !os.IsNotExist(err) {
		t.Errorf("the created file is still there: %v", err)
	}
}

// TestStatusLineInstallIsIdempotent pins the second run: the entry is already exactly ours, the
// file is untouched and the ledger keeps the first install's previous value.
func TestStatusLineInstallIsIdempotent(t *testing.T) {
	env, home, hud := statuslineEnv(t)
	writeConfig(t, statuslineSettings(home), "{\"statusLine\":{\"type\":\"command\",\"command\":\"my own line\"}}\n")
	if _, err := InstallStatusLine(hud, env, filepath.Join(home, "bin", "huddle")); err != nil {
		t.Fatalf("first install: %v", err)
	}
	once := readFile(t, statuslineSettings(home))
	got, err := InstallStatusLine(hud, env, filepath.Join(home, "bin", "huddle"))
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if !got.Already || got.Changed {
		t.Fatalf("second install = %+v, want already", got)
	}
	if again := readFile(t, statuslineSettings(home)); again != once {
		t.Fatalf("the second install rewrote the file:\nwant %q\ngot  %q", once, again)
	}
	if chain := StatusLineChain(hud); chain != "my own line" {
		t.Fatalf("StatusLineChain after the second install = %q, want the first install's previous", chain)
	}
}

// TestStatusLineRemoveWithNothingOfOurs pins the empty hand: no record and no entry of ours is
// nothing to remove, said with the not-there answer.
func TestStatusLineRemoveWithNothingOfOurs(t *testing.T) {
	env, home, hud := statuslineEnv(t)
	before := "{\"model\":\"opus\"}\n"
	writeConfig(t, statuslineSettings(home), before)
	got, err := RemoveStatusLine(hud, env)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !got.NotThere {
		t.Fatalf("remove = %+v, want not-there", got)
	}
	if readFile(t, statuslineSettings(home)) != before {
		t.Errorf("remove touched the file")
	}
}

// TestStatusLineConfigDirWins pins the variable: a CLAUDE_CONFIG_DIR names the settings file,
// never the home's .claude.
func TestStatusLineConfigDirWins(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	env := Env{Lookup: func(key string) string {
		if key == "CLAUDE_CONFIG_DIR" {
			return dir
		}
		if key == "HOME" {
			return home
		}
		return ""
	}}
	if got := StatusLineSettingsPath(env); got != filepath.Join(dir, "settings.json") {
		t.Fatalf("StatusLineSettingsPath = %q, want the config dir's", got)
	}
}

// TestStatusLineInstallRefusesAMalformedFile keeps the refuse-before-write rule: a settings
// file that does not parse is left as it is.
func TestStatusLineInstallRefusesAMalformedFile(t *testing.T) {
	env, home, hud := statuslineEnv(t)
	before := "{\"model\": "
	writeConfig(t, statuslineSettings(home), before)
	if _, err := InstallStatusLine(hud, env, filepath.Join(home, "bin", "huddle")); err == nil {
		t.Fatal("install on a malformed file = nil error, want a refusal")
	}
	if got := readFile(t, statuslineSettings(home)); got != before {
		t.Fatalf("install touched the malformed file: %q", got)
	}
}

// TestStatusLineCommandNamesTheBin pins the entry: the command runs the binary it was given,
// quoted for a shell, with the statusline word after it.
func TestStatusLineCommandNamesTheBin(t *testing.T) {
	got := statusLineCommand("/opt/huddle-bin/current")
	if !strings.Contains(got, `"/opt/huddle-bin/current"`) || !strings.HasSuffix(got, " statusline") {
		t.Fatalf("statusLineCommand = %q, want the quoted binary and the word", got)
	}
	if got := statusLineCommand(`/weird"path/huddle`); !strings.Contains(got, `\"`) {
		t.Fatalf("statusLineCommand does not quote: %q", got)
	}
}

// TestStatusLineChainWithoutALedger answers empty on a home with no ledger, and answers empty
// when the recorded previous value runs no command.
func TestStatusLineChainWithoutALedger(t *testing.T) {
	if chain := StatusLineChain(t.TempDir()); chain != "" {
		t.Fatalf("StatusLineChain with no ledger = %q, want empty", chain)
	}
	env, home, hud := statuslineEnv(t)
	writeConfig(t, statuslineSettings(home), "{\"statusLine\":{\"type\":\"status\",\"text\":\"hi\"}}\n")
	if _, err := InstallStatusLine(hud, env, filepath.Join(home, "bin", "huddle")); err != nil {
		t.Fatalf("install: %v", err)
	}
	if chain := StatusLineChain(hud); chain != "" {
		t.Fatalf("StatusLineChain over a command-less statusline = %q, want empty", chain)
	}
}
