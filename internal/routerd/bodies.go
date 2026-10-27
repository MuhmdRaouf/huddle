package routerd

// The daemon's bodies: the body key lives in the login keystore, the bodies live in radar's
// ledger under RADAR_HOME, and anything short of a keystore keeps the router serving with
// bodies off. One line says so; the router never fails over an extra.

import (
	"context"
	crand "crypto/rand"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/keystore"
	"github.com/MuhmdRaouf/huddle/internal/core/ports"
	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/ledger"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
)

// tapCloseBudget is how long a shutdown lets the tap write its queue before the store closes.
const tapCloseBudget = 5 * time.Second

// keystoreKey draws and keeps the body key through one keystore.
type keystoreKey struct {
	store ports.KeyStore
}

func (k keystoreKey) Get(ctx context.Context) (string, bool) {
	return k.store.Get(ctx)
}

func (k keystoreKey) Set(ctx context.Context, value string) error {
	return k.store.Set(ctx, value)
}

// platformBodyKey is the body key source of this machine: the login account's keystore, or nil
// where there is none — a home that is not the login account's (tests, agents) keeps bodies
// off, the same rule the push key follows. The rule is keystore.BodyKey's, shared with the hub;
// the source reads the daemon's env, never the process environment: an in-process test's temp
// HOME in Options.Env decides, so the runner's real login keystore is out of reach.
func platformBodyKey(env domain.EnvLookup) ledger.KeySource {
	if store := keystore.BodyKey(env); store != nil {
		return keystoreKey{store}
	}
	return nil
}

// openBodies opens the body store under RADAR_HOME and answers the tap that feeds it. It
// answers nil, nil and logs one line when bodies stay off: no keystore, a key the keystore
// refuses (that error never carries the key), or a store that does not open.
func openBodies(ctx context.Context, env domain.EnvLookup, source ledger.KeySource, log func(string)) (*router.LedgerTap, *ledger.BodyStore) {
	if source == nil {
		log("huddle-router: bodies off: no keystore")
		return nil, nil
	}
	key, err := ledger.LoadBodyKey(ctx, source, crand.Reader)
	if err != nil {
		log("huddle-router: bodies off: " + err.Error())
		return nil, nil
	}
	store, err := ledger.OpenBodies(paths.RouterBodiesPath(ctx, env), key)
	if err != nil {
		log("huddle-router: bodies off: " + err.Error())
		return nil, nil
	}
	return router.NewLedgerTap(store, 0, log), store
}

// bodies is the tap and its store while bodies are on; the nil bodies closes nothing.
type bodies struct {
	tap   *router.LedgerTap
	store *ledger.BodyStore
}

// close writes the tap's queue into the store within the tap's budget, then closes the store.
func (b *bodies) close(log func(line string)) {
	if b == nil {
		return
	}
	if b.tap != nil {
		budget, cancel := context.WithTimeout(context.Background(), tapCloseBudget)
		defer cancel()
		if err := b.tap.Close(budget); err != nil {
			log("huddle-router: bodies tap: " + err.Error())
		}
	}
	if b.store != nil {
		if err := b.store.Close(); err != nil {
			log("huddle-router: bodies store: " + err.Error())
		}
	}
}

// sweep prunes the bodies past their days; a failed sweep is one line and the router serves on.
func (b *bodies) sweep(ctx context.Context, log func(line string)) {
	if b == nil || b.store == nil {
		return
	}
	if _, err := b.store.Sweep(ctx, time.Now()); err != nil {
		log("huddle-router: bodies sweep: " + err.Error())
	}
}
