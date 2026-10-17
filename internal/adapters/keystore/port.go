// What the platform key stores share: the runner they call their tools through, the key's names in a
// store, the key's accepted shape and the failure text. A key only ever travels on a tool's stdin:
// never in argv, never in an error message.
package keystore

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// RunResult is what a platform tool returned. A nil Code is a signal; Missing means the tool
// itself could not be started.
type RunResult struct {
	Code    *int
	Stdout  string
	Stderr  string
	Missing bool
}

// Runner runs one tool without a shell; stdin, when given, is written and closed. Tests pass a
// fake.
type Runner func(ctx context.Context, cmd string, args []string, stdin *string) RunResult

// KeyIdentity is a store's names for one secret: the service it lives under and the account it
// belongs to.
type KeyIdentity struct {
	Service string
	Account string
	// Display is the provider as people know it, for the Secret Service label.
	Display string
}

// KeyStoreKind is which store a key lives in at rest.
type KeyStoreKind string

const (
	KeyStoreMacOS KeyStoreKind = "macos"
	KeyStoreLinux KeyStoreKind = "linux"
)

// KeyStore is where a secret lives at rest: the OS secret store.
type KeyStore interface {
	Kind() KeyStoreKind
	// How setup and status lines name the store: "macOS Keychain", "Secret Service", or a path.
	Label() string
	Available(ctx context.Context) bool
	Get(ctx context.Context) (string, bool)
	// Set rejects an invalid key before any tool runs; the error says why the store refused it,
	// never the key itself.
	Set(ctx context.Context, key string) error
	Remove(ctx context.Context) error
}

// keyShape is a provider key as the stores accept it: 16-512 characters of [A-Za-z0-9._-]. This
// also keeps quoting safe.
var keyShape = regexp.MustCompile(`^[A-Za-z0-9._-]{16,512}$`)

// IsValidKey reports whether key fits the stores' accepted shape.
func IsValidKey(key string) bool {
	return keyShape.MatchString(key)
}

// InvalidKeyError is returned before any tool runs; its message never contains the key.
type InvalidKeyError struct{}

func (InvalidKeyError) Error() string {
	return "the key must be 16-512 characters of letters, digits, '.', '_' or '-'"
}

// AssertValidKey returns InvalidKeyError when key is not a shape the stores accept.
func AssertValidKey(key string) error {
	if !IsValidKey(key) {
		return InvalidKeyError{}
	}
	return nil
}

// StoreFailure is a store failure that names the store and the exit code, never stdin or the
// tool's output.
func StoreFailure(label string, action string, result RunResult) error {
	why := "signal"
	switch {
	case result.Missing:
		why = "tool not found"
	case result.Code != nil:
		why = fmt.Sprintf("exit %d", *result.Code)
	}
	return fmt.Errorf("could not %s the key in %s (%s)", action, label, why)
}

// TrimNewline is tool output minus one trailing line break.
func TrimNewline(text string) string {
	text = strings.TrimSuffix(text, "\n")
	return strings.TrimSuffix(text, "\r")
}

// exited reports whether the tool ran to the given exit code (a signal or a missing tool did
// not).
func (r RunResult) exited(code int) bool {
	return r.Code != nil && *r.Code == code
}

// trimmedKey is a get's stdout as a stored key: one trailing line break gone, empty means none.
func trimmedKey(stdout string) (string, bool) {
	value := TrimNewline(stdout)
	if value == "" {
		return "", false
	}
	return value, true
}
