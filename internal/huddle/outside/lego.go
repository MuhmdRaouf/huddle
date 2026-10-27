package outside

// The certificate issuer: Let's Encrypt over DNS-01 through lego, the DNS provider
// chosen by name. The provider's token is handed to it in-process — the one environment
// variable the provider reads is set for its construction alone and put back after, so the token
// never reaches the user's shell, the log or another child process. The ACME account key is
// kept beside the certificate it obtains, so renewals answer to the account the first obtain
// opened.

import (
	"context"
	"crypto"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/dns/bunny"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/providers/dns/desec"
	"github.com/go-acme/lego/v4/providers/dns/digitalocean"
	"github.com/go-acme/lego/v4/providers/dns/dnsimple"
	"github.com/go-acme/lego/v4/providers/dns/dreamhost"
	"github.com/go-acme/lego/v4/providers/dns/gandi"
	"github.com/go-acme/lego/v4/providers/dns/linode"
	"github.com/go-acme/lego/v4/providers/dns/ns1"
	"github.com/go-acme/lego/v4/providers/dns/vultr"
	"github.com/go-acme/lego/v4/registration"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// providerShape is a DNS provider's name as lego and the keystore's accounts spell it: the
// lowercase words the issuer switches on, never a path piece or a quote.
var providerShape = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// tokenEnv names the one environment variable each supported DNS provider reads its token from.
// A provider lego knows but this table does not needs more than one credential, and is refused
// with the names that work.
var tokenEnv = map[string]string{
	"bunny":        "BUNNY_API_KEY",
	"cloudflare":   "CLOUDFLARE_DNS_API_TOKEN",
	"desec":        "DESEC_TOKEN",
	"digitalocean": "DO_AUTH_TOKEN",
	"dnsimple":     "DNSIMPLE_OAUTH_TOKEN",
	"dreamhost":    "DREAMHOST_API_KEY",
	"gandi":        "GANDI_API_KEY",
	"linode":       "LINODE_TOKEN",
	"ns1":          "NS1_API_KEY",
	"vultr":        "VULTR_API_KEY",
}

// providerBuilders constructs each provider in tokenEnv, which reads its token from the environment.
var providerBuilders = map[string]func() (challenge.Provider, error){
	"bunny":        func() (challenge.Provider, error) { return bunny.NewDNSProvider() },
	"cloudflare":   func() (challenge.Provider, error) { return cloudflare.NewDNSProvider() },
	"desec":        func() (challenge.Provider, error) { return desec.NewDNSProvider() },
	"digitalocean": func() (challenge.Provider, error) { return digitalocean.NewDNSProvider() },
	"dnsimple":     func() (challenge.Provider, error) { return dnsimple.NewDNSProvider() },
	"dreamhost":    func() (challenge.Provider, error) { return dreamhost.NewDNSProvider() },
	"gandi":        func() (challenge.Provider, error) { return gandi.NewDNSProvider() },
	"linode":       func() (challenge.Provider, error) { return linode.NewDNSProvider() },
	"ns1":          func() (challenge.Provider, error) { return ns1.NewDNSProvider() },
	"vultr":        func() (challenge.Provider, error) { return vultr.NewDNSProvider() },
}

// oneTokenProviders lists the supported providers in order, for the message that names them.
func oneTokenProviders() []string {
	names := make([]string, 0, len(tokenEnv))
	for name := range tokenEnv {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// UnknownProviderError names a DNS provider no issuer knows: a usage error, refused before any
// token is asked for.
type UnknownProviderError struct{ Provider string }

func (e UnknownProviderError) Error() string {
	return fmt.Sprintf("unknown DNS provider %q", e.Provider)
}

// UnsupportedProviderError names a DNS provider whose credentials do not shape into one token.
type UnsupportedProviderError struct{ Provider string }

func (e UnsupportedProviderError) Error() string {
	return fmt.Sprintf("the DNS provider %q needs more than one credential; one token works with: %s",
		e.Provider, strings.Join(oneTokenProviders(), ", "))
}

// DNSProviderKnown answers whether lego knows the DNS provider name.
func DNSProviderKnown(name string) bool {
	if !providerShape.MatchString(name) {
		return false
	}
	i := sort.SearchStrings(legoProviderNames, name)
	return i < len(legoProviderNames) && legoProviderNames[i] == name
}

// NewACME answers the issuer for one DNS provider and its token. accountKey is where the ACME
// account's key is kept ("" holds it in memory for this process alone), so the renewal loop
// answers to the account the first obtain opened.
func NewACME(provider, token, accountKey string) (home.ACME, error) {
	if !providerShape.MatchString(provider) || !DNSProviderKnown(provider) {
		return nil, UnknownProviderError{Provider: provider}
	}
	env, ok := tokenEnv[provider]
	if !ok {
		return nil, UnsupportedProviderError{Provider: provider}
	}
	return &legoACME{provider: provider, token: token, tokenEnv: env, accountKey: accountKey}, nil
}

// legoACME is one provider's issuer: its token and where the ACME account's key is kept.
type legoACME struct {
	provider   string
	token      string
	tokenEnv   string
	accountKey string
}

// Obtain answers one bundled certificate for host with its private key. lego takes no context:
// its own HTTP timeouts bound the exchange, and a server that stops while it runs finishes this
// one obtain and never starts another.
func (a *legoACME) Obtain(_ context.Context, host string) ([]byte, []byte, error) {
	key, err := a.account()
	if err != nil {
		return nil, nil, err
	}
	user := &acmeUser{key: key}
	client, err := lego.NewClient(lego.NewConfig(user))
	if err != nil {
		return nil, nil, err
	}
	provider, err := a.challenge()
	if err != nil {
		return nil, nil, err
	}
	if err := client.Challenge.SetDNS01Provider(provider); err != nil {
		return nil, nil, err
	}
	// the account is resolved from the key: the first obtain opens it, the renewals answer to it
	registrationResource, err := client.Registration.ResolveAccountByKey()
	if err != nil {
		return nil, nil, err
	}
	user.registration = registrationResource
	certs, err := client.Certificate.Obtain(certificate.ObtainRequest{Domains: []string{host}, Bundle: true})
	if err != nil {
		return nil, nil, err
	}
	return certs.Certificate, certs.PrivateKey, nil
}

// challenge constructs the DNS provider with the token in the one environment variable the
// provider reads, for the construction alone: the variable is put back (or removed) as it was,
// so the token lives only inside the provider that holds it now.
func (a *legoACME) challenge() (challenge.Provider, error) {
	build, ok := providerBuilders[a.provider]
	if !ok {
		return nil, UnknownProviderError{Provider: a.provider}
	}
	previous, had := os.LookupEnv(a.tokenEnv)
	_ = os.Setenv(a.tokenEnv, a.token)
	provider, err := build()
	if had {
		_ = os.Setenv(a.tokenEnv, previous)
	} else {
		_ = os.Unsetenv(a.tokenEnv)
	}
	return provider, err
}

// account answers the ACME account's private key: loaded from where it is kept, or generated
// and kept there for the next obtain.
func (a *legoACME) account() (crypto.PrivateKey, error) {
	if a.accountKey != "" {
		if raw, err := os.ReadFile(a.accountKey); err == nil {
			if key, err := certcrypto.ParsePEMPrivateKey(raw); err == nil {
				return key, nil
			}
		}
	}
	key, err := certcrypto.GeneratePrivateKey(certcrypto.EC256)
	if err != nil {
		return nil, err
	}
	if a.accountKey != "" {
		if err := os.MkdirAll(filepath.Dir(a.accountKey), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(a.accountKey, certcrypto.PEMEncode(key), 0o600); err != nil {
			return nil, err
		}
	}
	return key, nil
}

// acmeUser is the account lego talks to Let's Encrypt as: no email, the key is the identity.
type acmeUser struct {
	key          crypto.PrivateKey
	registration *registration.Resource
}

func (u *acmeUser) GetEmail() string                        { return "" }
func (u *acmeUser) GetRegistration() *registration.Resource { return u.registration }
func (u *acmeUser) GetPrivateKey() crypto.PrivateKey        { return u.key }
