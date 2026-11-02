package main

// A hub or hook a test starts from a hand-built environment map never reaches the owner's
// desktop: both renderers of those maps (argvEntries and envList) name a notification log under
// the map's HOME and put the run's notifier shims first on its PATH.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
)

func TestEveryHandBuiltEnvironmentNamesANotifyLogAndTheShims(t *testing.T) {
	shims := t.TempDir()
	t.Setenv(isoenv.ShimsEnv, shims)
	user := t.TempDir()
	maps := map[string]map[string]string{
		"argvBase":   argvBase(t),
		"aloneEnv":   aloneEnv(t),
		"machineEnv": machineEnv(t.TempDir(), user, t.TempDir(), t.TempDir(), t.TempDir(), "http://127.0.0.1:1", nil),
		"home only":  {"HOME": user, "PATH": strippedPath},
	}
	renderers := map[string]func(map[string]string) []string{"argvEntries": argvEntries, "envList": envList}
	for mapName, env := range maps {
		for renderName, render := range renderers {
			got := isoenv.Lookup(render(env))
			if want := filepath.Join(env["HOME"], "notify.log"); got["HUDDLE_NOTIFY_LOG"] != want {
				t.Errorf("%s through %s: HUDDLE_NOTIFY_LOG = %q, want %q", mapName, renderName, got["HUDDLE_NOTIFY_LOG"], want)
			}
			if prefix := filepath.Join(shims, "bin") + string(os.PathListSeparator); !strings.HasPrefix(got["PATH"], prefix) {
				t.Errorf("%s through %s: PATH = %q, want the shims first (%q)", mapName, renderName, got["PATH"], prefix)
			}
		}
	}
}

func TestAHandBuiltEnvironmentKeepsALogItNames(t *testing.T) {
	own := filepath.Join(t.TempDir(), "own.log")
	got := isoenv.Lookup(argvEntries(map[string]string{"HOME": t.TempDir(), "HUDDLE_NOTIFY_LOG": own}))
	if got["HUDDLE_NOTIFY_LOG"] != own {
		t.Errorf("HUDDLE_NOTIFY_LOG = %q, want the map's own %q", got["HUDDLE_NOTIFY_LOG"], own)
	}
}
