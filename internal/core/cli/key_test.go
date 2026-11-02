package cli_test

// The key suite: `key` reads a provider key from stdin into the key store, refuses it as an
// argument, and never prints it. Every key here is a made-up value built at run time.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/adapters/keystore"
	"github.com/MuhmdRaouf/huddle/internal/core/cli"
	"github.com/MuhmdRaouf/huddle/internal/core/ports"
	"github.com/MuhmdRaouf/huddle/internal/core/testkit"
	"github.com/MuhmdRaouf/huddle/internal/core/testkit/fakedeps"
)

// keyFakeKey is a value no provider issues, in the shape the stores accept.
func keyFakeKey() string {
	return strings.Join([]string{"fake", "key", "not", "real", "0123456789"}, "-")
}

// keyEntry is a KeyEntry that only holds the store `key` writes to.
type keyEntry struct {
	store ports.KeyStore
}

func (e keyEntry) Store(context.Context) ports.KeyStore { return e.store }

func (keyEntry) Check(context.Context, string) ports.KeyVerdict { return ports.KeyVerdictAccepted }

func (keyEntry) OpenPage(context.Context) (string, error) { return "", nil }

func (keyEntry) PageOpen(context.Context, string) bool { return false }

func (keyEntry) RemoveFromFile(context.Context, string) bool { return false }

// keyWorld is the fakes with a key file under the temp home as the store.
func keyWorld(t *testing.T) (*fakedeps.Fakes, ports.KeyStore) {
	t.Helper()
	world := fakedeps.FakeDeps(t)
	file := filepath.Join(t.TempDir(), "home", "keys.env")
	store := keystore.FileKeyStore(testkit.ReferenceProvider, file)
	world.Deps.Host.KeyEntry = keyEntry{store: store}
	return &world, store
}

// keyStdin makes text the process's stdin for the test.
func keyStdin(t *testing.T, text string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin.txt")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open stdin: %v", err)
	}
	old := os.Stdin
	os.Stdin = file
	t.Cleanup(func() {
		os.Stdin = old
		_ = file.Close()
	})
}

// keyTranscript is everything the run printed, on both streams.
func keyTranscript(world fakedeps.Fakes) string {
	return strings.Join(append(append([]string{}, world.Out.Lines...), world.Out.Errors...), "\n")
}

func TestKeyRefusesAnArgument(t *testing.T) {
	world, store := keyWorld(t)
	key := keyFakeKey()
	keyStdin(t, key+"\n")

	if code := cliRun(t, *world, "key", key); code != cli.ExitUnexpected {
		t.Fatalf("exit = %d, want 1", code)
	}
	if _, found := store.Get(context.Background()); found {
		t.Fatal("a key given as an argument was stored")
	}
	if len(world.Out.Errors) != 1 {
		t.Fatalf("error lines = %d, want 1", len(world.Out.Errors))
	}
	if strings.Contains(keyTranscript(*world), key) {
		t.Fatal("the refusal echoed the argument")
	}
}

func TestKeyReadsStdin(t *testing.T) {
	t.Run("stores the key stdin carries, without its line break", func(t *testing.T) {
		world, store := keyWorld(t)
		key := keyFakeKey()
		keyStdin(t, key+"\n")

		if code := cliRun(t, *world, "key"); code != cli.ExitOK {
			t.Fatalf("exit = %d, want 0 (%s)", code, keyTranscript(*world))
		}
		stored, found := store.Get(context.Background())
		if !found || stored != key {
			t.Fatalf("stored key matches = %v, found = %v", stored == key, found)
		}
		if want := "key: saved in " + store.Label(); !strings.Contains(world.Out.Text(), want) {
			t.Fatalf("%q missing from %q", want, world.Out.Text())
		}
	})

	t.Run("an empty stdin stores nothing and exits 1", func(t *testing.T) {
		world, store := keyWorld(t)
		keyStdin(t, "\n")

		if code := cliRun(t, *world, "key"); code != cli.ExitUnexpected {
			t.Fatalf("exit = %d, want 1", code)
		}
		if _, found := store.Get(context.Background()); found {
			t.Fatal("an empty key was stored")
		}
		if want := "key: stdin held no key"; !strings.Contains(keyTranscript(*world), want) {
			t.Fatalf("%q missing from %q", want, keyTranscript(*world))
		}
	})

	t.Run("a key the store refuses exits 1 with the store's reason", func(t *testing.T) {
		world, store := keyWorld(t)
		keyStdin(t, "short\n")

		if code := cliRun(t, *world, "key"); code != cli.ExitUnexpected {
			t.Fatalf("exit = %d, want 1", code)
		}
		if _, found := store.Get(context.Background()); found {
			t.Fatal("a refused key was stored")
		}
		reason := keystore.InvalidKeyError{}
		if want := "key: " + reason.Error(); !strings.Contains(keyTranscript(*world), want) {
			t.Fatalf("%q missing from %q", want, keyTranscript(*world))
		}
	})

	t.Run("a host without a key store exits 1", func(t *testing.T) {
		world, _ := keyWorld(t)
		world.Deps.Host.KeyEntry = nil
		keyStdin(t, keyFakeKey()+"\n")

		if code := cliRun(t, *world, "key"); code != cli.ExitUnexpected {
			t.Fatalf("exit = %d, want 1", code)
		}
		if want := "key: this host has no key store"; !strings.Contains(keyTranscript(*world), want) {
			t.Fatalf("%q missing from %q", want, keyTranscript(*world))
		}
	})
}

func TestKeyNeverEchoes(t *testing.T) {
	key := keyFakeKey()
	cases := map[string]func(world *fakedeps.Fakes){
		"saved": func(*fakedeps.Fakes) {},
		"refused by the store": func(world *fakedeps.Fakes) {
			memory := testkit.NewMemoryKeyStore("", "", "")
			memory.FailSet = true
			world.Deps.Host.KeyEntry = keyEntry{store: memory}
		},
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			world, _ := keyWorld(t)
			arrange(world)
			keyStdin(t, key+"\n")

			cliRun(t, *world, "key")
			if strings.Contains(keyTranscript(*world), key) {
				t.Fatal("the key reached the output")
			}
		})
	}
}
