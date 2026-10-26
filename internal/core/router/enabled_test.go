package router

// providers.json: where huddle-router's enabled providers are recorded and read back. The file
// is the only state the daemon re-reads, so enabling a provider needs no restart.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

func TestHomeOf(t *testing.T) {
	env := domain.EnvLookup{"HUDDLE_ROUTER_HOME": "/tmp/router", "HOME": "/home/u"}
	if got := HomeOf(env); got != "/tmp/router" {
		t.Errorf("HomeOf = %q, want the override", got)
	}
	if got := HomeOf(domain.EnvLookup{"HOME": "/home/u"}); got != "/home/u/.agents/huddle-router" {
		t.Errorf("HomeOf = %q, want the home's own", got)
	}
	// An empty override counts as unset.
	if got := HomeOf(domain.EnvLookup{"HUDDLE_ROUTER_HOME": "", "HOME": "/home/u"}); got != "/home/u/.agents/huddle-router" {
		t.Errorf("HomeOf = %q, want the home's own", got)
	}
}

func TestEnabledFile(t *testing.T) {
	got := EnabledFile(domain.EnvLookup{"HUDDLE_ROUTER_HOME": "/tmp/router"})
	if want := filepath.Join("/tmp/router", "providers.json"); got != want {
		t.Errorf("EnabledFile = %q, want %q", got, want)
	}
}

