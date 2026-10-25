// The one place a harness config file is read or written: install and remove read the bytes,
// hand them to the adapter's pure edit, write the answer back with private modes and record
// the change in the huddle home's ledger, so remove can prove the round trip was exact.
package harness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// InstallResult is what one install did.
type InstallResult struct {
	Path    string // the config file
	Changed bool   // the entry is in the file now and was not before
	Created bool   // the file was not there; install wrote it
	Already bool   // the file already held exactly this entry
	Routing string // the file the routing variables went into; "" when the harness got none
}

// RemoveResult is what one remove did.
type RemoveResult struct {
	Path     string // the config file
	Removed  bool   // the entry is gone
	FileGone bool   // the file install had created is gone with it
	Kept     bool   // the entry changed since install: left in place, and said so
	Exact    bool   // the file came back byte for byte as before install
	NotThere bool   // nothing of ours was in the file
}

// Install wires the entries into the named harness's config file — the MCP server, and the
// lifecycle hooks where the harness has a mechanism for them — plus the gateway routing where
// the harness reads a provider base URL from a file (omp's models.yml, opencode's
// opencode.json, pi's models.json) and the entry names a gateway, and records each change in
// the ledger. A malformed config is refused before anything is written.
func Install(ctx context.Context, hudHome string, env Env, name string, entry Entry) (InstallResult, error) {
	a, err := ByName(name)
	if err != nil {
		return InstallResult{}, err
	}
	// the plugin shim comes first: the config names it, so a config without the file behind
	// it would point the harness at nothing
	shim := wantShim(hudHome, name)
	if shim != "" {
		if err := writeShim(shim, entry.Command); err != nil {
			return InstallResult{}, err
		}
	}
	path := a.ConfigPath(env)
	before, existed, err := readIfExists(path)
	if err != nil {
		return InstallResult{}, err
	}
	out, err := installAll(a, before, entry, shim)
	if err != nil {
		return InstallResult{}, &MalformedError{Path: path, Err: err}
	}
	changed := !existed || !bytes.Equal(out, before)
	if changed {
		if err := writeAtomic0600(path, out); err != nil {
			return InstallResult{}, err
		}
		if err := recordLedger(hudHome, ledgerEntry{
			Harness: name, Path: path, Created: !existed, BeforeSHA256: sha256hex(before),
		}); err != nil {
			return InstallResult{}, err
		}
	}
	result := InstallResult{Path: path, Changed: changed, Created: !existed, Already: !changed}
	if routing, ok := routings[name]; ok && entry.Gateway != "" {
		if result.Routing, err = installRouting(hudHome, env, routing, entry); err != nil {
			return InstallResult{}, err
		}
	}
	return result, nil
}

// routing is one harness's model-provider edit that points it at the router's gateway: a file
// of its own beside the MCP config (omp's models.yml, pi's models.json) or a second edit of
// the same file (opencode's provider). The ledger keeps its record under ledgerName.
type routing interface {
	ConfigPath(env Env) string
	Install(cfg []byte, entry Entry) ([]byte, error)
	Remove(cfg []byte, entry Entry) ([]byte, error)
	ledgerName() string
}

// routings is the routing edit per harness whose model provider names a base URL in a file.
var routings = map[string]routing{"omp": ompRouting, "opencode": openCodeRouting, "pi": piRouting}

// installRouting writes the routing edit into the harness's routing file and records the
// change under the file's own ledger name, so remove can tell a file it created.
func installRouting(hudHome string, env Env, routing routing, entry Entry) (string, error) {
	path := routing.ConfigPath(env)
	before, existed, err := readIfExists(path)
	if err != nil {
		return "", err
	}
	out, err := routing.Install(before, entry)
	if err != nil {
		return "", &MalformedError{Path: path, Err: err}
	}
	if existed && bytes.Equal(out, before) {
		return path, nil
	}
	if err := writeAtomic0600(path, out); err != nil {
		return "", err
	}
	return path, recordLedger(hudHome, ledgerEntry{
		Harness: routing.ledgerName(), Path: path, Created: !existed, BeforeSHA256: sha256hex(before),
	})
}

