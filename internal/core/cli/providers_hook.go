// The providers' hook work inside huddle's merged hook. The provider plugins ship no hooks of
// their own: one pass over the five providers at session start re-applies a set-up provider's
// wiring after a plugin update, cleans up after a removed one and prints the
// jobs-awaiting-review line, and one pass at subagent stop says what a finished agent ran on.
// Each provider runs under its own recover, so one provider's failure never stops the next, and
// every failure is silence: a hook never disturbs the session it observes.

package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/MuhmdRaouf/huddle/internal/adapters"
	"github.com/MuhmdRaouf/huddle/internal/core/app"
	"github.com/MuhmdRaouf/huddle/internal/core/ports"
	"github.com/MuhmdRaouf/huddle/internal/core/providers"
	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// providerWiring builds one provider's hook wiring. Production wires the real adapters over the
// provider's own state root and installed bundle; the scenario tests pin a wiring whose host
// calls answer from fakes, so no daemon spawns and no port is bound.
type providerWiring func(ctx context.Context, provider domain.Provider, env domain.EnvLookup, out ports.Output) app.Deps

// ProvidersSessionStart runs every set-up provider's session-start work in process and answers
// the outputs to merge: the setup re-apply (RoutingHook), the removed-plugin cleanup and the jobs
// line.
func ProvidersSessionStart(ctx context.Context, env domain.EnvLookup) [][]byte {
	return providersSessionStart(ctx, env, wireProviderDeps)
}

// ProvidersSubagentStop runs every set-up provider's usage hook over the payload.
func ProvidersSubagentStop(ctx context.Context, env domain.EnvLookup, payload []byte) [][]byte {
	return providersSubagentStop(ctx, env, payload, wireProviderDeps)
}

// captureOutput collects one provider's hook lines for the merge; an error line is dropped, the
// way a hook's stderr is.
type captureOutput struct{ buf *bytes.Buffer }

func (o *captureOutput) Line(text string)  { o.buf.WriteString(text + "\n") }
func (o *captureOutput) Error(text string) { _, _ = io.WriteString(io.Discard, text) }

// providersSessionStart is ProvidersSessionStart over one wiring.
func providersSessionStart(ctx context.Context, env domain.EnvLookup, wiring providerWiring) [][]byte {
	var outs [][]byte
	for _, name := range providers.Names {
		provider, ok := providers.ByName(name)
		if !ok || !providerSetUp(ctx, provider, env) {
			continue
		}
		out := &captureOutput{buf: &bytes.Buffer{}}
		func() {
			// One provider's panic is that provider's silence, never the session's.
			defer func() { _ = recover() }()
			runProviderSessionStart(ctx, provider, env, wiring, out)
		}()
		if out.buf.Len() > 0 {
			outs = append(outs, out.buf.Bytes())
		}
	}
	return outs
}

// runProviderSessionStart is one provider's session-start work: the wiring the setup hook runs,
// then the line that depends on the plugin still being installed — the setup re-apply and the
// jobs line while it is, the removal when it is not.
func runProviderSessionStart(ctx context.Context, provider domain.Provider, env domain.EnvLookup, wiring providerWiring, out ports.Output) {
	silent := wiring(ctx, provider, env, setupSilent{})
	switch providerPresence(ctx, provider, env) {
	case router.PresencePresent:
		_ = WithEngineHook(ctx, silent, func(ctx context.Context) ExitCode {
			return RoutingHook(ctx, NewSetupDeps(silent))
		})
		board := silent
		board.Out = out
		_, _ = invoke(ctx, BoardCommand{}, []string{"--hook"}, board, hookCwd())
	case router.PresenceAbsent, router.PresenceDisabled:
		_ = RemoveRouting(ctx, NewSetupDeps(silent))
	}
}

// providersSubagentStop is ProvidersSubagentStop over one wiring.
func providersSubagentStop(ctx context.Context, env domain.EnvLookup, payload []byte, wiring providerWiring) [][]byte {
	var outs [][]byte
	for _, name := range providers.Names {
		provider, ok := providers.ByName(name)
		if !ok || !providerSetUp(ctx, provider, env) {
			continue
		}
		if providerPresence(ctx, provider, env) != router.PresencePresent {
			continue
		}
		out := &captureOutput{buf: &bytes.Buffer{}}
		func() {
			defer func() { _ = recover() }()
			_ = AgentStopHook(ctx, wiring(ctx, provider, env, out), payload)
		}()
		if out.buf.Len() > 0 {
			outs = append(outs, out.buf.Bytes())
		}
	}
	return outs
}

// providerSetUp is whether the provider was ever set up on this machine: recorded in
// providers.json, or its setup-done mark exists (a setup from before providers.json).
func providerSetUp(ctx context.Context, provider domain.Provider, env domain.EnvLookup) bool {
	if enabled, err := router.ReadEnabled(env); err == nil && slices.Contains(enabled, provider.Name) {
		return true
	}
	return ReadSetupDone(adapters.StateRoot(ctx, provider, env))
}

// providerPresence is whether the provider's plugin is still installed and enabled, read the way
// the router's uninstall watch reads it: the ledger's uninstall marker beside Claude Code's
// installed_plugins.json and the scopes' enabledPlugins.
func providerPresence(ctx context.Context, provider domain.Provider, env domain.EnvLookup) router.Presence {
	layout := domain.NewStateLayout(adapters.StateRoot(ctx, provider, env))
	ledger, ok := router.ReadLedger(ctx, layout.Ledger)
	if !ok {
		ledger, ok = router.ReadLedger(ctx, layout.DisabledLedger)
	}
	var marker *string
	if ok {
		marker = ledger.Marker
	}
	return router.PluginPresence(provider.Name, adapters.ClaudeConfigDir(env), marker, nil)
}

// wireProviderDeps builds the wiring `huddle provider <p> cli setup --hook` builds, over the
// provider's own state root, with the bundle inside the provider plugin's installed dir — not
// CLAUDE_PLUGIN_ROOT, which in huddle's hook names huddle's own plugin.
func wireProviderDeps(ctx context.Context, provider domain.Provider, env domain.EnvLookup, out ports.Output) app.Deps {
	executable := ""
	if self, err := os.Executable(); err == nil {
		executable = self
	}
	return Wire(ctx, WireOptions{
		Provider:   provider,
		Env:        env,
		Executable: executable,
		BundlePath: providerBundle(provider, env),
		Out:        out,
	})
}

// providerBundle is the provider's CLI bundle inside its installed plugin: <install>/dist/<name>.js,
// the agents sitting beside the dist dir. "" when the plugin's install path is unknown.
func providerBundle(provider domain.Provider, env domain.EnvLookup) string {
	install := router.PluginInstallPath(provider.Name, adapters.ClaudeConfigDir(env), nil)
	if install == "" {
		return ""
	}
	return filepath.Join(install, "dist", provider.Name+".js")
}

// hookCwd is the working directory the merged hook runs in: the session's, the board scope's
// repository.
func hookCwd() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return dir
}
