// The claim suite: one answer to "is this our model?" — the resolved provider's
// claim (env overrides included), shared by the worker's router and the lane picks. An
// overridden id outside the usual prefix is the provider's everywhere; a foreign id is nobody's.
package router

import (
	"reflect"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// claimOverride is zai's main model overridden to an id no prefix of it covers.
const claimOverride = "acme-coder-9"

const claimForeign = "kimi-k3"

func claimEnv(extra domain.EnvLookup) domain.EnvLookup {
	env := domain.EnvLookup{"ZAI_MODEL_MAIN": claimOverride}
	for name, value := range extra {
		env[name] = value
	}
	return env
}

func TestClaims(t *testing.T) {
	t.Parallel()
	t.Run("covers the resolved catalog ids and the prefixes, nothing else, and never the empty id", func(t *testing.T) {
		resolved := domain.ResolveProvider(routerProvider, claimEnv(nil))
		claim := resolved.ModelClaim()
		want := domain.ModelClaim{IDs: []string{claimOverride, "glm-5.3-flash"}, Prefixes: []string{"glm-"}}
		if !reflect.DeepEqual(claim, want) {
			t.Fatalf("claim = %+v, want %+v", claim, want)
		}
		for _, check := range []struct {
			model string
			want  bool
		}{
			{model: claimOverride, want: true},
			{model: "glm-anything", want: true},
			{model: claimForeign, want: false},
			{model: "claude-sonnet-5-5", want: false},
			{model: "", want: false},
		} {
			if got := domain.Claims(claim, check.model); got != check.want {
				t.Fatalf("claims(%q) = %v, want %v", check.model, got, check.want)
			}
		}
		// The shipped catalog does not claim the override: only the resolved provider does.
		if domain.Claims(routerProvider.ModelClaim(), claimOverride) {
			t.Fatal("the unresolved provider claims the overridden id")
		}
	})
}