// removeRouting takes the routing edit back out: a file install created and nothing else
// holds is gone, a block the user changed stays. The MCP config's own result says what the
// remove did; the routing file follows it.
func removeRouting(hudHome string, env Env, routing routing, entry Entry) error {
	ledger := readLedger(ledgerPath(hudHome))
	record, hasRecord := ledger.named(routing.ledgerName())
	path := routing.ConfigPath(env)
	if hasRecord && record.Path != "" {
		path = record.Path
	}
	before, existed, err := readIfExists(path)
	if err != nil {
		return err
	}
	if existed {
		out, err := routing.Remove(before, entry)
		switch {
		case errors.Is(err, ErrEntryChanged):
		case err != nil:
			return &MalformedError{Path: path, Err: err}
		case hasRecord && record.Created && (len(bytes.TrimSpace(out)) == 0 || jsonEmptyObject(out)):
			if err := os.Remove(path); err != nil {
				return err
			}
		default:
			if err := writeAtomic0600(path, out); err != nil {
				return err
			}
		}
	}
	if !hasRecord {
		return nil
	}
	return dropLedger(hudHome, routing.ledgerName())
}

// Remove takes every entry of ours back out of the named harness's config file — the MCP
// server and the hook entries with it — and its routing file, and drops the ledger's records. A file install created
// and nothing else holds is gone entirely; a file that is back to its previous bytes is said
// to be; an entry the user changed is left alone, hooks and server with it.
func Remove(ctx context.Context, hudHome string, env Env, name string, entry Entry) (RemoveResult, error) {
	// the routing comes out first, the reverse of install: when it shares the MCP config's
	// file, the config's own record then compares the bytes from before either edit
	if routing, ok := routings[name]; ok {
		if err := removeRouting(hudHome, env, routing, entry); err != nil {
			return RemoveResult{}, err
		}
	}
	return removeConfig(hudHome, env, name, entry)
}

// removeConfig takes every entry of ours out of the harness's MCP config file.
func removeConfig(hudHome string, env Env, name string, entry Entry) (RemoveResult, error) {
	a, err := ByName(name)
	if err != nil {
		return RemoveResult{}, err
	}
	shim := wantShim(hudHome, name)
	ledger := readLedger(ledgerPath(hudHome))
	record, hasRecord := ledger.named(name)
	path := a.ConfigPath(env)
	if hasRecord && record.Path != "" {
		path = record.Path
	}
	before, existed, err := readIfExists(path)
	if err != nil {
		return RemoveResult{}, err
	}
	if !existed {
		if hasRecord {
			if err := dropLedger(hudHome, name); err != nil {
				return RemoveResult{}, err
			}
		}
		removeShim(shim)
		return RemoveResult{Path: path, NotThere: true}, nil
	}
	out, err := removeAll(a, before, entry, shim)
	if errors.Is(err, ErrEntryChanged) {
		if err := dropLedger(hudHome, name); err != nil {
			return RemoveResult{}, err
		}
		// no entry of ours at all, or one the user's hand changed: the parse told them apart
		if !holdsOurs(a, before, entry, shim) {
			removeShim(shim)
			return RemoveResult{Path: path, NotThere: true}, nil
		}
		return RemoveResult{Path: path, Kept: true}, nil
	}
	if err != nil {
		return RemoveResult{}, &MalformedError{Path: path, Err: err}
	}
	if hasRecord && record.Created && emptiedConfig(a, out) {
		if err := os.Remove(path); err != nil {
			return RemoveResult{}, err
		}
		if err := dropLedger(hudHome, name); err != nil {
			return RemoveResult{}, err
		}
		removeShim(shim)
		return RemoveResult{Path: path, Removed: true, FileGone: true}, nil
	}
	if !bytes.Equal(out, before) {
		if err := writeAtomic0600(path, out); err != nil {
			return RemoveResult{}, err
		}
	}
	if err := dropLedger(hudHome, name); err != nil {
		return RemoveResult{}, err
	}
	removeShim(shim)
	exact := hasRecord && sha256hex(out) == record.BeforeSHA256
	return RemoveResult{Path: path, Removed: true, Exact: exact}, nil
}

// wantShim answers the plugin file path the named harness's config names, "" when its hooks
// are config-only.
func wantShim(hudHome, name string) string {
	if _, isPlugin := hookFor(name).(pluginHooks); isPlugin {
		return shimPath(hudHome)
	}
	return ""
}

