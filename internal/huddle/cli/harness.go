// The harness word: wire huddle's MCP server into the other coding agents on this machine.
// A machine word, not a channel operation: it runs before any session identity is read, from
// the terminal or from a session, and never talks to a server. internal/harness holds the
// per-harness edits and the ledger; this word only spells what they answered.
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/harness"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// harnessWord runs `huddle harness list | install <name> | remove <name>`. Exit: 0 ok, 2 usage,
// 4 a name no harness answers to (or nothing of ours to remove).
func (c *cli) harness(ctx context.Context) int {
	sub := "list"
	if next, ok := c.shift(); ok {
		sub = next
	}
	switch sub {
	case "list":
		for _, one := range harness.List(harnessEnv(c.w)) {
			c.say(fmt.Sprintf("%-9s %-10s %s", one.Name, one.State(), one.Path))
		}
		return 0
	case "install", "remove":
		name, ok := c.shift()
		if !ok {
			return c.die(fmt.Sprintf("harness %s <name>  (%s)", sub, strings.Join(harness.Names(), ", ")), 2)
		}
		if _, err := harness.ByName(name); err != nil {
			return c.die(err.Error(), 4)
		}
		hud, err := home.Resolve(c.w.getenv)
		if err != nil {
			return c.die(err.Error(), 1)
		}
		if sub == "install" {
			return c.harnessInstall(ctx, hud.Root, name)
		}
		return c.harnessRemove(ctx, hud.Root, name)
	default:
		return c.die("harness list | install <name> | remove <name>", 2)
	}
}

// harnessInstall wires the harness and says what changed.
func (c *cli) harnessInstall(ctx context.Context, hudHome, name string) int {
	got, err := harness.Install(ctx, hudHome, harnessEnv(c.w), name, harness.NewEntry(name, harnessBin(c.w)))
	if err != nil {
		return c.die(err.Error(), 1)
	}
	switch {
	case got.Already:
		c.say(fmt.Sprintf("%s: already wired into %s", name, got.Path))
	case got.Created:
		c.say(fmt.Sprintf("%s: wired into %s (created it)", name, got.Path))
	default:
		c.say(fmt.Sprintf("%s: wired into %s", name, got.Path))
	}
	return 0
}

// harnessRemove takes the harness back out and says what happened.
func (c *cli) harnessRemove(ctx context.Context, hudHome, name string) int {
	got, err := harness.Remove(ctx, hudHome, harnessEnv(c.w), name, harness.NewEntry(name, harnessBin(c.w)))
	if err != nil {
		return c.die(err.Error(), 1)
	}
	switch {
	case got.NotThere:
		return c.die(fmt.Sprintf("%s: nothing to remove at %s", name, got.Path), 4)
	case got.Kept:
		c.say(fmt.Sprintf("%s: the huddle entry in %s changed since install; left in place", name, got.Path))
	case got.FileGone:
		c.say(fmt.Sprintf("%s: removed %s (install created it)", name, got.Path))
	case got.Exact:
		c.say(fmt.Sprintf("%s: removed from %s (the file is back to its previous bytes)", name, got.Path))
	default:
		c.say(fmt.Sprintf("%s: removed from %s (the file keeps other changes)", name, got.Path))
	}
	return 0
}

// harnessEnv answers the harness package's view of this world: its variables and its PATH,
// never the process's, so a test's answer is the test's own.
func harnessEnv(w *World) harness.Env {
	return harness.Env{
		Lookup: w.getenv,
		Which: func(bin string) bool {
			for _, dir := range filepath.SplitList(w.getenv("PATH")) {
				if dir == "" {
					continue
				}
				if info, err := os.Stat(filepath.Join(dir, bin)); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
					return true
				}
			}
			return false
		},
	}
}

// harnessBin answers the binary the entry runs: the running executable, symlinks resolved.
func harnessBin(w *World) string {
	bin := w.bin()
	if real, err := filepath.EvalSymlinks(bin); err == nil {
		return real
	}
	return bin
}
