package outside

// The OS keystore for huddle's own secrets (SPEC 4.1): the macOS Keychain through
// /usr/bin/security, the freedesktop Secret Service through secret-tool. The tools run without a
// shell, a secret travels on the tool's stdin (never its command line, where `ps` would show
// it), and a store that does not answer within a second leaves the home's keys/ files to hold
// the secret instead — a machine without a keystore never loses one.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// SecretsService is the name huddle's secrets live under in the OS keystore.
const SecretsService = "huddle"

// probeBudget bounds the question of whether the keystore answers (SPEC 4.1: one second);
// toolBudget bounds the get and set of one secret.
const (
	probeBudget = time.Second
	toolBudget  = 10 * time.Second
)

// secretRun is one keystore tool's outcome: what it printed, its exit code, and whether it ran
// at all (a tool that could not start never ran).
type secretRun struct {
	stdout string
	code   int
	ran    bool
}

// ok is the tool's answer being yes.
func (r secretRun) ok() bool { return r.ran && r.code == 0 }

// secretRunner runs one keystore tool without a shell, stdin when given written and closed; nil
// runs it as a child of this process. Tests answer from a fake, so no real keystore is read.
type secretRunner func(ctx context.Context, name string, args []string, stdin *string) secretRun

// spawnSecretRunner is the runner the real store uses: the tool bounded, its stdout collected,
// its exit code kept.
func spawnSecretRunner(ctx context.Context, name string, args []string, stdin *string) secretRun {
	bounded, cancel := context.WithTimeout(ctx, toolBudget)
	defer cancel()
	cmd := exec.CommandContext(bounded, name, args...)
	if stdin != nil {
		cmd.Stdin = strings.NewReader(*stdin)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err == nil {
		return secretRun{stdout: out.String(), ran: true}
	}
	var exited *exec.ExitError
	if errors.As(err, &exited) {
		return secretRun{stdout: out.String(), code: exited.ExitCode(), ran: true}
	}
	return secretRun{}
}

// osKeyStore is the platform's keystore: how it is asked, and on which platform.
type osKeyStore struct {
	run      secretRunner
	platform string // darwin or linux; the host's when empty
}

// SecretsFor answers the store a huddle secret goes to (SPEC 4.1): the OS keystore when its tool
// answers within a second, else the home's keys/ files. HUDDLE_KEYS=files pins the files outright:
// a hermetic run's children must keep every secret under the temp home they were given, whatever
// this machine's real keystore answers.
func SecretsFor(ctx context.Context, h home.Home) home.SecretStore {
	if os.Getenv("HUDDLE_KEYS") == "files" {
		return home.KeyFiles{Dir: h.Keys()}
	}
	store := OSSecrets(spawnSecretRunner)
	if store.Available(ctx) {
		return store
	}
	return home.KeyFiles{Dir: h.Keys()}
}

// OSSecrets is the OS keystore for huddle's secrets under the runner given.
func OSSecrets(run secretRunner) *osKeyStore {
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
		return s.run(bounded, "/usr/bin/security", []string{"default-keychain"}, nil).ok()
	case "linux":
		// exit 1 is "no such secret" — the service answered and holds none yet
		outcome := s.run(bounded, "secret-tool",
			[]string{"lookup", "service", SecretsService, "account", "probe"}, nil)
		return outcome.ran && (outcome.ok() || outcome.code == 1)
	default:
		return false
	}
}

// Get answers the account's secret from the platform's keystore.
func (s *osKeyStore) Get(ctx context.Context, account string) (string, bool) {
	if s.run == nil || !accountShapeForSecret(account) {
		return "", false
	}
	switch s.platformOf() {
	case "darwin":
		outcome := s.run(ctx, "/usr/bin/security",
			[]string{"find-generic-password", "-s", SecretsService, "-a", account, "-w"}, nil)
		if !outcome.ok() {
			return "", false
		}
		return trimToolLine(outcome.stdout), true
	case "linux":
		outcome := s.run(ctx, "secret-tool",
			[]string{"lookup", "service", SecretsService, "account", account}, nil)
		if !outcome.ok() {
			return "", false
		}
		return trimToolLine(outcome.stdout), true
	default:
		return "", false
	}
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
	switch s.platformOf() {
	case "darwin":
		// `security -i` reads its command line from stdin, so the secret stays off argv; -U
		// replaces an existing item. The command may exit 0 without storing, so the read-back
		// proves it.
		command := fmt.Sprintf("add-generic-password -U -s %q -a %q -w %q\n", SecretsService, account, value)
		s.run(ctx, "/usr/bin/security", []string{"-i"}, &command)
		got, ok := s.Get(ctx, account)
		if !ok || got != value {
			return fmt.Errorf("huddle: the macOS Keychain did not keep %q", account)
		}
		return nil
	case "linux":
		// secret-tool stores its stdin up to EOF as it is: no trailing newline
		outcome := s.run(ctx, "secret-tool",
			[]string{"store", "--label=Huddle " + account, "service", SecretsService, "account", account}, &value)
		if !outcome.ok() {
			return fmt.Errorf("huddle: the Secret Service did not keep %q", account)
		}
		return nil
	default:
		return fmt.Errorf("huddle: no keystore on %s", s.platformOf())
	}
}

// platformOf answers the keystore's platform: the one picked, else the host's.
func (s *osKeyStore) platformOf() string {
	if s.platform != "" {
		return s.platform
	}
	return runtime.GOOS
}

// trimToolLine is a tool's stdout as the secret it printed: one trailing line break gone.
func trimToolLine(stdout string) string {
	text := strings.TrimSuffix(stdout, "\n")
	return strings.TrimSuffix(text, "\r")
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
