package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The file-level edges of install and remove: a name no harness answers, a config that
// cannot be read, a ledger that cannot be written, the two "nothing of ours" answers a
// remove can give, and a directory that will not take the write.

func TestInstallAndRemoveRefuseANameNoHarnessHas(t *testing.T) {
	env, home := testEnv(t, nil)
	entry := testEntry(home, "gemini")
	if _, err := Install(context.Background(), home, env, "not-a-harness", entry); err == nil {
		t.Error("install took an unknown harness")
	}
	if _, err := Remove(context.Background(), home, env, "not-a-harness", entry); err == nil {
		t.Error("remove took an unknown harness")
	}
}

func TestInstallAndRemoveRefuseAConfigTheyCannotRead(t *testing.T) {
	env, home := testEnv(t, nil)
	a, err := ByName("gemini")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(a.ConfigPath(env), 0o700); err != nil {
		t.Fatal(err)
	}
	entry := testEntry(home, "gemini")
	if _, err := Install(context.Background(), home, env, "gemini", entry); err == nil {
		t.Error("install read a directory for a config")
	}
	if _, err := Remove(context.Background(), home, env, "gemini", entry); err == nil {
		t.Error("remove read a directory for a config")
	}
}

// A remove of a trace that is not there: nothing recorded and nothing on disk answers
// "not there"; a record whose file is gone drops the record and answers the same.
func TestRemoveOfATraceThatIsNotThere(t *testing.T) {
	env, home := testEnv(t, nil)
	entry := testEntry(home, "gemini")
	got, err := Remove(context.Background(), home, env, "gemini", entry)
	if err != nil || !got.NotThere {
		t.Fatalf("remove = %+v, %v; want not-there", got, err)
	}

	a, err := ByName("gemini")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Install(context.Background(), home, env, "gemini", entry); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(a.ConfigPath(env)); err != nil {
		t.Fatal(err)
	}
	got, err = Remove(context.Background(), home, env, "gemini", entry)
	if err != nil || !got.NotThere {
		t.Fatalf("remove = %+v, %v; want not-there with the record dropped", got, err)
	}
	assertLedger(t, ledgerPath(home), ledgerCheck{name: "gemini", gone: true})
}

// An entry that was taken out by hand leaves nothing for remove to do: the file holds no
// entry of ours any more, and remove says exactly that.
func TestRemoveOfAnEntryGoneByHand(t *testing.T) {
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			env, home := testEnv(t, nil)
			a, err := ByName(name)
			if err != nil {
				t.Fatal(err)
			}
			entry := testEntry(home, name)
			path := a.ConfigPath(env)
			if _, err := Install(context.Background(), home, env, name, entry); err != nil {
				t.Fatal(err)
			}
			writeConfig(t, path, fixturesFor(t, a)[1].body) // the file without huddle
			got, err := Remove(context.Background(), home, env, name, entry)
			if err != nil || !got.NotThere {
				t.Fatalf("remove = %+v, %v; want not-there", got, err)
			}
		})
	}
}

