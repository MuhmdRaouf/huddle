package hooks

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/alerts"
)

// writeInstalledPlugins writes Claude Code's installed_plugins.json under home/.claude naming
// huddle's user-scope install at commit sha.
func writeInstalledPlugins(t *testing.T, home, sha string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "plugins")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"version":2,"plugins":{"huddle@muhmdraouf":[{"scope":"user","version":"` + sha[:12] +
		`","gitCommitSha":"` + sha + `"}]}}`
	if err := os.WriteFile(filepath.Join(dir, "installed_plugins.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// pointCurrentAt repoints the launcher's current link under home/.agents/huddle-bin at
// src/<commit>/huddle, the way a source build does.
func pointCurrentAt(t *testing.T, home, commit string) {
	t.Helper()
	launcher := filepath.Join(home, ".agents", "huddle-bin")
	if err := os.MkdirAll(filepath.Join(launcher, "src", commit), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(launcher, "current")
	_ = os.Remove(link)
	if err := os.Symlink(filepath.Join("src", commit, "huddle"), link); err != nil {
		t.Fatal(err)
	}
}

// TestSessionStartReportsPluginSkewToRadar: a session start whose plugin cache holds another
// huddle commit than the binary leaves radar the fact behind one plugin_behind alert; the next
// start that finds them equal clears it; a machine with no plugin install leaves nothing. The
// hook says nothing either way, and the plugin cache stays as it was.
func TestSessionStartReportsPluginSkewToRadar(t *testing.T) {
	home, radarHome := t.TempDir(), t.TempDir()
	env := map[string]string{"HOME": home, "RADAR_HOME": radarHome, "HUDDLE_CHANNEL": "", "HUDDLE_AS": ""}
	radarEnv := domain.EnvLookup{"RADAR_HOME": radarHome, "HOME": home}
	start := func() {
		t.Helper()
		if stdout, _, _ := runHook(t, "session-start", `{"source":"startup"}`, env); stdout != "" {
			t.Errorf("session-start printed %q, want silence", stdout)
		}
	}

	pointCurrentAt(t, home, "38e9e40e321e")
	start()
	if skew := alerts.ReadPluginSkew(context.Background(), radarEnv); skew != nil {
		t.Fatalf("no plugin install: skew = %+v, want none", skew)
	}

	writeInstalledPlugins(t, home, "052c8e49c9ff0123456789abcdef0123456789ab")
	cachePath := filepath.Join(home, ".claude", "plugins", "installed_plugins.json")
	before, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	start()
	skew := alerts.ReadPluginSkew(context.Background(), radarEnv)
	if skew == nil || skew.Cache != "052c8e49c9ff" || skew.Binary != "38e9e40e321e" {
		t.Fatalf("skewed start: skew = %+v, want cache 052c8e49c9ff and binary 38e9e40e321e", skew)
	}
	if after, err := os.ReadFile(cachePath); err != nil || string(after) != string(before) {
		t.Errorf("the plugin cache changed: %v %q", err, after)
	}

	pointCurrentAt(t, home, "052c8e49c9ff")
	start()
	if skew := alerts.ReadPluginSkew(context.Background(), radarEnv); skew != nil {
		t.Errorf("equal start: skew = %+v, want none", skew)
	}
}