// Status is one harness's place on this machine.
type Status struct {
	Name      string
	Path      string
	Installed bool // the config holds the huddle entry
	Hooks     bool // the config also holds huddle's hook entries
	Available bool // the harness is here: its binary or its config directory
}

// State answers the one word list prints: installed, available or absent.
func (s Status) State() string {
	if s.Installed {
		return "installed"
	}
	if s.Hooks {
		return "hooks"
	}
	if s.Available {
		return "available"
	}
	return "absent"
}

// List answers every harness's status, in list order.
func List(env Env) []Status {
	out := make([]Status, 0, len(adapters))
	for _, a := range adapters {
		one := Status{Name: a.Name(), Path: a.ConfigPath(env), Available: a.Detect(env)}
		if raw, existed, err := readIfExists(one.Path); err == nil && existed {
			one.Installed = containsEntry(a, raw)
			one.Hooks = holdsHooks(a, raw)
		}
		out = append(out, one)
	}
	return out
}

// MalformedError is a config file huddle refused to edit: it does not parse, and nothing was
// written. The path names the file.
type MalformedError struct {
	Path string
	Err  error
}

func (e *MalformedError) Error() string { return e.Path + ": " + e.Err.Error() }
func (e *MalformedError) Unwrap() error { return e.Err }

// readIfExists answers a file's bytes, or nil when it is not there.
func readIfExists(path string) ([]byte, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return raw, true, nil
}

