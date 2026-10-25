// Which providers huddle-router serves: one file per user, re-read every poll, so enabling a
// provider needs no restart. The names are recorded as they are given and dropped when no
// compiled provider owns them, so the file can never turn a typo into a lane.
package router

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters"
	"github.com/MuhmdRaouf/huddle/internal/adapters/fsutil"
	"github.com/MuhmdRaouf/huddle/internal/adapters/singleton"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// HomeOf is huddle-router's state dir: $HUDDLE_ROUTER_HOME, else $HOME/.agents/huddle-router.
func HomeOf(env domain.EnvLookup) string {
	if home := env["HUDDLE_ROUTER_HOME"]; home != "" {
		return home
	}
	return adapters.AgentsStateDir(env, "huddle-router")
}

// PortOf is the port the router serves: $HUDDLE_ROUTER_PORT, else 18786. The words, the daemon
// and the gateway all take it from here, so one variable moves every edition off the canonical
// port.
func PortOf(env domain.EnvLookup) (int, error) {
	raw := env["HUDDLE_ROUTER_PORT"]
	if raw == "" {
		return domain.HuddleRouterPort, nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("huddle-router: $HUDDLE_ROUTER_PORT (%q) is not a port", raw)
	}
	return port, nil
}

// EnabledFile is HomeOf/providers.json:
// {"providers":["zai","kimi"],"lanes":{"zai":{"fallback":["glm-5.3-flash"]}}}.
func EnabledFile(env domain.EnvLookup) string {
	return filepath.Join(HomeOf(env), "providers.json")
}

// enabled is the file's shape.
type enabled struct {
	Providers []string `json:"providers"`
	// Lanes is each provider lane's own settings, by provider name.
	Lanes map[string]laneSettings `json:"lanes,omitempty"`
}

// laneSettings is one lane's settings in providers.json.
type laneSettings struct {
	// Fallback is the models a failing model's requests go to, in order.
	Fallback []string `json:"fallback,omitempty"`
}

// ReadFallback is the provider lane's fallback list in providers.json; nil when the file, the
// lane or the list is missing or does not parse (ReadEnabled reports a bad file).
func ReadFallback(env domain.EnvLookup, name string) []string {
	raw, err := os.ReadFile(EnabledFile(env))
	if err != nil {
		return nil
	}
	var parsed enabled
	if json.Unmarshal(raw, &parsed) != nil {
		return nil
	}
	return parsed.Lanes[name].Fallback
}

// ReadEnabled answers the enabled providers in the compiled providers' order. A missing file is
// none; unknown names are dropped; an unreadable or malformed file is an error.
func ReadEnabled(env domain.EnvLookup) ([]string, error) {
	file := EnabledFile(env)
	raw, err := os.ReadFile(file)
	if err != nil {
		if fsutil.ErrnoCode(err) == "ENOENT" {
			return nil, nil
		}
		return nil, fmt.Errorf("%s: %s", file, fsutil.ErrorMessage(err))
	}
	var parsed enabled
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("%s: not valid providers.json (%s)", file, err)
	}
	// The compiled providers' order is the router ports' order.
	byPort := make([]int64, 0, len(domain.PluginRouters))
	for port := range domain.PluginRouters {
		byPort = append(byPort, port)
	}
	slices.Sort(byPort)
	var names []string
	for _, port := range byPort {
		name := domain.PluginRouters[port]
		if slices.Contains(parsed.Providers, name) {
			names = append(names, name)
		}
	}
	return names, nil
}

// SetEnabled records a provider on or off, atomically, under the file's own flock — taken on a
// file beside providers.json, because the write below replaces providers.json's inode and a
// flock would not survive the rename. The wait is bounded: the critical section is a
// read-modify-write of a file of a few names.
func SetEnabled(env domain.EnvLookup, name string, on bool) error {
	file := EnabledFile(env)
	release, err := singleton.Wait(file+".lock", "huddle-router", 5*time.Second)
	if err != nil {
		return err
	}
	defer release()
	var current enabled
	if raw, err := os.ReadFile(file); err == nil {
		if err := json.Unmarshal(raw, &current); err != nil {
			return fmt.Errorf("%s: not valid providers.json (%s)", file, err)
		}
	}
	has := slices.Contains(current.Providers, name)
	switch {
	case on == has:
		return nil
	case on:
		current.Providers = append(current.Providers, name)
	default:
		kept := make([]string, 0, len(current.Providers))
		for _, keptName := range current.Providers {
			if keptName != name {
				kept = append(kept, keptName)
			}
		}
		current.Providers = kept
	}
	text, err := json.Marshal(current)
	if err != nil {
		return err
	}
	_, err = fsutil.WriteFileAtomic(context.Background(), file, text, fsutil.WriteOptions{Mkdir: true})
	return err
}
