package outside

// The certificate issuer's pieces that decide without a network: the provider table, the two
// refusals, the account key a renewal answers to, and the token that lives only inside the
// provider it constructed.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDNSProviderKnownSplitsTheNamesItCanBuild(t *testing.T) {
	// a provider lego switches on builds, even without its token
	if !DNSProviderKnown("cloudflare") {
		t.Error("DNSProviderKnown(cloudflare) = false, want a provider lego builds")
	}
	// a name that is not one — caps, spaces, separators — is refused before any build
	if DNSProviderKnown("Cloud_Token") || DNSProviderKnown("has space") || DNSProviderKnown("") {
		t.Error("DNSProviderKnown accepted a name that is not a provider name")
	}
}

func TestNewACMERefusesTheUnknownAndTheUnsupported(t *testing.T) {
	if _, err := NewACME("Route_Secrets", "tok", ""); !errors.As(err, &UnknownProviderError{}) {
		t.Errorf("NewACME of a name that is not one = %v, want UnknownProviderError", err)
	}
	var unsupported UnsupportedProviderError
	// a provider lego builds but whose credentials do not shape into one token
	for _, name := range []string{"route53"} {
		if _, err := NewACME(name, "tok", ""); !errors.As(err, &unsupported) {
			t.Errorf("NewACME(%s) = %v, want UnsupportedProviderError", name, err)
		}
	}
	if err := (UnsupportedProviderError{Provider: "route53"}).Error(); !strings.Contains(err, "bunny, cloudflare") || !strings.Contains(err, "route53") {
		t.Errorf("the refusal = %q, want the provider and the one-token list", err)
	}
	acme, err := NewACME("bunny", "tok", "")
	if err != nil {
		t.Fatalf("NewACME(bunny): %v", err)
	}
	if acme == nil {
		t.Fatal("NewACME(bunny) answered nil")
	}
}

func TestOneTokenProvidersListsEveryNameSorted(t *testing.T) {
	names := oneTokenProviders()
	if len(names) != len(tokenEnv) {
		t.Fatalf("oneTokenProviders = %v, want %d names", names, len(tokenEnv))
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("oneTokenProviders = %v, want it sorted", names)
		}
	}
}

func TestTheIssuerErrorNamesNameTheProviderAndTheList(t *testing.T) {
	if got := (UnknownProviderError{Provider: "mystery"}).Error(); got != `unknown DNS provider "mystery"` {
		t.Errorf("UnknownProviderError = %q", got)
	}
	got := (UnsupportedProviderError{Provider: "route53"}).Error()
	if !strings.Contains(got, `the DNS provider "route53" needs more than one credential`) {
		t.Errorf("UnsupportedProviderError = %q", got)
	}
}

func TestTheACMEAccountKeyIsKeptAndAnsweredOnRenewals(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tls", "acme-account.key")
	a := &legoACME{provider: "bunny", tokenEnv: "BUNNY_API_KEY", accountKey: path}
	first, err := a.account()
	if err != nil {
		t.Fatalf("the first account: %v", err)
	}
	if first == nil {
		t.Fatal("the first account answered no key")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the account key was not kept: %v", err)
	}
	info, err := os.Stat(path)
	if err == nil && info.Mode().Perm() != 0o600 {
		t.Errorf("the account key is %v, want 0600", info.Mode().Perm())
	}
	second, err := a.account()
	if err != nil {
		t.Fatalf("the second account: %v", err)
	}
	if got, ok := second.(interface{ Equal(interface{}) bool }); ok {
		if !got.Equal(first) {
			t.Error("the second account answered a different key than the one kept")
		}
	}
	// a file that is not a key is regenerated, not obeyed
	if err := os.WriteFile(path, []byte("not a pem key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	third, err := a.account()
	if err != nil {
		t.Fatalf("the account over a corrupt file: %v", err)
	}
	if third == nil {
		t.Error("the account over a corrupt file answered no key")
	}
}

func TestTheACMEAccountRefusesAPlaceItCannotWrite(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &legoACME{provider: "bunny", tokenEnv: "BUNNY_API_KEY", accountKey: filepath.Join(file, "key.pem")}
	if _, err := a.account(); err == nil {
		t.Error("the account under a file answered nil, want an error")
	}
	// a key file that cannot be rewritten refuses the renewal's fresh key
	locked := t.TempDir()
	key := filepath.Join(locked, "key.pem")
	if err := os.WriteFile(key, []byte("x"), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(key, 0o600) })
	b := &legoACME{provider: "bunny", tokenEnv: "BUNNY_API_KEY", accountKey: key}
	if _, err := b.account(); err == nil {
		t.Error("the account over an unwritable key file answered nil, want an error")
	}
}

func TestTheChallengeHandsTheTokenOverAndPutsTheEnvironmentBack(t *testing.T) {
	t.Setenv("BUNNY_API_KEY", "")
	a := &legoACME{provider: "bunny", token: "sekrit-token", tokenEnv: "BUNNY_API_KEY"}
	provider, err := a.challenge()
	if err != nil {
		t.Fatalf("challenge: %v", err)
	}
	if provider == nil {
		t.Fatal("challenge answered no provider")
	}
	if value, _ := os.LookupEnv("BUNNY_API_KEY"); value != "" {
		t.Errorf("the token stayed in the environment as %q, want it put back", value)
	}
	// an environment that held a value gets exactly that value back
	t.Setenv("BUNNY_API_KEY", "previous")
	if _, err := a.challenge(); err != nil {
		t.Fatalf("challenge: %v", err)
	}
	if value, _ := os.LookupEnv("BUNNY_API_KEY"); value != "previous" {
		t.Errorf("the environment kept %q, want the previous value", value)
	}
	// an unknown provider refuses with its name
	b := &legoACME{provider: "no-such-provider", token: "x", tokenEnv: "BUNNY_API_KEY"}
	if _, err := b.challenge(); err == nil {
		t.Error("challenge of an unknown provider answered nil, want an error")
	}
}
