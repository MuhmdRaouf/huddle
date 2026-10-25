package home

// Where huddle's own secrets live at rest (SPEC 4.1): the OS keystore is preferred, and the
// home's keys/ directory holds them where none answers — one 0600 file per secret, so a secret
// never sits inside a file another secret shares. Accounts are huddle's names, as
// "dns:<provider>" for a DNS provider's token.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SecretStore is where huddle's secrets live at rest: the OS keystore, or the home's keys/ files
// where none answers (SPEC 4.1). Accounts are huddle's own names, as "dns:<provider>".
type SecretStore interface {
	Get(ctx context.Context, account string) (string, bool)
	Set(ctx context.Context, account, value string) error
}

// accountShape is the account names a file is named after: huddle's own words with their ":"
// separators, never a path piece — the name becomes a file name under keys/.
var accountShape = regexp.MustCompile(`^[a-z0-9][a-z0-9.:-]*$`)

// secretFile is the file one account's secret lives in: the account's ":" spelled "-", a file
// name on every filesystem huddle runs on.
func secretFile(dir, account string) string {
	name := strings.ReplaceAll(account, ":", "-")
	return filepath.Join(dir, name)
}

// KeyFiles is the home's keys/ directory as a SecretStore: one 0600 file per account, written
// atomically so a reader never sees half a secret.
type KeyFiles struct{ Dir string }

// Get answers the account's secret; false when no file holds one.
func (k KeyFiles) Get(_ context.Context, account string) (string, bool) {
	if !accountShape.MatchString(account) {
		return "", false
	}
	value, err := os.ReadFile(secretFile(k.Dir, account))
	if err != nil || len(value) == 0 {
		return "", false
	}
	return string(value), true
}

// Set writes the account's secret; an account that is not huddle's name shape is refused before
// anything is written, so no path outside keys/ is ever named.
func (k KeyFiles) Set(_ context.Context, account, value string) error {
	if !accountShape.MatchString(account) {
		return fmt.Errorf("huddle: %q is not a secret's account name", account)
	}
	return writeFile0600(secretFile(k.Dir, account), []byte(value))
}

// Delete takes the account's secret back out; an account no file holds is no error. An account
// that is not huddle's name shape is refused before anything is named, as Set refuses it.
func (k KeyFiles) Delete(_ context.Context, account string) error {
	if !accountShape.MatchString(account) {
		return fmt.Errorf("huddle: %q is not a secret's account name", account)
	}
	err := os.Remove(secretFile(k.Dir, account))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
