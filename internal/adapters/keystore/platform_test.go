package keystore_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/adapters/keystore"
)

// testKey is the fake key the tests store: long enough for the shape, never a real secret.
const testKey = "huddle-fake-key-0123456789abcd"

// keyID is the reference secret's identity in a store.
var keyID = keystore.KeyIdentity{Service: "zai-plugin-cc", Account: "api-key", Display: "Z.ai GLM"}

// runCall is one tool run a store made.
type runCall struct {
	Cmd   string
	Args  []string
	Stdin *string
}

// fakeRunner scripts every store's tool run and records the calls.
type fakeRunner struct {
	Calls  []runCall
	Answer func(runCall) keystore.RunResult
}

func (f *fakeRunner) run(ctx context.Context, cmd string, args []string, stdin *string) keystore.RunResult {
	call := runCall{Cmd: cmd, Args: args, Stdin: stdin}
	f.Calls = append(f.Calls, call)
	if f.Answer != nil {
		return f.Answer(call)
	}
	code := 0
	return keystore.RunResult{Code: &code}
}

// asRunner adapts the fake to the keystore Runner: the signatures match, the named types live one
// per package.
func (f *fakeRunner) asRunner() keystore.Runner {
	return f.run
}

func strPtr(text string) *string { return &text }

// okRes is a tool run that exited 0 with no output.
func okRes() keystore.RunResult {
	code := 0
	return keystore.RunResult{Code: &code}
}

// res is a tool run that exited code with the given output.
func res(code int, stdout string, stderr string) keystore.RunResult {
	return keystore.RunResult{Code: &code, Stdout: stdout, Stderr: stderr}
}

// missingRes is a tool that could not be started.
func missingRes() keystore.RunResult {
	return keystore.RunResult{Missing: true}
}

// assertCall checks one recorded run: the command, its argv, and the stdin it was handed (nil:
// none). The values are never printed: the key travels on some of these stdins.
func assertCall(t *testing.T, got runCall, cmd string, args []string, stdin *string) {
	t.Helper()
	if got.Cmd != cmd {
		t.Errorf("cmd = %q, want %q", got.Cmd, cmd)
	}
	if !slices.Equal(got.Args, args) {
		t.Errorf("args = %v, want %v", got.Args, args)
	}
	if !stdinEqual(got.Stdin, stdin) {
		t.Error("stdin mismatch")
	}
}

func stdinEqual(got *string, want *string) bool {
	if got == nil || want == nil {
		return got == want
	}
	return *got == *want
}

// argvOf collects every argument the fake saw.
func argvOf(fake *fakeRunner) []string {
	var argv []string
	for _, call := range fake.Calls {
		argv = append(argv, call.Cmd)
		argv = append(argv, call.Args...)
	}
	return argv
}

// expectKeyOnlyOnStdin: the key must be in the stdin of at least one call and in no argv element.
func expectKeyOnlyOnStdin(t *testing.T, fake *fakeRunner) {
	t.Helper()
	for _, arg := range argvOf(fake) {
		if strings.Contains(arg, testKey) {
			t.Fatal("the key reached an argv element")
		}
	}
	for _, call := range fake.Calls {
		if call.Stdin != nil && strings.Contains(*call.Stdin, testKey) {
			return
		}
	}
	t.Fatal("the key never travelled on stdin")
}

func TestKeyShape(t *testing.T) {
	t.Run("accepts 16-512 chars of [A-Za-z0-9._-] and nothing else", func(t *testing.T) {
		for _, key := range []string{
			testKey,
			strings.Repeat("a", 16),
			strings.Repeat("a", 512),
		} {
			if !keystore.IsValidKey(key) {
				t.Errorf("IsValidKey(%d chars) = false, want true", len(key))
			}
		}
		for _, key := range []string{
			strings.Repeat("a", 15),
			strings.Repeat("a", 513),
			strings.Repeat("a", 16) + `"`,
			strings.Repeat("a", 16) + " x",
			strings.Repeat("a", 16) + "\n",
		} {
			if keystore.IsValidKey(key) {
				t.Errorf("IsValidKey(%d chars) = true, want false", len(key))
			}
		}
	})
}

