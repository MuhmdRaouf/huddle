package outside

// The keystore runner the real store spawns, the refusals before any tool runs, and the
// platforms' ask-and-read shapes — all through fakes, so no keystore is ever touched.

import (
	"context"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/adapters/keystore"
)

func TestOSSecretsWrapsTheRunnerItIsGiven(t *testing.T) {
	code := 0
	fake := func(context.Context, string, []string, *string) keystore.RunResult {
		return keystore.RunResult{Code: &code}
	}
	store := OSSecrets(fake)
	if store == nil || store.run == nil {
		t.Fatal("OSSecrets answered nothing to run")
	}
	// an unwrapped platform is asked in its own way and answers its own truth
	store.platform = "linux"
	if !store.Available(context.Background()) {
		t.Error("Available on a linux store whose probe ran = false, want true")
	}
}

func TestAKeystoreWithoutARunnerOrAPlatformAnswersNo(t *testing.T) {
	store := &osKeyStore{}
	if store.Available(context.Background()) {
		t.Error("Available without a runner = true, want false")
	}
	store.platform = "plan9"
	if store.Available(context.Background()) {
		t.Error("Available on a platform with no keystore = true, want false")
	}
}

func TestTheLinuxStoreLooksUpAndStoresThroughSecretTool(t *testing.T) {
	ctx := context.Background()
	var asked []string
	var stdin *string
	fake := func(_ context.Context, name string, args []string, in *string) keystore.RunResult {
		asked = append(asked, name+" "+strings.Join(args, " "))
		stdin = in
		return ranRes(0, "the-secret\n")
	}
	store := &osKeyStore{run: fake, platform: "linux"}
	got, ok := store.Get(ctx, "acct:one")
	if !ok || got != "the-secret" {
		t.Fatalf("Get = (%q, %v), want the secret without its break", got, ok)
	}
	if !strings.Contains(asked[len(asked)-1], "lookup service "+SecretsService+" account acct:one") {
		t.Errorf("the lookup was %q", asked[len(asked)-1])
	}
	if err := store.Set(ctx, "acct:one", "v2"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	last := asked[len(asked)-1]
	if !strings.Contains(last, "store --label=Huddle acct:one") || !strings.Contains(last, "account acct:one") {
		t.Errorf("the store was %q", last)
	}
	if stdin == nil || *stdin != "v2" {
		t.Errorf("the secret travelled as %v, want it on the tool's stdin", stdin)
	}
	// a tool that refuses keeps the secret's account in the error, never the secret
	refusing := func(context.Context, string, []string, *string) keystore.RunResult {
		return ranRes(1, "")
	}
	locked := &osKeyStore{run: refusing, platform: "linux"}
	if err := locked.Set(ctx, "acct:one", "sekret"); err == nil {
		t.Error("Set on a refusing store answered nil, want an error")
	} else if strings.Contains(err.Error(), "sekret") {
		t.Errorf("the error carries the secret: %v", err)
	}
	if _, ok := locked.Get(ctx, "acct:one"); ok {
		t.Error("Get on a refusing store answered ok")
	}
	// a store of no platform refuses with its name
	plan9 := &osKeyStore{run: fake, platform: "plan9"}
	if err := plan9.Set(ctx, "acct:one", "v3"); err == nil || !strings.Contains(err.Error(), "plan9") {
		t.Errorf("Set on plan9 = %v, want an error naming the platform", err)
	}
	if _, ok := plan9.Get(ctx, "acct:one"); ok {
		t.Error("Get on plan9 answered ok")
	}
}

// TestTheHostStoreIsAskedItsOwnWay pins the platform the empty field picks: the host's own way
// runs through the fake, and a set that the read-back cannot prove is refused.
func TestTheHostStoreIsAskedItsOwnWay(t *testing.T) {
	ctx := context.Background()
	fake := func(_ context.Context, name string, args []string, in *string) keystore.RunResult {
		return ranRes(0, "the-secret\n")
	}
	store := &osKeyStore{run: fake} // platform empty: the host's own way
	if _, ok := store.Get(ctx, "acct:host"); !ok {
		t.Fatal("Get on the host's own way answered not ok")
	}
	// the Keychain's set is only good when the read-back proves it: a value the store does not
	// hold back is an error naming the store. The platform is pinned, because only that path
	// reads back and the suite runs on both hosts.
	keychain := &osKeyStore{run: fake, platform: "darwin"}
	if err := keychain.Set(ctx, "acct:host", "something-else"); err == nil {
		t.Error("Set the read-back cannot prove answered nil, want an error")
	} else if !strings.Contains(err.Error(), "did not keep") {
		t.Errorf("the refusal = %v, want it to name the store", err)
	}
}

func TestASecretsAccountMustBeASafeName(t *testing.T) {
	ctx := context.Background()
	calls := 0
	fake := func(context.Context, string, []string, *string) keystore.RunResult {
		calls++
		return ranRes(0, "")
	}
	store := &osKeyStore{run: fake}
	for _, bad := range []string{"", "with space", "quote\"", "slash/path"} {
		if _, ok := store.Get(ctx, bad); ok {
			t.Errorf("Get(%q) answered ok, want a refusal before any tool runs", bad)
		}
		if err := store.Set(ctx, bad, "v"); err == nil {
			t.Errorf("Set(%q) answered nil, want a refusal", bad)
		}
	}
	if calls != 0 {
		t.Fatalf("the tool ran %d times for refused names, want none", calls)
	}
	// a store with no runner refuses before anything else
	runnerless := &osKeyStore{}
	if err := runnerless.Set(ctx, "acct", "v"); err == nil || !strings.Contains(err.Error(), "no keystore") {
		t.Errorf("Set without a runner = %v, want the no-keystore error", err)
	}
	if !accountShapeForSecret("acct:one.sub-2") {
		t.Error("accountShapeForSecret refused a name huddle spells itself")
	}
}
