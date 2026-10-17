package cli

// `huddle cert` (SPEC 7.2): the terminal word that obtains the hub's certificate over DNS-01
// from Let's Encrypt and lays it in the home's tls/, where the hub listener serves it. The DNS
// provider is chosen by name, its token is asked for with the terminal's echo off (never in a
// chat, never printed) and kept in the keystore under `dns:<provider>`, and the server renews
// the certificate 30 days before expiry on its own. A machine whose config names no hub host
// has nothing to certify, so that shape is refused before anything is typed.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/outside"
)

// certUsage is the one line every wrong shape answers.
const certUsage = "usage: huddle cert --provider <name>   (the DNS provider that answers the DNS-01 challenge, as cloudflare or digitalocean)"

// Cert obtains the hub's certificate over DNS-01 and stores the provider's token. Exit: 0 the
// certificate is in tls/, 1 the issuer refused, 2 a wrong line or a machine with no hub name.
func Cert(ctx context.Context, w *World, argv []string, stdout, stderr io.Writer) int {
	root := Home(ctx, w)
	if root == "" {
		fmt.Fprintln(stderr, "huddle: no home: set HOME or HUDDLE_HOME")
		return 1
	}
	opt, pos := parseFlags(argv)
	if len(pos) > 0 && pos[0] == "cert" { // the whole line reaches here, word and all
		pos = pos[1:]
	}
	if len(pos) != 0 {
		fmt.Fprintln(stderr, certUsage)
		return 2
	}
	provider, hasProvider := opt.text("provider")
	if !hasProvider {
		fmt.Fprintln(stderr, certUsage)
		return 2
	}
	if !w.dnsKnown(provider) {
		fmt.Fprintf(stderr, "huddle: unknown DNS provider %q — the issuer knows its providers by name, as cloudflare or digitalocean\n", provider)
		return 2
	}
	h := home.Home{Root: root}
	cfg, err := home.LoadConfig(h)
	if err != nil {
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	if cfg.Hub.Host == "" {
		fmt.Fprintln(stderr, "huddle: no hub name to certify — huddle role hub --listen <addr:port> --host <name> writes it first")
		return 2
	}
	secrets := w.secrets(ctx, h)
	account := "dns:" + provider
	token, held := secrets.Get(ctx, account)
	if !held {
		fmt.Fprintf(stdout, "token for %s (typed unseen, kept in the keystore): ", provider)
		typed, err := outside.ReadSecret(w.stdin())
		fmt.Fprintln(stdout)
		if err != nil && typed == "" {
			fmt.Fprintln(stderr, certUsage)
			return 2
		}
		if typed == "" {
			fmt.Fprintln(stderr, "huddle: the token is empty")
			return 2
		}
		token = typed
		if err := secrets.Set(ctx, account, token); err != nil {
			fmt.Fprintf(stderr, "huddle: %v\n", err)
			return 1
		}
	}
	acme, err := w.certIssue(provider, token, filepath.Join(h.TLS(), "acme-account.key"))
	if err != nil {
		var unknown outside.UnknownProviderError
		var unsupported outside.UnsupportedProviderError
		switch {
		case errors.As(err, &unknown), errors.As(err, &unsupported):
			fmt.Fprintf(stderr, "huddle: %v\n", err)
			return 2
		}
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	expiry, err := home.IssueCertificate(ctx, h, acme, cfg.Hub.Host)
	if err != nil {
		fmt.Fprintf(stderr, "huddle: obtaining the certificate for %s failed: %v\n", cfg.Hub.Host, err)
		return 1
	}
	if cfg.Cert.DNSProvider != provider {
		cfg.Cert.DNSProvider = provider
		if err := home.SaveConfig(h, cfg); err != nil {
			fmt.Fprintf(stderr, "huddle: %v\n", err)
			return 1
		}
	}
	fmt.Fprintf(stdout, "certificate for %s in %s, expires %s\n", cfg.Hub.Host, h.TLS(), expiry.Format(time.DateOnly))
	fmt.Fprintln(stdout, "the hub listener serves it (huddle up, or the service); the server renews it 30 days before expiry")
	return 0
}