func writeEnabled(t *testing.T, env domain.EnvLookup, text string) {
	t.Helper()
	if err := os.MkdirAll(HomeOf(env), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(EnabledFile(env), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadEnabled(t *testing.T) {
	t.Run("a missing file is none", func(t *testing.T) {
		env := domain.EnvLookup{"HUDDLE_ROUTER_HOME": t.TempDir()}
		names, err := ReadEnabled(env)
		if err != nil || len(names) != 0 {
			t.Fatalf("ReadEnabled = (%v, %v), want none and no error", names, err)
		}
	})

	t.Run("names come back in the compiled providers' order and unknown ones drop", func(t *testing.T) {
		env := domain.EnvLookup{"HUDDLE_ROUTER_HOME": t.TempDir()}
		writeEnabled(t, env, `{"providers":["qwen","zai","madeup","kimi"]}`)
		names, err := ReadEnabled(env)
		if err != nil {
			t.Fatalf("ReadEnabled: %v", err)
		}
		if want := []string{"zai", "kimi", "qwen"}; !slices.Equal(names, want) {
			t.Errorf("ReadEnabled = %v, want %v", names, want)
		}
	})

	t.Run("every provider enabled in a shuffled file comes back in the providers' order", func(t *testing.T) {
		env := domain.EnvLookup{"HUDDLE_ROUTER_HOME": t.TempDir()}
		writeEnabled(t, env, `{"providers":["qwen","minimax","deepseek","kimi","zai"]}`)
		names, err := ReadEnabled(env)
		if err != nil {
			t.Fatalf("ReadEnabled: %v", err)
		}
		if want := domain.ProviderNames(); !slices.Equal(names, want) {
			t.Errorf("ReadEnabled = %v, want %v", names, want)
		}
	})

	t.Run("an empty list is none", func(t *testing.T) {
		env := domain.EnvLookup{"HUDDLE_ROUTER_HOME": t.TempDir()}
		writeEnabled(t, env, `{"providers":[]}`)
		names, err := ReadEnabled(env)
		if err != nil || len(names) != 0 {
			t.Fatalf("ReadEnabled = (%v, %v), want none and no error", names, err)
		}
	})

	t.Run("a malformed file is an error", func(t *testing.T) {
		env := domain.EnvLookup{"HUDDLE_ROUTER_HOME": t.TempDir()}
		writeEnabled(t, env, `{"providers":`)
		if _, err := ReadEnabled(env); err == nil {
			t.Fatal("ReadEnabled = nil error, want one")
		}
	})

	t.Run("a file of the wrong shape is an error", func(t *testing.T) {
		env := domain.EnvLookup{"HUDDLE_ROUTER_HOME": t.TempDir()}
		writeEnabled(t, env, `{"providers":"zai"}`)
		if _, err := ReadEnabled(env); err == nil {
			t.Fatal("ReadEnabled = nil error, want one")
		}
	})
}

func TestSetEnabled(t *testing.T) {
	t.Run("on records the provider", func(t *testing.T) {
		env := domain.EnvLookup{"HUDDLE_ROUTER_HOME": t.TempDir()}
		if err := SetEnabled(env, "zai", true); err != nil {
			t.Fatalf("SetEnabled: %v", err)
		}
		names, err := ReadEnabled(env)
		if err != nil || !slices.Equal(names, []string{"zai"}) {
			t.Fatalf("ReadEnabled = (%v, %v), want [zai]", names, err)
		}
	})

	t.Run("on twice records it once", func(t *testing.T) {
		env := domain.EnvLookup{"HUDDLE_ROUTER_HOME": t.TempDir()}
		if err := SetEnabled(env, "zai", true); err != nil {
			t.Fatalf("SetEnabled: %v", err)
		}
		if err := SetEnabled(env, "zai", true); err != nil {
			t.Fatalf("second SetEnabled: %v", err)
		}
		names, err := ReadEnabled(env)
		if err != nil || !slices.Equal(names, []string{"zai"}) {
			t.Fatalf("ReadEnabled = (%v, %v), want [zai] once", names, err)
		}
	})

	t.Run("off takes one provider out and keeps the rest", func(t *testing.T) {
		env := domain.EnvLookup{"HUDDLE_ROUTER_HOME": t.TempDir()}
		for _, name := range []string{"zai", "kimi"} {
			if err := SetEnabled(env, name, true); err != nil {
				t.Fatalf("SetEnabled %s: %v", name, err)
			}
		}
		if err := SetEnabled(env, "zai", false); err != nil {
			t.Fatalf("SetEnabled off: %v", err)
		}
		names, err := ReadEnabled(env)
		if err != nil || !slices.Equal(names, []string{"kimi"}) {
			t.Fatalf("ReadEnabled = (%v, %v), want [kimi]", names, err)
		}
	})

	t.Run("off with nothing on changes nothing", func(t *testing.T) {
		env := domain.EnvLookup{"HUDDLE_ROUTER_HOME": t.TempDir()}
		if err := SetEnabled(env, "zai", false); err != nil {
			t.Fatalf("SetEnabled off: %v", err)
		}
		names, err := ReadEnabled(env)
		if err != nil || len(names) != 0 {
			t.Fatalf("ReadEnabled = (%v, %v), want none", names, err)
		}
	})

	t.Run("eight concurrent writers all land", func(t *testing.T) {
		env := domain.EnvLookup{"HUDDLE_ROUTER_HOME": t.TempDir()}
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				if err := SetEnabled(env, concurrencyName(n), true); err != nil {
					t.Errorf("SetEnabled %s: %v", concurrencyName(n), err)
				}
			}(i)
		}
		wg.Wait()
		raw, err := os.ReadFile(EnabledFile(env))
		if err != nil {
			t.Fatalf("read the file: %v", err)
		}
		for i := 0; i < 8; i++ {
			if !strings.Contains(string(raw), concurrencyName(i)) {
				t.Errorf("the file lost %s:\n%s", concurrencyName(i), raw)
			}
		}
	})
}

// concurrencyName is one name per concurrent writer: SetEnabled records any name it is told,
// and ReadEnabled drops the ones no compiled provider owns.
func concurrencyName(n int) string {
	return "writer" + string(rune('a'+n))
}

func TestReadFallback(t *testing.T) {
	t.Run("a lane's fallback list comes back as written", func(t *testing.T) {
		env := domain.EnvLookup{"HUDDLE_ROUTER_HOME": t.TempDir()}
		writeEnabled(t, env, `{"providers":["zai"],"lanes":{"zai":{"fallback":["glm-5.3-flash"]}}}`)
		if got := ReadFallback(env, "zai"); !slices.Equal(got, []string{"glm-5.3-flash"}) {
			t.Errorf("ReadFallback(zai) = %v", got)
		}
		if got := ReadFallback(env, "kimi"); got != nil {
			t.Errorf("ReadFallback(kimi) = %v, want none", got)
		}
	})

	t.Run("a missing or malformed file is no fallback", func(t *testing.T) {
		env := domain.EnvLookup{"HUDDLE_ROUTER_HOME": t.TempDir()}
		if got := ReadFallback(env, "zai"); got != nil {
			t.Errorf("missing: %v", got)
		}
		writeEnabled(t, env, `{"providers":["zai"],"lanes":{"zai":{"fallback":"glm-5.3-flash"}}}`)
		if got := ReadFallback(env, "zai"); got != nil {
			t.Errorf("malformed: %v", got)
		}
	})

	t.Run("enabling a provider keeps every lane's fallback list", func(t *testing.T) {
		env := domain.EnvLookup{"HUDDLE_ROUTER_HOME": t.TempDir()}
		writeEnabled(t, env, `{"providers":["zai"],"lanes":{"zai":{"fallback":["glm-5.3-flash"]}}}`)
		if err := SetEnabled(env, "kimi", true); err != nil {
			t.Fatal(err)
		}
		if got := ReadFallback(env, "zai"); !slices.Equal(got, []string{"glm-5.3-flash"}) {
			t.Errorf("after SetEnabled: ReadFallback(zai) = %v", got)
		}
	})
}
