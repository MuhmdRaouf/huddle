package adapters

import (
	"path/filepath"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// The state-root rule's cases: inputs and expected outputs. The providers are reference
// fixtures; only the names a rule reads are filled in.

var referenceProvider = domain.Provider{Name: "zai", EnvPrefix: "ZAI"}

var acmeProvider = domain.Provider{Name: "acme", EnvPrefix: "ACME"}

// referenceStateRoot is the reference provider's state-root rule.
var referenceStateRoot = StateRootRule{EnvVar: "ZAI_STATE_DIR", XdgName: "zai"}

var acmeStateRoot = StateRootRule{EnvVar: "ACME_STATE_DIR", XdgName: "acme"}

func TestResolveStateRoot(t *testing.T) {
	t.Run("zai: its env var, else ~/.agents/zai, with or without CLAUDE_PLUGIN_DATA", func(t *testing.T) {
		assertStateRoots(t, referenceStateRoot, "ZAI_STATE_DIR", "/data/zai-plugin-cc-m", "zai")
	})

	t.Run("a second provider: its env var, else ~/.agents/<name>, with or without CLAUDE_PLUGIN_DATA", func(t *testing.T) {
		assertStateRoots(t, acmeStateRoot, "ACME_STATE_DIR", "/data/acme-plugin-m", "acme")
	})

	t.Run("never reads another provider's env var or data dir", func(t *testing.T) {
		zaiOwn := domain.EnvLookup{
			"ZAI_STATE_DIR":      "/zai",
			"CLAUDE_PLUGIN_DATA": "/data/zai-plugin-cc-m",
			"HOME":               "/h",
		}
		acmeOwn := domain.EnvLookup{
			"ACME_STATE_DIR":     "/acme",
			"CLAUDE_PLUGIN_DATA": "/data/acme-plugin-m",
			"HOME":               "/h",
		}

		if got := ResolveStateRoot(t.Context(), zaiOwn, acmeStateRoot); got != filepath.Join("/h", ".agents", "acme") {
			t.Fatalf("resolveStateRoot(zai's world, acme rule) = %q, want %q", got, filepath.Join("/h", ".agents", "acme"))
		}
		if got := ResolveStateRoot(t.Context(), acmeOwn, referenceStateRoot); got != filepath.Join("/h", ".agents", "zai") {
			t.Fatalf("resolveStateRoot(acme's world, reference rule) = %q, want %q", got, filepath.Join("/h", ".agents", "zai"))
		}
	})

	t.Run("stateRootRule derives each provider's rule from its own names", func(t *testing.T) {
		if got := NewStateRootRule(referenceProvider); got != referenceStateRoot {
			t.Fatalf("stateRootRule(reference) = %+v, want %+v", got, referenceStateRoot)
		}
		want := StateRootRule{EnvVar: "ACME_STATE_DIR", XdgName: "acme"}
		if got := NewStateRootRule(acmeProvider); got != want {
			t.Fatalf("stateRootRule(acme) = %+v, want %+v", got, want)
		}
	})
}

// assertStateRoots is the shared body of the it.each rows: the env var wins, then ~/.agents —
// CLAUDE_PLUGIN_DATA never picks the root, because Claude Code sets it only for hooks and the
// hooks must find the setup-done and pid files setup and the router wrote, and XDG_STATE_HOME
// does not pick the root either.
func assertStateRoots(t *testing.T, rule StateRootRule, envVar, own, name string) {
	t.Helper()
	agents := filepath.Join("/h", ".agents", name)
	cases := []struct {
		env  domain.EnvLookup
		want string
	}{
		{
			domain.EnvLookup{envVar: "/explicit", "CLAUDE_PLUGIN_DATA": own, "HOME": "/h"},
			"/explicit",
		},
		{
			domain.EnvLookup{"CLAUDE_PLUGIN_DATA": own, "XDG_STATE_HOME": "/xdg", "HOME": "/h"},
			agents,
		},
		{
			domain.EnvLookup{"XDG_STATE_HOME": "/xdg", "HOME": "/h"},
			agents,
		},
		{
			domain.EnvLookup{"HOME": "/h"},
			agents,
		},
	}
	for _, tc := range cases {
		if got := ResolveStateRoot(t.Context(), tc.env, rule); got != tc.want {
			t.Fatalf("resolveStateRoot(%v) = %q, want %q", tc.env, got, tc.want)
		}
	}
}

func TestStateRoot(t *testing.T) {
	t.Run("derives the rule and resolves the root for a whole provider", func(t *testing.T) {
		if got := StateRoot(t.Context(), referenceProvider, domain.EnvLookup{"ZAI_STATE_DIR": "/explicit"}); got != "/explicit" {
			t.Fatalf("stateRoot(reference) = %q, want %q", got, "/explicit")
		}
		if got := StateRoot(t.Context(), acmeProvider, domain.EnvLookup{"HOME": "/h"}); got != filepath.Join("/h", ".agents", "acme") {
			t.Fatalf("stateRoot(acme) = %q, want %q", got, filepath.Join("/h", ".agents", "acme"))
		}
	})

	t.Run("a CLAUDE_PLUGIN_DATA value leaves the root at ~/.agents", func(t *testing.T) {
		env := domain.EnvLookup{"CLAUDE_PLUGIN_DATA": "zai-plugin-m", "HOME": "/h"}
		if got := ResolveStateRoot(t.Context(), env, referenceStateRoot); got != filepath.Join("/h", ".agents", "zai") {
			t.Fatalf("resolveStateRoot(own data dir) = %q, want %q", got, filepath.Join("/h", ".agents", "zai"))
		}
	})
}
