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

	"github.com/MuhmdRaouf/huddle/internal/core/router"
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
			state := one.State()
			if one.Hooks {
				state += "+hooks"
			}
			c.say(fmt.Sprintf("%-9s %-19s %s", one.Name, state, one.Path))
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

// harnessInstall wires the harness and says what changed. The hooks ride the same install
// where the agent has a lifecycle mechanism; the note names them.
func (c *cli) harnessInstall(ctx context.Context, hudHome, name string) int {
	got, err := harness.Install(ctx, hudHome, harnessEnv(c.w), name, harnessEntry(c.w, name))
	if err != nil {
		return c.die(err.Error(), 1)
	}
	note := ""
	if events, err := harness.HookEvents(name); err == nil && len(events) > 0 {
		note = fmt.Sprintf(", hooks for %d events", len(events))
	}
	switch {
	case got.Already:
		c.say(fmt.Sprintf("%s: already wired into %s%s", name, got.Path, note))
	case got.Created:
		c.say(fmt.Sprintf("%s: wired into %s (created it)%s", name, got.Path, note))
	default:
		c.say(fmt.Sprintf("%s: wired into %s%s", name, got.Path, note))
	}
	return 0
}

// harnessRemove takes the harness back out and says what happened.
func (c *cli) harnessRemove(ctx context.Context, hudHome, name string) int {
	got, err := harness.Remove(ctx, hudHome, harnessEnv(c.w), name, harnessEntry(c.w, name))
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

// harnessEntry answers the entry one harness's config gets: this binary's mcp word, and the
// router's /gateway on the port the router serves ($HUDDLE_ROUTER_PORT, else 18786). A port
// variable that does not parse names no gateway, the way the router refuses it.
func harnessEntry(w *World, name string) harness.Entry {
	entry := harness.NewEntry(name, harnessBin(w))
	if port, err := router.PortOf(worldEnv(w)); err == nil {
		entry.Gateway = fmt.Sprintf("http://127.0.0.1:%d%s", port, router.GatewayPrefix)
	}
	return entry
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

// harnessBin answers the binary the entry runs: the launcher's stable current path when one sits
// beside the running binary — it survives an upgrade, so the config never goes stale — else the
// running executable, symlinks resolved, as before the launcher kept that link.
func harnessBin(w *World) string {
	return harness.StableBin(w.bin())
}
