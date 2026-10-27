package outside

// The OS keystore for huddle's own secrets, through the shared
// keystore adapters: the macOS Keychain and the freedesktop Secret Service.
// The tools run without a shell, a secret travels on the tool's stdin (never
// its command line, where `ps` would show it), and a store that does not
// answer within a second leaves the home's keys/ files to hold the secret
// instead — a machine without a keystore never loses one.

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/keystore"
	"github.com/MuhmdRaouf/huddle/internal/core/ports"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// SecretsService is the name huddle's secrets live under in the OS keystore.
const SecretsService = "huddle"

// probeBudget bounds the question of whether the keystore answers, at one second;
// toolBudget bounds the get and set of one secret.
const (
	probeBudget = time.Second
	toolBudget  = 10 * time.Second
)

// osKeyStore is the platform's keystore: how it is asked, and on which platform.
type osKeyStore struct {
	run      keystore.Runner
	platform string // darwin or linux; the host's when empty
}

// SecretsFor answers the store a huddle secret goes to: the OS keystore when its tool
// answers within a second, else the home's keys/ files. HUDDLE_KEYS=files pins the files outright:
// a hermetic run's children must keep every secret under the temp home they were given, whatever
// this machine's real keystore answers.
func SecretsFor(ctx context.Context, h home.Home) home.SecretStore {
	if os.Getenv("HUDDLE_KEYS") == "files" {
		return home.KeyFiles{Dir: h.Keys()}
	}
	store := OSSecrets(keystore.SpawnRunner)
	if store.Available(ctx) {
		return store
	}
	return home.KeyFiles{Dir: h.Keys()}
}

// OSSecrets is the OS keystore for huddle's secrets under the runner given.
func OSSecrets(run keystore.Runner) *osKeyStore {
	return &osKeyStore{run: run}
}

// Available answers whether the platform's keystore answers at all: a machine without one keeps
// its secrets in files, and one that hangs is not waited for past the probe's second.
func (s *osKeyStore) Available(ctx context.Context) bool {
	if s.run == nil {
		return false
	}
	bounded, cancel := context.WithTimeout(ctx, probeBudget)
	defer cancel()
	switch s.platformOf() {
	case "darwin":
		result := s.run(bounded, keystore.Security, []string{"default-keychain"}, nil)
		return !result.Missing && result.Exited(0)
	case "linux":
		// exit 1 is "no such secret" — the service answered and holds none yet
		outcome := s.run(bounded, keystore.SecretTool,
			[]string{"lookup", "service", SecretsService, "account", "probe"}, nil)
		return !outcome.Missing && (outcome.Exited(0) || outcome.Exited(1))
	default:
		return false
	}
}

// storeFor answers the platform's store for one secret, nil where the platform has none. Huddle
// secrets are not API keys, so the store takes any value and labels each one with its account.
func (s *osKeyStore) storeFor(account string) ports.KeyStore {
	id := keystore.KeyIdentity{
		Service:  SecretsService,
		Account:  account,
		Display:  "Huddle",
		Label:    "Huddle " + account,
		AnyValue: true,
	}
	switch s.platformOf() {
	case "darwin":
		return keystore.MacosKeyStore(id, s.run)
	case "linux":
		return keystore.LinuxKeyStore(id, s.run)
	default:
		return nil
	}
}

// Get answers the account's secret from the platform's keystore.
func (s *osKeyStore) Get(ctx context.Context, account string) (string, bool) {
	if s.run == nil || !accountShapeForSecret(account) {
		return "", false
	}
	store := s.storeFor(account)
	if store == nil {
		return "", false
	}
	bounded, cancel := context.WithTimeout(ctx, toolBudget)
	defer cancel()
	return store.Get(bounded)
}

// Set writes the account's secret into the platform's keystore, the secret on the tool's stdin
// (never its command line). A store that says no answers an error that names the store, never
// the secret.
func (s *osKeyStore) Set(ctx context.Context, account, value string) error {
	if s.run == nil {
		return fmt.Errorf("huddle: no keystore to hold %q", account)
	}
	if !accountShapeForSecret(account) {
		return fmt.Errorf("huddle: %q is not a secret's account name", account)
	}
	store := s.storeFor(account)
	if store == nil {
		return fmt.Errorf("huddle: no keystore on %s", s.platformOf())
	}
	bounded, cancel := context.WithTimeout(ctx, toolBudget)
	defer cancel()
	if err := store.Set(bounded, value); err != nil {
		return fmt.Errorf("huddle: the %s did not keep %q", store.Label(), account)
	}
	return nil
}

// Delete takes the account's secret back out of the platform's keystore; the account on the
// tool's command line is no secret. A store that says no answers an error that names the store,
// never the secret.
func (s *osKeyStore) Delete(ctx context.Context, account string) error {
	if s.run == nil {
		return fmt.Errorf("huddle: no keystore to hold %q", account)
	}
	if !accountShapeForSecret(account) {
		return fmt.Errorf("huddle: %q is not a secret's account name", account)
	}
	store := s.storeFor(account)
	if store == nil {
		return fmt.Errorf("huddle: no keystore on %s", s.platformOf())
	}
	bounded, cancel := context.WithTimeout(ctx, toolBudget)
	defer cancel()
	if err := store.Remove(bounded); err != nil {
		return fmt.Errorf("huddle: the %s did not drop %q", store.Label(), account)
	}
	return nil
}

// platformOf answers the keystore's platform: the one picked, else the host's.
func (s *osKeyStore) platformOf() string {
	if s.platform != "" {
		return s.platform
	}
	return runtime.GOOS
}

// accountShapeForSecret keeps an account name safe to quote into a tool's command line: huddle's
// own words with their ":" separators.
func accountShapeForSecret(account string) bool {
	if account == "" {
		return false
	}
	for _, r := range account {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == ':', r == '.', r == '-':
		default:
			return false
		}
	}
	return true
}
