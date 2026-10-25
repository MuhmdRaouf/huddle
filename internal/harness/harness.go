// Package harness wires huddle's MCP server into the other coding agents on this machine:
// Codex CLI, Gemini CLI, Qwen Code, opencode, Crush, omp, pi and zcode. Every adapter is a pure edit over the
// harness's config bytes, so install and remove round-trip a config byte for byte, and the one
// file-touching path (Install, Remove) records every change in the huddle home's ledger so
// remove undoes exactly what install did. Install also wires each agent's own lifecycle hooks
// where it has a mechanism for them (hook.go); radar's transcript collectors are other cards.
package harness

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// Env is the machine a harness resolves its config path and binary in: the variable lookup and
// the PATH search. The zero value reads the process's own environment.
type Env struct {
	Lookup func(key string) string
	Which  func(bin string) bool
}

// lookup reads one variable; nil reads the process's.
func (e Env) lookup(key string) string {
	if e.Lookup == nil {
		return os.Getenv(key)
	}
	return e.Lookup(key)
}

// which answers whether a binary is on PATH; nil runs the real search.
func (e Env) which(bin string) bool {
	if e.Which == nil {
		_, err := exec.LookPath(bin)
		return err == nil
	}
	return e.Which(bin)
}

// ProcessEnv answers the environment of the running process.
func ProcessEnv() Env { return Env{} }

// Entry is the MCP server record one harness's config holds for huddle.
type Entry struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
	Gateway string // the router's /gateway base URL a harness's model provider names; "" names none
}

// bridgeRequestTimeoutMS bounds one bridge call in a harness whose entry names a timeout: a
// wait holds its request this long, then answers timeout and the agent calls wait again.
const bridgeRequestTimeoutMS = 120000

// NewEntry answers the entry the named harness runs: this binary, its mcp word, and the env
// var naming the harness, so the bridge knows who spawned it.
func NewEntry(name, bin string) Entry {
	return Entry{
		Name:    "huddle",
		Command: bin,
		Args:    []string{"mcp"},
		Env:     map[string]string{"HUDDLE_HARNESS": name},
	}
}

// StableBin answers the binary path an entry should name: the launcher's current link when one
// sits beside the running binary — <home>/current, which every verified install re-points at the
// new version, so a config naming it survives an upgrade — else the binary's own resolved path.
func StableBin(bin string) string {
	if real, err := filepath.EvalSymlinks(bin); err == nil {
		bin = real
	}
	current := filepath.Join(filepath.Dir(filepath.Dir(bin)), "current")
	if info, err := os.Stat(current); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
		return current
	}
	return bin
}

// Adapter is one harness's config edit: bytes in, bytes out, nothing but the huddle entry
// changed. A nil cfg is a config file that is not there yet.
type Adapter interface {
	Name() string
	ConfigPath(env Env) string
	Detect(env Env) bool
	Install(cfg []byte, entry Entry) ([]byte, error)
	Remove(cfg []byte, entry Entry) ([]byte, error)
}

// adapters is every harness this package wires, in list order.
var adapters = []Adapter{codex, gemini, qwen, opencode, crush, omp, pi, zcode}

// Names answers the harness names in list order.
func Names() []string {
	names := make([]string, 0, len(adapters))
	for _, a := range adapters {
		names = append(names, a.Name())
	}
	return names
}

// ByName answers the named harness's adapter.
func ByName(name string) (Adapter, error) {
	for _, a := range adapters {
		if a.Name() == name {
			return a, nil
		}
	}
	return nil, UnknownHarnessError{Name: name}
}

// UnknownHarnessError is a name no adapter answers to.
type UnknownHarnessError struct{ Name string }

func (e UnknownHarnessError) Error() string {
	return "unknown harness " + strconv.Quote(e.Name) + " (huddle harness list shows the names)"
}

// ErrEntryChanged is remove's answer when the huddle entry in the file is no longer the one
// install wrote: the user changed it, and remove leaves it alone.
var ErrEntryChanged = errors.New("the huddle entry changed since install")

// homeDir answers one directory of the user's home.
func homeDir(env Env, name string) string {
	return filepath.Join(env.lookup("HOME"), name)
}

// xdgDir answers one directory of the XDG config root.
func xdgDir(env Env, name string) string {
	if xdg := env.lookup("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, name)
	}
	return filepath.Join(env.lookup("HOME"), ".config", name)
}

// dirThere answers whether a directory exists.
func dirThere(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
