package outside

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/go-acme/lego/v4/providers/dns"
)

func TestLegoProviderNamesAreTheNamesLegoBuilds(t *testing.T) {
	if !sort.StringsAreSorted(legoProviderNames) {
		t.Fatal("legoProviderNames is not sorted; DNSProviderKnown searches it")
	}
	for _, name := range legoProviderNames {
		if _, err := dns.NewDNSChallengeProviderByName(name); err != nil &&
			strings.Contains(strings.ToLower(err.Error()), "unrecognized dns provider") {
			t.Errorf("lego does not know %q, which legoProviderNames lists", name)
		}
	}
}

func TestEveryOneTokenProviderIsBuiltAsLegoBuildsIt(t *testing.T) {
	if len(providerBuilders) != len(tokenEnv) {
		t.Fatalf("providerBuilders has %d providers, tokenEnv %d", len(providerBuilders), len(tokenEnv))
	}
	for name, env := range tokenEnv {
		build, ok := providerBuilders[name]
		if !ok {
			t.Errorf("tokenEnv names %q, which providerBuilders cannot build", name)
			continue
		}
		if !DNSProviderKnown(name) {
			t.Errorf("DNSProviderKnown(%q) = false for a supported provider", name)
		}
		t.Setenv(env, "test-token")
		ours, ourErr := build()
		theirs, theirErr := dns.NewDNSChallengeProviderByName(name)
		if (ourErr == nil) != (theirErr == nil) || fmt.Sprintf("%T", ours) != fmt.Sprintf("%T", theirs) {
			t.Errorf("%s: built %T (%v), lego builds %T (%v)", name, ours, ourErr, theirs, theirErr)
		}
	}
}