func TestMacOSKeychain(t *testing.T) {
	ctx := context.Background()

	t.Run("stores through `security -i` with the command line on stdin, then reads it back", func(t *testing.T) {
		fake := &fakeRunner{}
		fake.Answer = func(call runCall) keystore.RunResult {
			if call.Args[0] == "find-generic-password" {
				return res(0, testKey+"\n", "")
			}
			return okRes()
		}
		if err := keystore.MacosKeyStore(keyID, fake.asRunner()).Set(ctx, testKey); err != nil {
			t.Fatalf("set: %v", err)
		}
		assertCall(t, fake.Calls[0], keystore.Security, []string{"-i"},
			strPtr(`add-generic-password -U -s "zai-plugin-cc" -a "api-key" -w "`+testKey+`"`+"\n"))
		assertCall(t, fake.Calls[1], keystore.Security,
			[]string{"find-generic-password", "-s", "zai-plugin-cc", "-a", "api-key", "-w"}, nil)
		expectKeyOnlyOnStdin(t, fake)
	})

	t.Run("a write the read-back does not confirm fails without naming the key", func(t *testing.T) {
		fake := &fakeRunner{}
		fake.Answer = func(call runCall) keystore.RunResult {
			if call.Args[0] == "find-generic-password" {
				return res(44, "", "")
			}
			return okRes()
		}
		err := keystore.MacosKeyStore(keyID, fake.asRunner()).Set(ctx, testKey)
		if err == nil || err.Error() != "could not store the key in macOS Keychain (exit 44)" {
			t.Fatalf("set error = %v, want the exit 44 failure", err)
		}
		fake.Answer = func(runCall) keystore.RunResult {
			return res(1, "", "bad "+testKey)
		}
		err = keystore.MacosKeyStore(keyID, fake.asRunner()).Set(ctx, testKey)
		if err == nil || err.Error() != "could not store the key in macOS Keychain (exit 1)" {
			t.Fatalf("set error = %v, want the exit 1 failure", err)
		}
		if strings.Contains(err.Error(), testKey) {
			t.Error("the failure names the key")
		}
	})

	t.Run("get trims the newline; not found is undefined; remove deletes through stdin; available probes", func(t *testing.T) {
		fake := &fakeRunner{}
		store := keystore.MacosKeyStore(keyID, fake.asRunner())
		fake.Answer = func(runCall) keystore.RunResult { return res(0, testKey+"\n", "") }
		if value, found := store.Get(ctx); value != testKey || !found {
			t.Errorf("get = %d chars %v, want the key", len(value), found)
		}
		fake.Answer = func(runCall) keystore.RunResult { return res(44, "", "not found") }
		if _, found := store.Get(ctx); found {
			t.Error("get found a key the tool did not return")
		}
		fake.Answer = func(runCall) keystore.RunResult { return okRes() }
		if _, found := store.Get(ctx); found {
			t.Error("get found a key in empty output")
		}
		if err := store.Remove(ctx); err != nil {
			t.Fatalf("remove: %v", err)
		}
		assertCall(t, fake.Calls[len(fake.Calls)-1], keystore.Security, []string{"-i"},
			strPtr(`delete-generic-password -s "zai-plugin-cc" -a "api-key"`+"\n"))
		if !store.Available(ctx) {
			t.Error("available = false, want true")
		}
		if want := []string{"default-keychain"}; !slices.Equal(fake.Calls[len(fake.Calls)-1].Args, want) {
			t.Errorf("available probe args = %v, want %v", fake.Calls[len(fake.Calls)-1].Args, want)
		}
		fake.Answer = func(runCall) keystore.RunResult { return missingRes() }
		if store.Available(ctx) {
			t.Error("available = true with the tool missing, want false")
		}
		err := store.Remove(ctx)
		if err == nil || err.Error() != "could not remove the key in macOS Keychain (tool not found)" {
			t.Fatalf("remove error = %v, want the tool-not-found failure", err)
		}
		if store.Label() != "macOS Keychain" {
			t.Errorf("label = %q", store.Label())
		}
	})
}

