package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// spinnerTipsEnv is one test machine for the spinner tips: a temp CLAUDE_CONFIG_DIR, a temp
// huddle home beside it, no real home anywhere.
func spinnerTipsEnv(t *testing.T) (Env, string, string) {
	t.Helper()
	dir := t.TempDir()
	hud := t.TempDir()
	env := Env{Lookup: func(key string) string {
		if key == "CLAUDE_CONFIG_DIR" {
			return dir
		}
		return ""
	}}
	return env, dir, hud
}

// spinnerTipsSettings is the settings file the env resolves.
func spinnerTipsSettings(dir string) string {
	return filepath.Join(dir, "settings.json")
}

// TestSpinnerTipsInstallCreatesAndRemoveTakesTheFileAway pins the missing-file case: install
// writes a settings file of one member, remove takes the file away again.
func TestSpinnerTipsInstallCreatesAndRemoveTakesTheFileAway(t *testing.T) {
	env, dir, hud := spinnerTipsEnv(t)
	tips := filepath.Join(hud, "spinner-tips.json")
	got, err := InstallSpinnerTips(hud, env, tips)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !got.Created || !got.Changed {
		t.Fatalf("install = %+v, want created", got)
	}
	raw, err := os.ReadFile(spinnerTipsSettings(dir))
	if err != nil {
		t.Fatalf("install wrote no file: %v", err)
	}
	if want := `"spinnerTipsOverride":{"tipsFile":` + jsonString(tips) + `}`; !strings.Contains(string(raw), want) {
		t.Fatalf("the created file holds %s, want the tipsFile entry in it", raw)
	}
	info, err := os.Stat(spinnerTipsSettings(dir))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("settings mode %o, want 0600", info.Mode().Perm())
	}

	removed, err := RemoveSpinnerTips(hud, env)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !removed.Removed || !removed.FileGone {
		t.Fatalf("remove = %+v, want the created file gone", removed)
	}
	if _, err := os.Stat(spinnerTipsSettings(dir)); !os.IsNotExist(err) {
		t.Errorf("the created file is still there: %v", err)
	}
	if again, err := RemoveSpinnerTips(hud, env); err != nil || !again.NotThere {
		t.Errorf("second remove = (%+v, %v), want not-there", again, err)
	}
}

// TestSpinnerTipsInstallThenRemoveRestoresExact pins the round trip on a file the user wrote:
// install splices the entry in around the members already there, and remove gives the bytes
// back exactly.
func TestSpinnerTipsInstallThenRemoveRestoresExact(t *testing.T) {
	env, dir, hud := spinnerTipsEnv(t)
	before := "{\"unrelated\":\"keep\"}\n"
	writeConfig(t, spinnerTipsSettings(dir), before)
	tips := filepath.Join(hud, "spinner-tips.json")

	got, err := InstallSpinnerTips(hud, env, tips)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !got.Changed || got.Already || got.Created {
		t.Fatalf("install = %+v, want changed on an existing file", got)
	}
	after := readFile(t, spinnerTipsSettings(dir))
	if want := `"spinnerTipsOverride":{"tipsFile":` + jsonString(tips) + `}`; !strings.Contains(after, want) {
		t.Fatalf("install wrote no tipsFile entry: %s", after)
	}
	if !strings.Contains(after, `"unrelated":"keep"`) {
		t.Fatalf("install disturbed the other keys: %s", after)
	}

	removed, err := RemoveSpinnerTips(hud, env)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !removed.Removed {
		t.Fatalf("remove = %+v, want removed", removed)
	}
	if got := readFile(t, spinnerTipsSettings(dir)); got != before {
		t.Fatalf("remove did not restore the file byte for byte:\nwant %q\ngot  %q", before, got)
	}
}

// TestSpinnerTipsInstallOverAnOverrideRestoresIt pins the replaced-value case: the entry the
// user already had comes back byte for byte.
func TestSpinnerTipsInstallOverAnOverrideRestoresIt(t *testing.T) {
	env, dir, hud := spinnerTipsEnv(t)
	before := "{\n  \"model\": \"opus\",\n  \"spinnerTipsOverride\": {\"tips\": [\"the user's own tip\"]}\n}\n"
	writeConfig(t, spinnerTipsSettings(dir), before)
	tips := filepath.Join(hud, "spinner-tips.json")

	got, err := InstallSpinnerTips(hud, env, tips)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !got.Changed || got.Already {
		t.Fatalf("install = %+v, want changed", got)
	}
	if after := readFile(t, spinnerTipsSettings(dir)); !strings.Contains(after, `"tipsFile"`) {
		t.Fatalf("install wrote no tipsFile: %s", after)
	}
	ledger := readLedger(ledgerPath(hud))
	if record, has := ledger.named(spinnerTipsLedgerName); !has {
		t.Fatal("the ledger holds no spinner-tips record")
	} else if want := `{"tips": ["the user's own tip"]}`; record.Before != want {
		t.Fatalf("the ledger records Before = %q, want %q", record.Before, want)
	}

	removed, err := RemoveSpinnerTips(hud, env)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !removed.Removed {
		t.Fatalf("remove = %+v, want removed", removed)
	}
	if got := readFile(t, spinnerTipsSettings(dir)); got != before {
		t.Fatalf("remove did not restore the old value byte for byte:\nwant %q\ngot  %q", before, got)
	}
	after := readLedger(ledgerPath(hud))
	if _, has := after.named(spinnerTipsLedgerName); has {
		t.Error("the ledger still records spinner tips after remove")
	}
}

