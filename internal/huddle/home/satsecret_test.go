package home

// The satellite's credential at rest: the store the caller hands holds it under the
// one account the hub's address names, replaced whole by the next join. A missing or blank
// credential is an error, because a link with none must not start. Tests answer from a map, so
// no real keystore is read.

import (
	"context"
	"strings"
	"testing"
)

// satSecrets is a secret store in a test: one map, read after the run.
type satSecrets struct {
	held map[string]string
}

func (s *satSecrets) Get(_ context.Context, account string) (string, bool) {
	value, ok := s.held[account]
	return value, ok
}

func (s *satSecrets) Set(_ context.Context, account, value string) error {
	if s.held == nil {
		s.held = map[string]string{}
	}
	s.held[account] = value
	return nil
}

// TestSatelliteSecretRoundTrip proves the credential lives in the store the caller hands: saved
// under the hub's one account, replaced whole by the next join, and a missing or blank one is an
// error that says how to join.
func TestSatelliteSecretRoundTrip(t *testing.T) {
	store := &satSecrets{}
	ctx := context.Background()
	if _, err := LoadSatelliteSecret(ctx, store, "https://huddle.box:4443"); err == nil {
		t.Fatal("a store with no credential answered one")
	}
	if err := SaveSatelliteSecret(ctx, store, "https://huddle.box:4443", "hsat_first\n"); err != nil {
		t.Fatalf("save: %v", err)
	}
	kept, err := LoadSatelliteSecret(ctx, store, "https://huddle.box:4443")
	if err != nil || kept != "hsat_first" {
		t.Fatalf("the kept credential = %q, %v, want the trimmed credential", kept, err)
	}
	if store.held[SatelliteSecretName("https://huddle.box:4443")] != "hsat_first" {
		t.Errorf("the store holds %q under the hub's account", store.held[SatelliteSecretName("https://huddle.box:4443")])
	}
	// a second join replaces the credential whole
	if err := SaveSatelliteSecret(ctx, store, "https://huddle.box:4443", "hsat_second"); err != nil {
		t.Fatalf("save again: %v", err)
	}
	if kept, _ = LoadSatelliteSecret(ctx, store, "https://huddle.box:4443"); kept != "hsat_second" {
		t.Fatalf("the replaced credential = %q, want hsat_second", kept)
	}
	// two hubs keep their own accounts
	if err := SaveSatelliteSecret(ctx, store, "http://127.0.0.1:18860", "hsat_other"); err != nil {
		t.Fatalf("save the other hub: %v", err)
	}
	if kept, _ = LoadSatelliteSecret(ctx, store, "http://127.0.0.1:18860"); kept != "hsat_other" {
		t.Fatalf("the other hub's credential = %q", kept)
	}
	if kept, _ = LoadSatelliteSecret(ctx, store, "https://huddle.box:4443"); kept != "hsat_second" {
		t.Fatalf("the first hub's credential = %q", kept)
	}
	// an empty hub url stores nothing
	if err := SaveSatelliteSecret(ctx, store, "", "hsat_x"); err == nil {
		t.Error("a credential with no hub url was stored")
	}
	if strings.Contains(kept, "\n") {
		t.Error("the credential keeps its newline")
	}
	// a blank stored value is no credential: the machine joins again
	store.held[SatelliteSecretName("https://huddle.box:4443")] = "   "
	if _, err := LoadSatelliteSecret(ctx, store, "https://huddle.box:4443"); err == nil || !strings.Contains(err.Error(), "join again") {
		t.Errorf("a blank credential = %v, want the join-again error", err)
	}
}

// TestSatelliteSecretNameIsAValidAccount pins the account a hub's credential sits under: the
// host lowercased with its port's colon spelled a dash, always a name the secret store takes.
func TestSatelliteSecretNameIsAValidAccount(t *testing.T) {
	name := SatelliteSecretName("https://Hub.Example.ts.net:8443")
	if name != "satellite:hub.example.ts.net-8443" {
		t.Fatalf("the account = %q, want satellite:hub.example.ts.net-8443", name)
	}
	if !accountShape.MatchString(name) {
		t.Errorf("the account %q is not a name the store takes", name)
	}
	if name := SatelliteSecretName("http://127.0.0.1:18860"); name != "satellite:127.0.0.1-18860" {
		t.Errorf("the loopback account = %q", name)
	}
}