// writeAtomic0600 writes one file atomically with private modes: a temp file beside it, then
// a rename, so a reader never sees half of it.
func writeAtomic0600(path string, body []byte) error {
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

// containsEntry answers whether a config holds a huddle entry, by the adapter's own kind.
func containsEntry(a Adapter, cfg []byte) bool {
	switch v := a.(type) {
	case jsonAdapter:
		return v.contains(cfg)
	case tomlAdapter:
		return v.contains(cfg)
	}
	return false
}

// emptiedConfig answers whether a config is empty of user content, by the adapter's own kind.
func emptiedConfig(a Adapter, cfg []byte) bool {
	switch v := a.(type) {
	case jsonAdapter:
		return v.emptied(cfg)
	case tomlAdapter:
		return v.emptied(cfg)
	}
	return false
}

// installAll answers the config with every entry of ours in it: the MCP server, and the
// harness's hook entries. Every harness ByName answers has a hook edit, so the lookup cannot
// miss.
func installAll(a Adapter, cfg []byte, entry Entry, shim string) ([]byte, error) {
	out, err := a.Install(cfg, entry)
	if err != nil {
		return nil, err
	}
	return hookFor(a.Name()).install(out, entry.Command, a.Name(), shim)
}

// removeAll answers the config with every entry of ours out of it. An entry the user changed
// — hook or server — refuses the whole remove: the file's ledger record covers both.
func removeAll(a Adapter, cfg []byte, entry Entry, shim string) ([]byte, error) {
	out, err := a.Remove(cfg, entry)
	if err != nil {
		return nil, err
	}
	return hookFor(a.Name()).remove(out, entry.Command, a.Name(), shim)
}

// holdsOurs answers whether any entry of ours — server or hooks — is in the config.
func holdsOurs(a Adapter, cfg []byte, entry Entry, shim string) bool {
	return containsEntry(a, cfg) || holdsHooks(a, cfg)
}

// holdsHooks answers whether a config holds huddle's hook entries, by the harness's own edit.
func holdsHooks(a Adapter, cfg []byte) bool {
	return hookFor(a.Name()).contains(cfg, "", a.Name(), "")
}

// writeShim lays the opencode plugin shim down: the bytes this install's binary runs, private
// modes, rewritten only when they differ.
func writeShim(path, bin string) error {
	want := opencodeShim(bin)
	if have, err := os.ReadFile(path); err == nil && bytes.Equal(have, want) {
		return nil
	}
	return writeAtomic0600(path, want)
}

// removeShim takes the plugin shim back out; a shim that is not there is fine.
func removeShim(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
}

// opencodeShim answers the plugin module huddle writes into its own home for opencode to
// load: it maps the session events opencode fires onto the external hook word and pipes the
// session's facts through (the payload shapes are the v1 SDK's: session.created carries the
// session under info, session.idle names it directly). Bun runs it inside opencode; the node
// module it imports is one Bun provides. Spawned fire and forget: a hook never holds the
// agent, and the kill timer is the 3 s deadline the card grants one.
func opencodeShim(bin string) []byte {
	return fmt.Appendf(nil, `// huddle's opencode plugin: the session events opencode fires reach the hub's registry.
// Written by huddle harness install opencode, which rewrites it; do not edit.
// Runs: %s hook external --harness opencode --event <type>
import { spawn } from "node:child_process";

const BIN = %s;
const EVENTS = {
  "session.created": (p) => ({ session_id: p?.info?.id ?? "", cwd: p?.info?.directory ?? "" }),
  "session.idle": (p) => ({ session_id: p?.sessionID ?? "", cwd: "" }),
};

export const Huddle = async () => ({
  event: async ({ event }) => {
    const map = event ? EVENTS[event.type] : undefined;
    if (!map) return;
    const payload = map(event.properties);
    if (!payload?.session_id) return;
    const proc = spawn(BIN,
      ["hook", "external", "--harness", "opencode", "--event", String(event.type)],
      { stdio: ["pipe", "ignore", "ignore"] });
    proc.on("error", () => {});
    proc.stdin?.on("error", () => {});
    const timer = setTimeout(() => { try { proc.kill(); } catch {} }, 3000);
    proc.on("exit", () => clearTimeout(timer));
    try { proc.stdin?.end(JSON.stringify(payload)); } catch {}
    try { proc.unref?.(); } catch {}
  },
});
`, bin, jsonString(bin))
}

// sha256hex answers the hex digest of one file's bytes: what the ledger compares.
func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// — the ledger: the record of every harness file huddle changed.

// ledgerFile is the ledger's shape.
type ledgerFile struct {
	Version int           `json:"version"`
	Entries []ledgerEntry `json:"entries"`
}

// ledgerEntry is one harness's record: the file install touched, whether install created it,
// and the digest of what was there before. Before is the statusline record's own extra: the
// member install replaced, as raw JSON, so remove can put it back byte for byte.
type ledgerEntry struct {
	Harness      string `json:"harness"`
	Path         string `json:"path"`
	Created      bool   `json:"created"`
	BeforeSHA256 string `json:"before_sha256"`
	Before       string `json:"before,omitempty"`
}

// ledgerPath answers the ledger's file in the huddle home.
func ledgerPath(hudHome string) string { return filepath.Join(hudHome, "harness-ledger.json") }

// readLedger answers the ledger, empty when it is not there or does not parse: a ledger a
// crash half wrote stops nothing.
func readLedger(path string) ledgerFile {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ledgerFile{Version: 1}
	}
	var ledger ledgerFile
	if json.Unmarshal(raw, &ledger) != nil {
		return ledgerFile{Version: 1}
	}
	if ledger.Version == 0 {
		ledger.Version = 1
	}
	return ledger
}

// named answers one harness's record.
func (l *ledgerFile) named(name string) (ledgerEntry, bool) {
	for _, one := range l.Entries {
		if one.Harness == name {
			return one, true
		}
	}
	return ledgerEntry{}, false
}

// recordLedger writes one harness's record into the ledger, replacing any earlier one.
func recordLedger(hudHome string, entry ledgerEntry) error {
	return writeLedger(hudHome, func(l *ledgerFile) {
		for i := range l.Entries {
			if l.Entries[i].Harness == entry.Harness {
				l.Entries[i] = entry
				return
			}
		}
		l.Entries = append(l.Entries, entry)
	})
}

// dropLedger takes one harness's record out of the ledger.
func dropLedger(hudHome, name string) error {
	return writeLedger(hudHome, func(l *ledgerFile) {
		kept := l.Entries[:0]
		for _, one := range l.Entries {
			if one.Harness != name {
				kept = append(kept, one)
			}
		}
		l.Entries = kept
	})
}

// writeLedger edits the ledger in place and writes it back, atomic and 0600.
func writeLedger(hudHome string, edit func(*ledgerFile)) error {
	ledger := readLedger(ledgerPath(hudHome))
	edit(&ledger)
	raw, err := json.Marshal(ledger)
	if err != nil {
		return err
	}
	return writeAtomic0600(ledgerPath(hudHome), raw)
}
