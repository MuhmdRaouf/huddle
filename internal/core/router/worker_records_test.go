package router

// The worker's small records and tools: the registry entry a hub reads, the installed plugin's
// path, the disk operations the rotating log stands on, and the key cache's forget.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/core/ports"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

func TestRegistryEntryForCarriesTheClaimAndThePid(t *testing.T) {
	t.Parallel()
	pid := int64(4242)
	entry := RegistryEntryFor(routerProvider, domain.EnvLookup{}, &pid)
	if entry.Name != "zai" || entry.Port != int64(routerProvider.Router.Port) {
		t.Errorf("RegistryEntryFor = %+v, want zai on its own port", entry)
	}
	if len(entry.ModelPrefixes) == 0 || len(entry.CatalogIds) == 0 {
		t.Errorf("RegistryEntryFor = %+v, want the claim's prefixes and ids", entry)
	}
	if entry.Pid == nil || *entry.Pid != 4242 {
		t.Errorf("RegistryEntryFor pid = %v, want 4242", entry.Pid)
	}
	if entry.UpdatedAt == "" {
		t.Error("RegistryEntryFor carried no timestamp")
	}
	anonymous := RegistryEntryFor(routerProvider, domain.EnvLookup{}, nil)
	if anonymous.Pid != nil {
		t.Errorf("RegistryEntryFor pid = %v, want none", anonymous.Pid)
	}
}

func TestDiskFsReadsAndWritesWhatIsThere(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "router.log")
	if err := DiskFs.Write(path, []byte("first")); err != nil {
		t.Fatalf("DiskFs.Write: %v", err)
	}
	raw, err := DiskFs.Read(path)
	if err != nil || string(raw) != "first" {
		t.Errorf("DiskFs.Read = %q, %v, want first", raw, err)
	}
	if err := DiskFs.Write(path, []byte("second")); err != nil {
		t.Fatalf("DiskFs.Write over the file: %v", err)
	}
	if raw, err := DiskFs.Read(path); err != nil || string(raw) != "second" {
		t.Errorf("DiskFs.Read = %q, %v, want second", raw, err)
	}
	if _, err := DiskFs.Read(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("DiskFs.Read of an absent file = nil error, want one")
	}
	if _, err := DiskFs.Size(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("DiskFs.Size of an absent file = nil error, want one")
	}
}

func TestKeyCacheClearForgetsTheKeptKey(t *testing.T) {
	t.Parallel()
	calls := 0
	worker := newRouterWorker(RouterOptions{Provider: routerProvider}, func(context.Context) (ports.ProviderKey, *ports.WorkerError) {
		calls++
		return ports.ProviderKey{Value: "kept"}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, outcome := keyWithin(ctx, worker); outcome != keyValue {
		t.Fatalf("keyWithin = %v, want the key", outcome)
	}
	if worker.keys.get(ctx).key.Value != "kept" {
		t.Fatal("the cache does not keep the key")
	}
	// the provider refused the key: the kept answer goes, and the next read looks again
	worker.keys.clear()
	if got := worker.keys.get(ctx).key.Value; got != "kept" {
		t.Fatalf("the cache kept nothing after the clear, want the fresh lookup's key, got %q", got)
	}
	if calls < 2 {
		t.Errorf("the lookup ran %d times, want it again after the clear", calls)
	}
}

func TestPluginInstallPathReadsTheInstalledPlugin(t *testing.T) {
	t.Parallel()
	configDir := t.TempDir()
	installed := `{
	  "plugins": {
	    "zai-plugin-cc@muhmdraouf": [ { "installPath": "/users/me/.claude/plugins/cache/zai" } ]
	  }
	}`
	read := func(path string) (string, bool) {
		if path == filepath.Join(configDir, "plugins", "installed_plugins.json") {
			return installed, true
		}
		return "", false
	}
	if got := PluginInstallPath("zai", configDir, read); got != "/users/me/.claude/plugins/cache/zai" {
		t.Errorf("PluginInstallPath = %q, want the cache path", got)
	}
	if got := PluginInstallPath("zai", configDir, func(string) (string, bool) { return "", false }); got != "" {
		t.Errorf("PluginInstallPath = %q, want empty without the file", got)
	}
	if got := PluginInstallPath("zai", configDir, func(string) (string, bool) { return "not json", true }); got != "" {
		t.Errorf("PluginInstallPath = %q, want empty for prose", got)
	}
	if got := PluginInstallPath("zai", configDir, nil); got != "" {
		t.Errorf("PluginInstallPath with no reader = %q, want empty without the file", got)
	}
}

func TestRotatingLogKeepsTheLastFileOnADisk(t *testing.T) {
	t.Parallel()
	logs := filepath.Join(t.TempDir(), "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(logs, "router.log")
	log := RotatingLog(path, nil, 512)
	for i := 0; i < 40; i++ {
		log("line of the router's own log, long enough to fill the little limit fast")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the log: %v", err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("the rotated log: %v", err)
	}
}
