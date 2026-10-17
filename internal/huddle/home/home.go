// One home per user, where every huddle entry finds the server's files: config.toml, the
// channels, the sessions' credentials and the run directory. The home is resolved the same way
// everywhere (Resolve): HUDDLE_HOME, else $XDG_CONFIG_HOME/huddle, else ~/.config/huddle — never
// a folder of the project a session happens to run in, so every session on the machine reports
// to the same server. Ensure lays the tree out with private modes (0700 dirs, 0600 files); it
// never rewrites what exists, so a config the owner edited stays as they left it.
package home

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Home is one user's huddle home: every path huddle keeps comes off its root.
type Home struct{ Root string }

// NoHomeError names an environment Resolve cannot place a home in.
type NoHomeError struct{}

func (NoHomeError) Error() string { return "huddle: no home: set HOME or HUDDLE_HOME" }

// Resolve answers the home the environment names: HUDDLE_HOME first, then
// $XDG_CONFIG_HOME/huddle, then ~/.config/huddle. An empty variable counts as unset; none of the
// three set is an error, never a guess.
func Resolve(env func(string) string) (Home, error) {
	if env == nil {
		env = os.Getenv
	}
	if given := env("HUDDLE_HOME"); given != "" {
		return Home{Root: given}, nil
	}
	if xdg := env("XDG_CONFIG_HOME"); xdg != "" {
		return Home{Root: filepath.Join(xdg, "huddle")}, nil
	}
	if user := env("HOME"); user != "" {
		return Home{Root: filepath.Join(user, ".config", "huddle")}, nil
	}
	return Home{}, NoHomeError{}
}

// Ensure lays the home's tree out, creating what is missing with private modes: 0700 for every
// directory, 0600 for every file. What already exists is left as it is — a config the owner
// wrote, or a tree another entry just created — so a second Ensure changes nothing.
func (h Home) Ensure(ctx context.Context) error {
	for _, dir := range []string{h.Root, h.Channels(), h.Sessions(), h.Run(), h.Keys()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	for _, file := range []string{h.Outbox(), h.Imports()} {
		if _, err := os.Stat(file); err == nil {
			continue
		}
		if err := writeFile0600(file, nil); err != nil {
			return err
		}
	}
	return ensureConfig(h)
}

// Config is the home's config.toml.
func (h Home) Config() string { return filepath.Join(h.Root, "config.toml") }

// HubDB is the hub's own database: the registry, the needs-you list, devices and audit.
func (h Home) HubDB() string { return filepath.Join(h.Root, "hub.db") }

// Channels is the directory of one SQLite file per channel.
func (h Home) Channels() string { return filepath.Join(h.Root, "channels") }

// ChannelDB is one channel's database.
func (h Home) ChannelDB(name string) string { return filepath.Join(h.Channels(), name+".db") }

// Auth is the credential digests file.
func (h Home) Auth() string { return filepath.Join(h.Root, "auth.json") }

// Knowledge is the server-scope knowledge database.
func (h Home) Knowledge() string { return filepath.Join(h.Root, "knowledge.db") }

// KnowledgeFile answers the server-wide knowledge store's file under dir, adopting the earlier
// shared.db spelling once: a directory written before the rename keeps its rows. The rename is
// guarded by the new file's absence, so a store the import already filled is never overwritten.
func KnowledgeFile(dir string) string {
	file := filepath.Join(dir, "knowledge.db")
	if _, err := os.Stat(file); err != nil {
		_ = os.Rename(filepath.Join(dir, "shared.db"), file)
	}
	return file
}

// ImportNotice is where the one-time import leaves the line the next session start says.
func (h Home) ImportNotice() string { return filepath.Join(h.Run(), "import-notice") }

// Sessions is the directory of the sessions' credential files.
func (h Home) Sessions() string { return filepath.Join(h.Root, "sessions") }

// Outbox is a satellite's reports while the hub is unreachable.
func (h Home) Outbox() string { return filepath.Join(h.Root, "outbox.jsonl") }

// Imports is the import ledger: which legacy homes already moved in.
func (h Home) Imports() string { return filepath.Join(h.Root, "imports.json") }

// Service is the service ledger: the record of the system service install (the launchd agent or
// systemd user unit written outside the home), removed with the service.
func (h Home) Service() string { return filepath.Join(h.Root, "service.json") }

// Run is the run directory: the pid, the port and the log of the running server.
func (h Home) Run() string { return filepath.Join(h.Root, "run") }

// PidFile is the running server's pid.
func (h Home) PidFile() string { return filepath.Join(h.Run(), "huddle.pid") }

// PortFile is the port the running server answers on.
func (h Home) PortFile() string { return filepath.Join(h.Run(), "huddle.port") }

// LogFile is the running server's log.
func (h Home) LogFile() string { return filepath.Join(h.Run(), "huddle.log") }

// TLS is the hub's certificate directory.
func (h Home) TLS() string { return filepath.Join(h.Root, "tls") }

// Keys holds the secrets the OS keystore would not answer: a 0600 file each.
func (h Home) Keys() string { return filepath.Join(h.Root, "keys") }

// writeFile0600 writes one file atomically with private modes: a temp file beside it, then a
// rename, so a reader never sees half of it.
func writeFile0600(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
