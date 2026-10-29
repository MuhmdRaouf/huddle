package radartab

// The tab reports Radar's two stores and its three loops into the hub's health registry: a
// mount over a good radar home answers both stores ok with the loops listed, and one whose
// radar home will not open answers both stores failing, so Radar never goes dark again.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/health"
)

// TestRadarsStoresReportIntoTheHubsHealth: a mount handed the hub's registry answers its
// stores' state and its three loops' names, and a radar home that will not open counts as
// failing, so /health shows a Radar running without its stores.
func TestRadarsStoresReportIntoTheHubsHealth(t *testing.T) {
	t.Run("a good radar home answers both stores ok and the three loops", func(t *testing.T) {
		env := tempEnv(t)
		registry := health.New(time.Now)
		tab, _, _ := mountMux(t, env, MountOptions{
			IntervalMs:     quietMs,
			InsightsTickMs: quietMs,
			Health:         registry,
		})
		if !tab.Owns {
			t.Fatal("the mount did not take the lock")
		}
		parts := partsOf(registry)
		for _, name := range []string{"history.db", "ledger.db"} {
			part, ok := parts[name]
			if !ok {
				t.Fatalf("the snapshot holds no %s: %+v", name, registry.Snapshot())
			}
			if part.Kind != string(domain.HealthStore) || part.State != string(domain.HealthOK) {
				t.Errorf("%s = %s/%s, want store/ok", name, part.Kind, part.State)
			}
		}
		for _, name := range []string{"radar.history-prune", "radar.history-writer", "radar.ledger-prune"} {
			part, ok := parts[name]
			if !ok {
				t.Fatalf("the snapshot holds no %s: %+v", name, registry.Snapshot())
			}
			if part.Kind != string(domain.HealthLoop) {
				t.Errorf("%s = kind %s, want loop", name, part.Kind)
			}
		}
	})

	t.Run("a radar home that will not open answers both stores failing", func(t *testing.T) {
		env := tempEnv(t)
		blocker := filepath.Join(filepath.Dir(env["RADAR_HOME"]), "radar-home-file")
		if err := os.WriteFile(blocker, []byte("a regular file, not a radar home"), 0o600); err != nil {
			t.Fatalf("write the blocker: %v", err)
		}
		env["RADAR_HOME"] = blocker
		registry := health.New(time.Now)
		tab, _, _ := mountMux(t, env, MountOptions{
			IntervalMs:     quietMs,
			InsightsTickMs: quietMs,
			Health:         registry,
		})
		if !tab.Owns {
			t.Fatal("the mount did not take the lock")
		}
		parts := partsOf(registry)
		for _, name := range []string{"history.db", "ledger.db"} {
			part, ok := parts[name]
			if !ok {
				t.Fatalf("the snapshot holds no %s: %+v", name, registry.Snapshot())
			}
			if part.State != string(domain.HealthFailing) {
				t.Errorf("%s = %s, want failing", name, part.State)
			}
		}
		if registry.Healthy() {
			t.Errorf("healthy = true, want false with both stores failing")
		}
	})
}

// partsOf answers one snapshot by component name.
func partsOf(registry *health.Registry) map[string]health.Part {
	parts := map[string]health.Part{}
	for _, part := range registry.Snapshot() {
		parts[part.Name] = part
	}
	return parts
}
