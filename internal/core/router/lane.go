// One provider's lane as the router processes build it: the endpoint overrides the environment
// names, the spool, the budget gate, the proxy environment and the keystore lookup. The per-
// provider worker and the huddle-router daemon both start from here, so a lane is built one way.
package router

import (
	"context"
	"net/url"

	"github.com/MuhmdRaouf/huddle/internal/adapters"
	"github.com/MuhmdRaouf/huddle/internal/adapters/keystore"
	"github.com/MuhmdRaouf/huddle/internal/adapters/spool"
	"github.com/MuhmdRaouf/huddle/internal/core/ports"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// envOrDefault is `env[name] ?? fallback`
func envOrDefault(env domain.EnvLookup, name, fallback string) string {
	if value, ok := env[name]; ok && value != "" {
		return value
	}
	return fallback
}

// workerKey is the provider key the way setup reads it (env, OS store, key file), never logged.
func workerKey(ctx context.Context, provider domain.Provider, env domain.EnvLookup) (ports.ProviderKey, *ports.WorkerError) {
	store := keystore.PlatformKeyStore(provider, env, keystore.PlatformOptions{})
	return adapters.LoadKey(ctx, provider, env, provider.KeyFile, store)
}

// BadURL is a lane's unusable endpoint override: the env variable that named it and the parse
// error, so each caller logs the failure in its own words.
type BadURL struct {
	// Name is the env variable whose value did not parse.
	Name string
	Err  error
}

func (e *BadURL) Error() string { return e.Name + ": " + e.Err.Error() }

func (e *BadURL) Unwrap() error { return e.Err }

// LaneOf is one provider's lane options as the router process builds them: endpoint overrides,
// spool, budget gate, proxy environment, the keystore lookup and providers.json's fallback list.
// The multi-level plugs (Port, Token, Peers, the Anthropic URL every lane shares) are the
// caller's to set.
func LaneOf(provider domain.Provider, env domain.EnvLookup, log func(string)) (RouterOptions, error) {
	resolved := domain.ResolveProvider(provider, env)
	anthropicKnob := domain.RouterEnvName(resolved, domain.RouterEnvKnobAnthropicURL)
	anthropic, err := url.Parse(envOrDefault(env, anthropicKnob, "https://api.anthropic.com"))
	if err != nil {
		return RouterOptions{}, &BadURL{Name: anthropicKnob, Err: err}
	}
	urlKnob := domain.RouterEnvName(resolved, domain.RouterEnvKnobURL)
	providerURL, err := url.Parse(envOrDefault(env, urlKnob, resolved.BaseURL))
	if err != nil {
		return RouterOptions{}, &BadURL{Name: urlKnob, Err: err}
	}
	return RouterOptions{
		Provider:    resolved,
		Anthropic:   anthropic,
		ProviderURL: providerURL,
		Log:         log,
		Events:      spool.CreateSpoolWriter(env, log, nil),
		Budget:      CreateBudgetGate(resolved.Name, env, nil, 0),
		Env:         ProxyEnv(env),
		KeyLookup: func(ctx context.Context) (ports.ProviderKey, *ports.WorkerError) {
			return workerKey(ctx, resolved, env)
		},
		Fallback: ReadFallback(env, resolved.Name),
	}, nil
}
