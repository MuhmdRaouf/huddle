package routerd

// openBodies is the daemon's bodies switch: a keystore becomes the tap and its store, and
// anything short of one stays off with a single line that never carries the key.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
)

// bodiesKey is a KeySource over one in-memory value; a refuse makes every Set fail after the
// drawn value is recorded, so the log can be checked against what the key was.
type bodiesKey struct {
	value  string
	stored bool
	refuse error
	drawn  []string
}

func (k *bodiesKey) Get(ctx context.Context) (string, bool) {
	return k.value, k.stored
}

func (k *bodiesKey) Set(ctx context.Context, value string) error {
	k.drawn = append(k.drawn, value)
	if k.refuse != nil {
		return k.refuse
	}
	k.value, k.stored = value, true
	return nil
}

// TestPlatformBodyKeyIsOffForAnotherHome proves the key source reads the daemon's env, never
// the process environment: a temp HOME in Options.Env and a HOME missing there both keep
// bodies off, so an in-process test can never reach the runner's real login keystore. This
// test runs without tmpguard on purpose — the process HOME is the real one exactly when it
// must not matter.
func TestPlatformBodyKeyIsOffForAnotherHome(t *testing.T) {
	t.Parallel()
	if source := platformBodyKey(domain.EnvLookup{"HOME": t.TempDir()}); source != nil {
		t.Error("platformBodyKey answered a key source for a home that is not the login account's")
	}
	if source := platformBodyKey(domain.EnvLookup{}); source != nil {
		t.Error("platformBodyKey answered a key source with no HOME in the daemon's env")
	}
}

func TestOpenBodiesWiresTheTapToTheStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := domain.EnvLookup{"RADAR_HOME": t.TempDir()}
	logs := &daemonLog{}

	tap, store := openBodies(ctx, env, &bodiesKey{}, logs.add)
	if tap == nil {
		t.Fatal("openBodies answered no tap with a keystore to draw from")
	}
	if store == nil {
		t.Fatal("openBodies answered no store with a keystore to draw from")
	}
	info, err := os.Stat(paths.RouterBodiesPath(ctx, env))
	if err != nil {
		t.Fatalf("stat router.db: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("router.db is mode %o, want 600", info.Mode().Perm())
	}

	// One body through the tap lands in the store once the tap closes.
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"keep this body whole"}]}`)
	if !tap.Offer("bodies-unit", "input", body, time.Now()) {
		t.Fatal("the tap refused the body")
	}
	budget, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := tap.Close(budget); err != nil {
		t.Fatalf("close the tap: %v", err)
	}
	got, ok, err := store.Body(ctx, "bodies-unit", "input")
	if err != nil {
		t.Fatalf("read the body back: %v", err)
	}
	if !ok {
		t.Fatal("the offered body is not in the store")
	}
	if !bytes.Equal(got, body) {
		t.Errorf("the body reads back %d bytes, want the %d offered", len(got), len(body))
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close the store: %v", err)
	}
}

func TestOpenBodiesWithoutAKeystoreStaysOff(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := domain.EnvLookup{"RADAR_HOME": t.TempDir()}
	logs := &daemonLog{}

	tap, store := openBodies(ctx, env, nil, logs.add)
	if tap != nil {
		t.Error("openBodies answered a tap with no keystore")
	}
	if store != nil {
		t.Error("openBodies answered a store with no keystore")
	}
	if got := logs.joined(); !strings.Contains(got, "bodies off: no keystore") {
		t.Errorf("the log says %q, want the no-keystore line", got)
	}
}

func TestOpenBodiesRefusedKeyNeverNamesIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := domain.EnvLookup{"RADAR_HOME": t.TempDir()}
	logs := &daemonLog{}
	key := &bodiesKey{refuse: errors.New("the keystore refused")}

	tap, store := openBodies(ctx, env, key, logs.add)
	if tap != nil {
		t.Error("openBodies answered a tap with a keystore that refuses")
	}
	if store != nil {
		t.Error("openBodies answered a store with a keystore that refuses")
	}
	joined := logs.joined()
	if !strings.Contains(joined, "bodies off: ") {
		t.Errorf("the log says %q, want a bodies-off line", joined)
	}
	for _, drawn := range key.drawn {
		if strings.Contains(joined, drawn) {
			t.Error("the log carries the drawn key")
		}
	}
}
