package cli

// `huddle cert` (SPEC 7.2): the terminal word that obtains the hub's certificate over DNS-01
// from Let's Encrypt and lays it in the home's tls/, where the hub listener serves it. The DNS
// provider is chosen by name, its token is asked for with the terminal's echo off (never in a
// chat, never printed) and kept in the keystore under `dns:<provider>`, and the server renews
// the certificate 30 days before expiry on its own. `--import` instead installs a pair the owner
// already holds (mkcert's, a company CA's) for the hub's own name: no issuer stands behind it,
// so it is never renewed — the log says when it runs out. With no flag at all the word says
// which certificate is in use: the imported pair, the ACME one, or the local CA's leaf, with
// its names, its expiry and, for the local CA, whether this machine trusts the root. A machine
// whose config names no hub host has nothing to certify, so that shape is refused before
// anything is typed.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/outside"
	"github.com/MuhmdRaouf/huddle/internal/pki"
)

// certUsage is the lines every wrong shape answers.
const certUsage = "usage: huddle cert   (the certificate in use: where from, for which names, until when)\n" +
	"       huddle cert --provider <name>   (the DNS provider that answers the DNS-01 challenge, as cloudflare or digitalocean)\n" +
	"       huddle cert --import <cert.pem> <key.pem>   (a pair you already hold, as mkcert's or a company CA's)"

// certNoHost is the one line both paths answer on a machine whose config names no hub host.
const certNoHost = "huddle: no hub name to certify — huddle role hub --listen <addr:port> --host <name> writes it first"

