package routerd

// The daemon's health path answers what its own loops and stores last reported: reload, the
// rss watch, the body sweep and the body store, each with its state. The hub lists the same
// parts as router.<name>.

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/core/router"
)

// awaitComponent polls the daemon's health answer until it names the component in the state,
// and answers the whole answer.
func awaitComponent(t *testing.T, rig *daemonRig, name, state string) HealthAnswer {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if body, ok := rig.health(t); ok {
			var answer HealthAnswer
			if err := json.Unmarshal([]byte(body), &answer); err == nil {
				for _, part := range answer.Components {
					if part.Name == name && part.State == state {
						return answer
					}
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never reported %s on the health path; the log:\n%s", name, state, rig.logs.joined())
	return HealthAnswer{}
}

// componentOf answers the named part of one health answer.
func componentOf(t *testing.T, answer HealthAnswer, name string) HealthPart {
	t.Helper()
	for _, part := range answer.Components {
		if part.Name == name {
			return part
		}
	}
	t.Fatalf("the health answer names no %s: %+v", name, answer.Components)
	return HealthPart{}
}

func TestTheRouterListsItsComponentsOnTheHealthPath(t *testing.T) {
	t.Parallel()

	t.Run("a healthy home reports reload and the body sweep ok after one tick", func(t *testing.T) {
		t.Parallel()
		rig := newDaemonRig(t)
		if err := router.SetEnabled(rig.env, "zai", true); err != nil {
			t.Fatal(err)
		}
		rig.start()
		answer := awaitComponent(t, rig, "reload", "ok")
		if reload := componentOf(t, answer, "reload"); reload.Kind != "loop" || reload.LastOKAt <= 0 || reload.Errors != 0 {
			t.Errorf("reload = %+v, want an ok loop with no errors", reload)
		}
		if sweep := componentOf(t, answer, "body-sweep"); sweep.State != "ok" {
			t.Errorf("body-sweep = %+v, want ok after the start sweep", sweep)
		}
		var names []string
		for _, part := range answer.Components {
			names = append(names, part.Name)
		}
		if !slices.Equal(names, []string{"body-sweep", "reload", "rss"}) {
			t.Errorf("components = %v, want the daemon's three loops sorted", names)
		}
	})

	t.Run("an unreadable providers.json reports reload failing with the error counted", func(t *testing.T) {
		t.Parallel()
		rig := newDaemonRig(t)
		if err := router.SetEnabled(rig.env, "zai", true); err != nil {
			t.Fatal(err)
		}
		rig.start()
		rig.awaitHealth(t, "zai")
		// providers.json becomes a directory, whose read fails on any account, root included.
		enabled := router.EnabledFile(rig.env)
		if err := os.Remove(enabled); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(enabled, 0o700); err != nil {
			t.Fatal(err)
		}
		answer := awaitComponent(t, rig, "reload", "failing")
		if reload := componentOf(t, answer, "reload"); reload.Errors < 1 || reload.LastError == "" {
			t.Errorf("reload = %+v, want the read failure counted and kept", reload)
		}
		// the routerd client answers the same parts from the same path
		got, up := Health(context.Background(), rig.port)
		if !up {
			t.Fatal("Health answered not up while the daemon serves")
		}
		if part := componentOf(t, got, "reload"); part.State != "failing" || part.Errors < 1 {
			t.Errorf("Health's reload = %+v, want the failing part", part)
		}
	})
}
