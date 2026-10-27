package keystore

// BodyKey's rule: the env's HOME decides, never the process environment, and a store that is
// not the login account's is no store at all.

import (
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// TestBodyKeyIsOffForAnotherHome proves the body key's store reads the env it is handed, never
// the process environment: a temp HOME in the env and a HOME missing there both answer nil, so
// an in-process test can never reach the runner's real login keystore. This test runs without
// tmpguard on purpose — the process HOME is the real one exactly when it must not matter.
func TestBodyKeyIsOffForAnotherHome(t *testing.T) {
	t.Parallel()
	if store := BodyKey(domain.EnvLookup{"HOME": t.TempDir()}); store != nil {
		t.Error("BodyKey answered a store for a home that is not the login account's")
	}
	if store := BodyKey(domain.EnvLookup{}); store != nil {
		t.Error("BodyKey answered a store with no HOME in the env")
	}
}
