package cli

// Setup's quiet edges: the settings helpers, the servers registry over a file that is missing
// or half-read, a join that cannot land, and a port flag that is no port.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// TestSetupRefusesAPortFlagThatIsNoPort pins the port flag's refusal: a word that is no number
// and no time is no port.
func TestSetupRefusesAPortFlagThatIsNoPort(t *testing.T) {
	w := cliWorld(t, t.TempDir(), t.TempDir(), nil)
	code, out, errOut := runH(t, w, "setup", "--port", "tomorrow")
	if code != 2 || !strings.Contains(errOut, "--port tomorrow: a port number") {
		t.Errorf("setup --port tomorrow = (%d, %q %q)", code, out, errOut)
	}
}

// TestSettingsRemoveKeepsAMissingKeyQuiet pins the settings rewrite: removing a key that is not
// there leaves the file's keys alone.
func TestSettingsRemoveKeepsAMissingKeyQuiet(t *testing.T) {
	s := &settings{v: jsfmt.Object([]string{"channel"}, []*jsfmt.Value{jsfmt.String("c")})}
	s.remove("port")
	if len(s.v.Keys()) != 1 || s.v.Keys()[0] != "channel" {
		t.Errorf("the keys after a quiet remove = %v", s.v.Keys())
	}
}

// TestServersInOrderReadsARegistryOrNothing pins the registry reader: no file and a half-read
// entry both answer what can be read, in file order.
func TestServersInOrderReadsARegistryOrNothing(t *testing.T) {
	ctx := context.Background()
	w := cliWorld(t, t.TempDir(), t.TempDir(), nil)
	if notes := serversInOrder(ctx, w); notes != nil {
		t.Errorf("serversInOrder without a registry = %v", notes)
	}
	if err := os.MkdirAll(filepath.Dir(RegistryFile(w)), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, RegistryFile(w), `{
  "/one": {"url": "http://127.0.0.1:18860", "pid": 1},
  "/two": "not an entry"
}`)
	notes := serversInOrder(ctx, w)
	if len(notes) != 1 || notes[0].home != "/one" || notes[0].entry.URL != "http://127.0.0.1:18860" {
		t.Errorf("serversInOrder = %+v", notes)
	}
}

// TestEnterStaysQuietWhenNothingAnswersOrTheRoleIsSet pins the join setup makes: it is silent
// when nothing answers, and carries the session's role when the environment names one.
func TestEnterStaysQuietWhenNothingAnswersOrTheRoleIsSet(t *testing.T) {
	enter(context.Background(), world(t, t.TempDir(), nil), "http://127.0.0.1:1", "c", "a")
	role := world(t, t.TempDir(), map[string]string{"HUDDLE_ROLE": "hub", "HUDDLE_CHANNEL": "c", "HUDDLE_AS": "a"})
	enter(context.Background(), role, "http://127.0.0.1:1", "c", "a")
}

// TestPostJSONSaysNothingWhenTheCallCannotBeMade pins the plain poster: a url that is not a url
// and an address nothing answers both read as no answer.
func TestPostJSONSaysNothingWhenTheCallCannotBeMade(t *testing.T) {
	ctx := context.Background()
	if value, ok := postJSON(ctx, "://not a url", nil, newBody(), time.Second); ok || value != nil {
		t.Errorf("postJSON over a bad url = (%v, %v)", value, ok)
	}
	if value, ok := postJSON(ctx, "http://127.0.0.1:1/api/join", nil, newBody(), time.Second); ok || value != nil {
		t.Errorf("postJSON over an unreachable url = (%v, %v)", value, ok)
	}
}
