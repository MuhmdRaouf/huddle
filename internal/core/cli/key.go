package cli

// `key`: read the provider's key from stdin into the key store setup uses. The key never travels
// in an argument (a process list shows those) and never reaches an output line.

import (
	"context"
	"strings"
)

// KeyCommand is `key`: the key piped on stdin goes into the OS store, or the key file where the
// platform has none.
type KeyCommand struct{}

var _ Command = KeyCommand{}

// Name is the command's.
func (KeyCommand) Name() string { return "key" }

// Synopsis is the command's line.
func (KeyCommand) Synopsis() []string { return []string{"key   (the key is read from stdin)"} }

// Options: none; a flag would be one more place for a key to land.
func (KeyCommand) Options() map[string]OptionSpec { return map[string]OptionSpec{} }

// Hidden: key is a user command.
func (KeyCommand) Hidden() bool { return false }

// Run stores the key; every refusal is one error line and exit 1, none of them carrying the key.
func (KeyCommand) Run(ctx context.Context, call Invocation) (ExitCode, error) {
	deps := call.Deps()
	if len(call.Positionals()) > 0 {
		deps.Out.Error("key: pass the key on stdin, not as an argument")
		return ExitUnexpected, nil
	}
	entry := deps.Host.KeyEntry
	if entry == nil {
		deps.Out.Error("key: this host has no key store")
		return ExitUnexpected, nil
	}
	text, err := ReadStdin(ctx)
	if err != nil {
		deps.Out.Error("key: cannot read stdin (" + err.Error() + ")")
		return ExitUnexpected, nil
	}
	key := strings.TrimSpace(text)
	if key == "" {
		deps.Out.Error("key: stdin held no key")
		return ExitUnexpected, nil
	}
	store := entry.Store(ctx)
	if err := store.Set(ctx, key); err != nil {
		deps.Out.Error("key: " + err.Error())
		return ExitUnexpected, nil
	}
	deps.Out.Line("key: saved in " + store.Label())
	return ExitOK, nil
}
