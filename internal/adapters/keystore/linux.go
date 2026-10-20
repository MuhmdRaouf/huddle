// The freedesktop Secret Service (GNOME Keyring, KWallet) through secret-tool. `store` reads the
// secret from stdin.
package keystore

import (
	"context"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/core/ports"
)

const (
	// SecretTool is the Secret Service's tool.
	SecretTool = "secret-tool"
	linuxLabel = "Secret Service"
)

type linuxStore struct {
	id  KeyIdentity
	run Runner
}

// LinuxKeyStore is the provider's key in the freedesktop Secret Service, through secret-tool.
func LinuxKeyStore(id KeyIdentity, run Runner) ports.KeyStore {
	return &linuxStore{id: id, run: run}
}

func (s *linuxStore) Kind() ports.KeyStoreKind { return ports.KeyStoreLinux }

func (s *linuxStore) Label() string { return linuxLabel }

// attributes are the secret's xdg:schema-less attribute pairs secret-tool looks it up by.
func (s *linuxStore) attributes() []string {
	return []string{"service", s.id.Service, "account", s.id.Account}
}

func (s *linuxStore) lookup(ctx context.Context) RunResult {
	return s.run(ctx, SecretTool, append([]string{"lookup"}, s.attributes()...), nil)
}

func (s *linuxStore) Available(ctx context.Context) bool {
	result := s.lookup(ctx)
	if result.Missing {
		return false
	}
	// Exit 1 with nothing on stderr is "no such secret"; a D-Bus or "no secret service" failure
	// says why on stderr.
	return result.Exited(0) || (result.Exited(1) && strings.TrimSpace(result.Stderr) == "")
}

func (s *linuxStore) Get(ctx context.Context) (string, bool) {
	result := s.lookup(ctx)
	if !result.Exited(0) {
		return "", false
	}
	if s.id.AnyValue {
		return TrimNewline(result.Stdout), true
	}
	return trimmedKey(result.Stdout)
}

func (s *linuxStore) Set(ctx context.Context, key string) error {
	if !s.id.AnyValue {
		if err := AssertValidKey(key); err != nil {
			return err
		}
	}
	label := s.id.Display + " API key"
	if s.id.Label != "" {
		label = s.id.Label
	}
	// No trailing newline: secret-tool stores stdin up to EOF as it is.
	args := append([]string{"store", "--label=" + label}, s.attributes()...)
	result := s.run(ctx, SecretTool, args, &key)
	if !result.Exited(0) {
		return StoreFailure(linuxLabel, "store", result)
	}
	return nil
}

func (s *linuxStore) Remove(ctx context.Context) error {
	result := s.run(ctx, SecretTool, append([]string{"clear"}, s.attributes()...), nil)
	if result.Missing {
		return StoreFailure(linuxLabel, "remove", result)
	}
	return nil
}
