// The 0.0.1 migration: machines set up before huddle-router run one router per provider (zai on
// 18787, kimi on 18788, …) with a pid record, a binary copy, watchdog guards and a ledger naming
// the old port. Migrate001 moves every provider that setup left behind onto huddle-router: the
// provider is recorded in providers.json, the old router retires so its open sessions keep
// working until they close, the old per-session watchdogs stop, the ledger names the daemon, and
// the old binary copy goes once nothing runs from it. Job state and keys are never touched.
package routerd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/MuhmdRaouf/huddle/internal/adapters"
	"github.com/MuhmdRaouf/huddle/internal/adapters/process"
	"github.com/MuhmdRaouf/huddle/internal/core/providers"
	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// oldBinaryName is the 0.0.1 router's binary copy under `<state>/router`.
const oldBinaryName = "plugin-core"

// migrateOnce is the daemon's one migration per start, a goroutine that recovers its own panic:
// a failed migration never costs the router.
func migrateOnce(ctx context.Context, env domain.EnvLookup, log func(line string)) {
	defer func() {
		if reason := recover(); reason != nil {
			log(fmt.Sprintf("huddle-router: recovered: %v", reason))
		}
	}()
	for _, line := range Migrate001(ctx, env, log) {
		log(line)
	}
}

// Migrate001 moves every provider a 0.0.1 setup left behind onto huddle-router and answers one
// line per provider moved. It runs once the router answers health, and is safe to run again: a
// provider with nothing left to move is skipped.
func Migrate001(ctx context.Context, env domain.EnvLookup, log func(line string)) []string {
	var moved []string
	for _, name := range providers.Names {
		provider, ok := providers.ByName(name)
		if !ok {
			continue
		}
		// One provider's panic is one log line; the next provider still runs.
		func() {
			defer func() {
				if reason := recover(); reason != nil {
					log(fmt.Sprintf("huddle-router: moving the 0.0.1 %s setup recovered: %v", name, reason))
				}
			}()
			if migrateProvider001(ctx, provider, env, log) {
				moved = append(moved, fmt.Sprintf("huddle-router: moved the 0.0.1 %s setup to huddle-router", name))
			}
		}()
	}
	return moved
}

// migrateProvider001 moves one provider and says whether anything was moved.
func migrateProvider001(ctx context.Context, provider domain.Provider, env domain.EnvLookup, log func(line string)) bool {
	root := adapters.StateRoot(ctx, provider, env)
	layout := domain.NewStateLayout(root)
	if _, err := os.Stat(layout.SetupDone); err != nil {
		return false
	}
	acted := false
	// The provider joins the daemon: the poll sees providers.json change and serves its lane.
	enabled, err := router.ReadEnabled(env)
	wasEnabled := err == nil && slices.Contains(enabled, provider.Name)
	if err := router.SetEnabled(env, provider.Name, true); err != nil {
		log(fmt.Sprintf("huddle-router: %s: %s", provider.Name, err.Error()))
	} else if !wasEnabled {
		acted = true
	}
	retireOldRouter001(ctx, provider, env, root, log, &acted)
	stopOldWatchdogs001(ctx, provider, root, log, &acted)
	rewriteOldLedger001(ctx, env, layout, log, &acted)
	removeOldBinary001(ctx, provider, root, log, &acted)
	return acted
}

// retireOldRouter001 retires a live 0.0.1 router of ours: it keeps serving the sessions that
// hold its address and exits once they close. A record naming a dead or foreign process is left
// to the next run of the step that can act on it.
func retireOldRouter001(ctx context.Context, provider domain.Provider, env domain.EnvLookup,
	root string, log func(line string), acted *bool) {
	record, ok := router.ReadPidFile(ctx, root)
	if !ok || !router.IsOurProcess(record.Pid, provider.Name, nil) {
		return
	}
	line := router.RetireRouter(ctx, router.RetireOptions{StopOptions: router.StopOptions{
		Provider:  provider,
		Env:       env,
		StateRoot: root,
	}})
	if retired, retiredOk := router.ReadRetiredFile(ctx, root); retiredOk && retired.Pid == record.Pid {
		log(fmt.Sprintf("huddle-router: retired the 0.0.1 %s router (pid %d); it exits when its sessions close",
			provider.Name, record.Pid))
	} else {
		log("huddle-router: " + line)
	}
	*acted = true
}

// stopOldWatchdogs001 signs the per-session `router watch` guards off: a guard naming a live
// process whose command line says `router watch` gets SIGTERM, and every guard file goes — a
// dead watch's file is litter, and a foreign pid's file is never a reason to signal it.
func stopOldWatchdogs001(ctx context.Context, provider domain.Provider, root string, log func(line string), acted *bool) {
	guards, err := filepath.Glob(filepath.Join(root, "watch.*"))
	if err != nil {
		return
	}
	for _, guard := range guards {
		pid, ok := guardPid(guard)
		if ok && pid > 1 && process.IsAlive(pid) {
			if line, ok := router.CommandLineOf(ctx, pid); ok && strings.Contains(line, "router watch") {
				if err := syscall.Kill(int(pid), syscall.SIGTERM); err == nil {
					log(fmt.Sprintf("huddle-router: stopped the 0.0.1 %s router watch (pid %d)",
						provider.Name, pid))
				}
			}
		}
		_ = os.Remove(guard)
		*acted = true
	}
}

// guardPid is the pid a `watch.<pid>` guard names, from its file name.
func guardPid(path string) (int64, bool) {
	name := filepath.Base(path)
	pid, err := strconv.ParseInt(strings.TrimPrefix(name, "watch."), 10, 64)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// rewriteOldLedger001 points the ledger's routerUrl at the daemon, keeping everything else —
// the uninstall's undo takes the routing entries out by the address the ledger names.
func rewriteOldLedger001(ctx context.Context, env domain.EnvLookup, layout domain.StateLayout,
	log func(line string), acted *bool) {
	ledger, ok := router.ReadLedger(ctx, layout.Ledger)
	if !ok {
		return
	}
	want := domain.HuddleRouterURL(env)
	if ledger.RouterURL == want {
		return
	}
	ledger.RouterURL = want
	if err := router.WriteLedger(ctx, layout.Ledger, ledger); err != nil {
		log(fmt.Sprintf("huddle-router: the ledger keeps the old router's address (%s)", err.Error()))
		return
	}
	*acted = true
}

// removeOldBinary001 deletes the 0.0.1 binary copy once no pid or retired record names a live
// process of ours; a router still serving retired sessions keeps its code until the next run.
func removeOldBinary001(ctx context.Context, provider domain.Provider, root string, log func(line string), acted *bool) {
	binary := filepath.Join(domain.NewStateLayout(root).RouterDir, oldBinaryName)
	if _, err := os.Stat(binary); err != nil {
		return
	}
	for _, read := range []func(context.Context, string) (router.PidRecord, bool){
		router.ReadPidFile,
		router.ReadRetiredFile,
	} {
		if record, ok := read(ctx, root); ok && router.IsOurProcess(record.Pid, provider.Name, nil) {
			return
		}
	}
	if err := os.Remove(binary); err != nil {
		log(fmt.Sprintf("huddle-router: the 0.0.1 %s binary copy stays (%s)", provider.Name, err.Error()))
		return
	}
	*acted = true
}