// Cert obtains the hub's certificate over DNS-01 and stores the provider's token, installs a
// pair the owner already holds, or — with no flag at all — says which certificate the hub serves:
// the imported pair, the ACME one, or the local CA's leaf, with its names and expiry and, for the
// local CA, whether this machine trusts the root. Exit: 0 the certificate is in tls/ or the
// status is said, 1 the issuer or the pair is refused, 2 a wrong line or a machine with no hub
// name.
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
	if opt.has("import") {
		return certImport(w, root, opt, pos, stdout, stderr)
	}
	if len(pos) != 0 {
		fmt.Fprintln(stderr, certUsage)
		return 2
	}
	provider, hasProvider := opt.text("provider")
	if !hasProvider {
		if opt.has("provider") { // given without its name
			fmt.Fprintln(stderr, certUsage)
			return 2
		}
		return certStatus(w, root, stdout, stderr)
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
		fmt.Fprintln(stderr, certNoHost)
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

// certStatus says which certificate the hub serves and from where it came. The choice is the
// listener's own: an imported pair over ACME over the local CA. Exit: 0 the status is said, 1
// the standing pair cannot be read, 2 a machine with no hub name.
func certStatus(w *World, root string, stdout, stderr io.Writer) int {
	h := home.Home{Root: root}
	cfg, err := home.LoadConfig(h)
	if err != nil {
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	if cfg.Hub.Host == "" {
		fmt.Fprintln(stderr, certNoHost)
		return 2
	}
	_, imported := home.ImportedName(h)
	switch {
	case imported:
		expiry, err := home.CertificateExpiry(h)
		if err != nil {
			fmt.Fprintf(stderr, "huddle: the imported pair in %s cannot be read: %v (huddle cert --import lays a fresh one)\n", h.TLS(), err)
			return 1
		}
		fmt.Fprintf(stdout, "certificate for %s: an imported pair in %s, expires %s\n", cfg.Hub.Host, h.TLS(), expiry.Format(time.DateOnly))
		fmt.Fprintln(stdout, "an imported pair is never renewed — import a fresh one before it expires")
	case cfg.Cert.DNSProvider != "":
		expiry, err := home.CertificateExpiry(h)
		if err != nil {
			fmt.Fprintf(stdout, "no certificate for %s yet — huddle cert --provider %s obtains the first one\n", cfg.Hub.Host, cfg.Cert.DNSProvider)
			return 0
		}
		fmt.Fprintf(stdout, "certificate for %s: %s over DNS-01, expires %s\n", cfg.Hub.Host, cfg.Cert.DNSProvider, expiry.Format(time.DateOnly))
		fmt.Fprintln(stdout, "the server renews it 30 days before expiry")
	default:
		authority := pki.New(h.PKI(), pki.Options{})
		fmt.Fprintf(stdout, "certificate for %s: huddle's local CA — a 12-hour leaf for %s, renewed on its own\n",
			cfg.Hub.Host, strings.Join(pki.HubLeafNames(cfg.Hub.Host), ", "))
		cert, err := authority.ReadRoot()
		if err != nil {
			fmt.Fprintf(stdout, "no root yet: the hub listener creates it on first start, huddle trust creates and installs it now\n")
			return 0
		}
		if pki.TrustedBy(w.systemPool(), cert) {
			fmt.Fprintf(stdout, "the root in %s is trusted by this machine (expires %s)\n", authority.RootFile(), cert.NotAfter.Format(time.DateOnly))
			return 0
		}
		port := ""
		if _, named, err := net.SplitHostPort(cfg.Hub.Listen); err == nil {
			port = named
		}
		address := cfg.Hub.Host
		if port != "" {
			address = net.JoinHostPort(cfg.Hub.Host, port)
		}
		fmt.Fprintf(stdout, "the root in %s is not trusted yet — run huddle trust once so browsers accept https://%s\n", authority.RootFile(), address)
	}
	return 0
}

// certImport installs a pair the owner already holds for the hub's name: the line takes the
// certificate file as --import's value (a flag takes the next word) and the key file as the one
// plain word left. The pair is validated in memory and nothing is written before it passes; the
// key is read and written, never printed. Exit: 0 installed, 1 the pair is refused, 2 a wrong
// line or a machine with no hub name.
func certImport(w *World, root string, opt *flags, pos []string, stdout, stderr io.Writer) int {
	certPath, tookFile := opt.text("import")
	if !tookFile || len(pos) != 1 || opt.has("provider") {
		fmt.Fprintln(stderr, certUsage)
		return 2
	}
	keyPath := pos[0]
	h := home.Home{Root: root}
	cfg, err := home.LoadConfig(h)
	if err != nil {
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	if cfg.Hub.Host == "" {
		fmt.Fprintln(stderr, certNoHost)
		return 2
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		fmt.Fprintf(stderr, "huddle: %s: %v\n", certPath, err)
		return 1
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		fmt.Fprintf(stderr, "huddle: %s: %v\n", keyPath, err)
		return 1
	}
	expiry, err := importExpiry(certPEM, keyPEM, cfg.Hub.Host, w.now())
	if err != nil {
		fmt.Fprintf(stderr, "huddle: importing the certificate for %s failed: %v\n", cfg.Hub.Host, err)
		return 1
	}
	if err := home.ImportCertificate(h, certPEM, keyPEM); err != nil {
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "certificate for %s in %s, expires %s\n", cfg.Hub.Host, h.TLS(), expiry.Format(time.DateOnly))
	fmt.Fprintln(stdout, "an imported certificate is never renewed — the log says once a day from 30 days out; import a fresh one then")
	return 0
}

// importExpiry validates a pair to install and answers the leaf's expiry: the files must parse
// as a tls pair (so the key matches the leaf), the leaf must name host and be live. A chain
// after the leaf passes as it stands.
func importExpiry(certPEM, keyPEM []byte, host string, now time.Time) (time.Time, error) {
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return time.Time{}, fmt.Errorf("the key does not match the certificate: %w", err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return time.Time{}, errors.New("the certificate file holds no certificate")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, err
	}
	if err := leaf.VerifyHostname(host); err != nil {
		return time.Time{}, fmt.Errorf("the certificate does not name %s: %w", host, err)
	}
	if leaf.NotAfter.Before(now) {
		return time.Time{}, fmt.Errorf("the certificate expired %s", leaf.NotAfter.Format(time.DateOnly))
	}
	return leaf.NotAfter, nil
}