// A ledger that cannot be written fails the install that needs it.
func TestInstallRefusesWhenTheLedgerCannotBeWritten(t *testing.T) {
	env, home := testEnv(t, nil)
	hudHome := filepath.Join(home, "ledger-home")
	if err := os.WriteFile(hudHome, []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(context.Background(), hudHome, env, "gemini", testEntry(home, "gemini")); err == nil {
		t.Error("install wrote no ledger and said nothing")
	}
}

// A ledger a crash half wrote reads as empty; a versionless one is version 1; dropping
// one harness keeps the others.
func TestTheLedgerSurvivesGarbageAndKeepsItsOtherEntries(t *testing.T) {
	hud := t.TempDir()
	if err := os.WriteFile(ledgerPath(hud), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ledger := readLedger(ledgerPath(hud)); ledger.Version != 1 || len(ledger.Entries) != 0 {
		t.Errorf("readLedger on garbage = %+v, want an empty version 1", ledger)
	}
	if err := os.WriteFile(ledgerPath(hud), []byte(`{"entries":[{"harness":"kept","path":"/x"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if ledger := readLedger(ledgerPath(hud)); ledger.Version != 1 || len(ledger.Entries) != 1 {
		t.Errorf("readLedger on a versionless ledger = %+v, want version 1", ledger)
	}
	if err := recordLedger(hud, ledgerEntry{Harness: "dropped", Path: "/y"}); err != nil {
		t.Fatal(err)
	}
	if err := dropLedger(hud, "dropped"); err != nil {
		t.Fatal(err)
	}
	ledger := readLedger(ledgerPath(hud))
	if _, there := ledger.named("dropped"); there {
		t.Error("the dropped record is still in the ledger")
	}
	if _, there := ledger.named("kept"); !there {
		t.Error("the drop took the other harness's record with it")
	}
}

// A config whose directory will not take a write fails the remove loudly: the write-back
// for a file that keeps other servers, and the file-gone remove for one install created.
func TestRemoveRefusesAConfigItsDirectoryWillNotTake(t *testing.T) {
	t.Run("the write back", func(t *testing.T) {
		env, home := testEnv(t, nil)
		a, err := ByName("codex")
		if err != nil {
			t.Fatal(err)
		}
		entry := testEntry(home, "codex")
		path := a.ConfigPath(env)
		writeConfig(t, path, fixturesFor(t, a)[2].body)
		if _, err := Install(context.Background(), home, env, "codex", entry); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Dir(path), 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Dir(path), 0o700) })
		if _, err := Remove(context.Background(), home, env, "codex", entry); err == nil {
			t.Error("remove wrote its result into a read-only directory and said nothing")
		}
	})
	t.Run("the file install created", func(t *testing.T) {
		env, home := testEnv(t, nil)
		a, err := ByName("gemini")
		if err != nil {
			t.Fatal(err)
		}
		entry := testEntry(home, "gemini")
		path := a.ConfigPath(env)
		if _, err := Install(context.Background(), home, env, "gemini", entry); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Dir(path), 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Dir(path), 0o700) })
		if _, err := Remove(context.Background(), home, env, "gemini", entry); err == nil {
			t.Error("remove took a created file's directory write for granted")
		}
	})
}

// The write that lays a config or the ledger down fails loudly on a home that cannot be
// made, a file that cannot be written, and a rename onto a directory — and the failed
// rename leaves no temp file behind.
func TestWriteAtomic0600RefusesImpossibleTargets(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic0600(filepath.Join(blocker, "f"), nil); err == nil {
		t.Error("writeAtomic made a home under a file")
	}

	frozen := t.TempDir()
	if err := os.Chmod(frozen, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(frozen, 0o700) })
	if err := writeAtomic0600(filepath.Join(frozen, "f"), nil); err == nil {
		t.Error("writeAtomic wrote into a read-only directory")
	}

	parent := t.TempDir()
	taken := filepath.Join(parent, "taken")
	if err := os.MkdirAll(taken, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic0600(taken, []byte("x")); err == nil {
		t.Error("writeAtomic renamed its temp file over a directory")
	}
	left, err := filepath.Glob(filepath.Join(parent, "*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("the failed rename left its temp file: %v", left)
	}
}

// A missing file reads as nothing, quietly; a file that cannot be read is an error.
func TestReadIfExists(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	raw, existed, err := readIfExists(missing)
	if err != nil || existed || raw != nil {
		t.Errorf("readIfExists = %v, %v, %v; want nil, false, nil", raw, existed, err)
	}
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readIfExists(filepath.Join(blocker, "f")); err == nil {
		t.Error("readIfExists read through a file")
	}
}

// The kind helpers answer false for anything that is not one of the two config kinds.
func TestKindHelpersAnswerFalseForAnUnknownAdapter(t *testing.T) {
	if containsEntry(nil, []byte("{}")) {
		t.Error("containsEntry claimed an unknown adapter's config")
	}
	if emptiedConfig(nil, []byte("{}")) {
		t.Error("emptiedConfig claimed an unknown adapter's config")
	}
}

// A malformed config error unwraps to the parse error that caused it and names the file.
func TestMalformedErrorUnwraps(t *testing.T) {
	cause := errors.New("the parse failed")
	err := &MalformedError{Path: "/users/x/config.toml", Err: cause}
	if !errors.Is(err, cause) {
		t.Errorf("errors.Is = false, want the cause unwrapped from %v", err)
	}
	if !strings.Contains(err.Error(), "/users/x/config.toml") || !strings.Contains(err.Error(), "the parse failed") {
		t.Errorf("error = %q, want the path and the cause", err.Error())
	}
}