func TestSecretService(t *testing.T) {
	ctx := context.Background()

	t.Run("stores with the key on stdin only (no trailing newline), labelled with the provider", func(t *testing.T) {
		fake := &fakeRunner{}
		if err := keystore.LinuxKeyStore(keyID, fake.asRunner()).Set(ctx, testKey); err != nil {
			t.Fatalf("set: %v", err)
		}
		if len(fake.Calls) != 1 {
			t.Fatalf("runs = %d, want 1", len(fake.Calls))
		}
		assertCall(t, fake.Calls[0], "secret-tool",
			[]string{"store", "--label=Z.ai GLM API key", "service", "zai-plugin-cc", "account", "api-key"},
			strPtr(testKey))
		expectKeyOnlyOnStdin(t, fake)
		fake.Answer = func(runCall) keystore.RunResult { return res(1, "", "") }
		err := keystore.LinuxKeyStore(keyID, fake.asRunner()).Set(ctx, testKey)
		if err == nil || err.Error() != "could not store the key in Secret Service (exit 1)" {
			t.Fatalf("set error = %v, want the exit 1 failure", err)
		}
	})

	t.Run("lookup and clear by service and account", func(t *testing.T) {
		fake := &fakeRunner{}
		store := keystore.LinuxKeyStore(keyID, fake.asRunner())
		fake.Answer = func(runCall) keystore.RunResult { return res(0, testKey, "") }
		if value, found := store.Get(ctx); value != testKey || !found {
			t.Errorf("get = %d chars %v, want the key", len(value), found)
		}
		if want := []string{"lookup", "service", "zai-plugin-cc", "account", "api-key"}; !slices.Equal(fake.Calls[0].Args, want) {
			t.Errorf("lookup args = %v, want %v", fake.Calls[0].Args, want)
		}
		if store.Label() != "Secret Service" {
			t.Errorf("label = %q", store.Label())
		}
		fake.Answer = func(runCall) keystore.RunResult { return res(1, "", "") }
		if _, found := store.Get(ctx); found {
			t.Error("get found a key the tool did not return")
		}
		if err := store.Remove(ctx); err != nil {
			t.Fatalf("remove: %v", err)
		}
		want := []string{"clear", "service", "zai-plugin-cc", "account", "api-key"}
		if !slices.Equal(fake.Calls[len(fake.Calls)-1].Args, want) {
			t.Errorf("clear args = %v, want %v", fake.Calls[len(fake.Calls)-1].Args, want)
		}
		fake.Answer = func(runCall) keystore.RunResult { return missingRes() }
		if err := store.Remove(ctx); err == nil || !strings.Contains(err.Error(), "tool not found") {
			t.Fatalf("remove error = %v, want the tool-not-found failure", err)
		}
	})

	t.Run("is unavailable when secret-tool is missing or the bus has no secret service", func(t *testing.T) {
		fake := &fakeRunner{}
		store := keystore.LinuxKeyStore(keyID, fake.asRunner())
		cases := []struct {
			answer    keystore.RunResult
			available bool
		}{
			{res(0, testKey, ""), true},
			{res(1, "", ""), true},
			{res(1, "", "Cannot autolaunch D-Bus without X11 $DISPLAY"), false},
			{res(1, "", "The name org.freedesktop.secrets was not provided"), false},
			{missingRes(), false},
		}
		for _, c := range cases {
			fake.Answer = func(runCall) keystore.RunResult { return c.answer }
			if store.Available(ctx) != c.available {
				t.Errorf("available after code %v stderr %q = %v, want %v",
					c.answer.Code, c.answer.Stderr, !c.available, c.available)
			}
		}
	})
}

func TestEveryPlatformStore(t *testing.T) {
	ctx := context.Background()
	stores := []struct {
		name   string
		kind   keystore.KeyStoreKind
		create func(keystore.KeyIdentity, keystore.Runner) keystore.KeyStore
	}{
		{"macos", keystore.KeyStoreMacOS, keystore.MacosKeyStore},
		{"linux", keystore.KeyStoreLinux, keystore.LinuxKeyStore},
	}
	for _, store := range stores {
		t.Run(store.name+": an invalid key is rejected before any command runs", func(t *testing.T) {
			fake := &fakeRunner{}
			keyStore := store.create(keyID, fake.asRunner())
			if keyStore.Kind() != store.kind {
				t.Errorf("kind = %q, want %q", keyStore.Kind(), store.kind)
			}
			for _, bad := range []string{
				"short",
				strings.Repeat("a", 20) + `" ; rm -rf ~`,
				strings.Repeat("a", 20) + "\nsecond line",
			} {
				err := keyStore.Set(ctx, bad)
				var invalid keystore.InvalidKeyError
				if !errors.As(err, &invalid) {
					t.Fatalf("set(%d chars) error = %v, want InvalidKeyError", len(bad), err)
				}
				if strings.Contains(err.Error(), bad) {
					t.Error("the error names the rejected key")
				}
			}
			if len(fake.Calls) != 0 {
				t.Fatalf("runs = %d, want 0", len(fake.Calls))
			}
		})
	}
}