// TestSpinnerTipsInstallIsIdempotent pins the second run: the entry is already exactly ours,
// the file is untouched and the ledger keeps the first install's previous value.
func TestSpinnerTipsInstallIsIdempotent(t *testing.T) {
	env, dir, hud := spinnerTipsEnv(t)
	tips := filepath.Join(hud, "spinner-tips.json")
	writeConfig(t, spinnerTipsSettings(dir), "{\"spinnerTipsOverride\":{\"tips\":[\"mine\"]}}\n")
	if _, err := InstallSpinnerTips(hud, env, tips); err != nil {
		t.Fatalf("first install: %v", err)
	}
	once := readFile(t, spinnerTipsSettings(dir))
	got, err := InstallSpinnerTips(hud, env, tips)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if !got.Already || got.Changed {
		t.Fatalf("second install = %+v, want already", got)
	}
	if again := readFile(t, spinnerTipsSettings(dir)); again != once {
		t.Fatalf("the second install rewrote the file:\nwant %q\ngot  %q", once, again)
	}
	ledger := readLedger(ledgerPath(hud))
	if record, has := ledger.named(spinnerTipsLedgerName); !has {
		t.Fatal("the ledger holds no spinner-tips record")
	} else if want := `{"tips":["mine"]}`; record.Before != want {
		t.Fatalf("the ledger records Before = %q, want the first install's previous %q", record.Before, want)
	}
}

// TestSpinnerTipsRefuseAMalformedFile keeps the refuse-before-write rule: a settings file that
// does not parse is left as it is, by install and by remove.
func TestSpinnerTipsRefuseAMalformedFile(t *testing.T) {
	env, dir, hud := spinnerTipsEnv(t)
	before := "{\"model\": "
	writeConfig(t, spinnerTipsSettings(dir), before)
	if _, err := InstallSpinnerTips(hud, env, filepath.Join(hud, "spinner-tips.json")); err == nil {
		t.Fatal("install on a malformed file = nil error, want a refusal")
	}
	if got := readFile(t, spinnerTipsSettings(dir)); got != before {
		t.Fatalf("install touched the malformed file: %q", got)
	}
	if _, err := RemoveSpinnerTips(hud, env); err == nil {
		t.Fatal("remove on a malformed file = nil error, want a refusal")
	}
	if got := readFile(t, spinnerTipsSettings(dir)); got != before {
		t.Fatalf("remove touched the malformed file: %q", got)
	}
}

// TestSpinnerTipsRemoveWithoutARecordTakesOursOut pins the lost-ledger case: no record and a
// value recognisably ours still comes out, the rest of the file untouched.
func TestSpinnerTipsRemoveWithoutARecordTakesOursOut(t *testing.T) {
	env, dir, hud := spinnerTipsEnv(t)
	before := "{\n  \"model\": \"opus\",\n  \"spinnerTipsOverride\": {\"tipsFile\": \"/somewhere/spinner-tips.json\"}\n}\n"
	writeConfig(t, spinnerTipsSettings(dir), before)
	got, err := RemoveSpinnerTips(hud, env)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !got.Removed {
		t.Fatalf("remove = %+v, want removed", got)
	}
	after := readFile(t, spinnerTipsSettings(dir))
	if strings.Contains(after, "spinnerTipsOverride") {
		t.Fatalf("remove left the member in: %s", after)
	}
	if !strings.Contains(after, "\"model\": \"opus\"") {
		t.Fatalf("remove disturbed the other keys: %s", after)
	}
}

// TestSpinnerTipsRemoveLeavesAStrangersValue pins the user's own entry: no record and a value
// that is not ours is not-there, the file untouched.
func TestSpinnerTipsRemoveLeavesAStrangersValue(t *testing.T) {
	env, dir, hud := spinnerTipsEnv(t)
	before := "{\"spinnerTipsOverride\":{\"tips\":[\"the user's own tips\"]}}\n"
	writeConfig(t, spinnerTipsSettings(dir), before)
	got, err := RemoveSpinnerTips(hud, env)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !got.NotThere {
		t.Fatalf("remove = %+v, want not-there", got)
	}
	if readFile(t, spinnerTipsSettings(dir)) != before {
		t.Errorf("remove touched the file")
	}
}
