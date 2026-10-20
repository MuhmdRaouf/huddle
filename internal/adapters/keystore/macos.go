// macOS Keychain through /usr/bin/security. Writes and deletes go through `security -i`, which
// reads its command line from stdin, so the key never reaches argv (where `ps` would show it).
// Reads print the password on stdout.
package keystore

import (
	"context"
	"fmt"

	"github.com/MuhmdRaouf/huddle/internal/core/ports"
)

// Security is the macOS Keychain's tool.
const Security = "/usr/bin/security"

const macosLabel = "macOS Keychain"

type macosStore struct {
	id  KeyIdentity
	run Runner
}

// MacosKeyStore is the provider's key in the macOS Keychain, through /usr/bin/security.
func MacosKeyStore(id KeyIdentity, run Runner) ports.KeyStore {
	return &macosStore{id: id, run: run}
}

func (s *macosStore) Kind() ports.KeyStoreKind { return ports.KeyStoreMacOS }

func (s *macosStore) Label() string { return macosLabel }

// where is the item's names as `security -i` wants them on its command line. The service name is
// validated, so the double quotes cannot be broken out of.
func (s *macosStore) where() string {
	return `-s "` + s.id.Service + `" -a "` + s.id.Account + `"`
}

func (s *macosStore) read(ctx context.Context) RunResult {
	return s.run(ctx, Security, []string{"find-generic-password", "-s", s.id.Service, "-a", s.id.Account, "-w"}, nil)
}

func (s *macosStore) Available(ctx context.Context) bool {
	result := s.run(ctx, Security, []string{"default-keychain"}, nil)
	return !result.Missing && result.Exited(0)
}

func (s *macosStore) Get(ctx context.Context) (string, bool) {
	result := s.read(ctx)
	if !result.Exited(0) {
		return "", false
	}
	if s.id.AnyValue {
		return TrimNewline(result.Stdout), true
	}
	return trimmedKey(result.Stdout)
}

func (s *macosStore) Set(ctx context.Context, key string) error {
	if !s.id.AnyValue {
		if err := AssertValidKey(key); err != nil {
			return err
		}
	} else if !quotable(key) {
		return fmt.Errorf("%s: a secret holding a double quote, a backslash or a character outside printable ASCII cannot be stored", macosLabel)
	}
	// `-U` updates an existing item.
	command := "add-generic-password -U " + s.where() + ` -w "` + key + `"` + "\n"
	result := s.run(ctx, Security, []string{"-i"}, &command)
	// `security -i` may exit 0 after a failed command: only a read-back proves the write.
	back := s.read(ctx)
	if !result.Exited(0) {
		return StoreFailure(macosLabel, "store", result)
	}
	if !back.Exited(0) || TrimNewline(back.Stdout) != key {
		return StoreFailure(macosLabel, "store", back)
	}
	return nil
}

func (s *macosStore) Remove(ctx context.Context) error {
	command := "delete-generic-password " + s.where() + "\n"
	result := s.run(ctx, Security, []string{"-i"}, &command)
	if result.Missing {
		return StoreFailure(macosLabel, "remove", result)
	}
	return nil
}

// quotable is whether a value can sit inside the double quotes of a `security -i` command line:
// printable ASCII without `"` or `\`, so it can neither close the quotes nor start an escape.
func quotable(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c < 0x20 || c > 0x7e || c == '"' || c == '\\' {
			return false
		}
	}
	return true
}
