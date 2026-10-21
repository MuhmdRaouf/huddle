// The router's certificate authority: it lets a provider router stand in for api.anthropic.com
// when Claude Code reaches it through HTTPS_PROXY. Claude Code trusts this CA only through
// NODE_EXTRA_CA_CERTS in its settings; it is never added to a system trust store.
//
// The CA carries a critical name constraint for api.anthropic.com, so even a leaked key cannot sign a
// certificate any client accepts for another host.
package pki

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// RouterHost is the one name the router CA signs for.
const RouterHost = "api.anthropic.com"

const (
	routerCAKeyFile  = "ca.key"
	routerCACertFile = "ca.pem"
	routerCACALife   = 10 * 365 * 24 * time.Hour
	routerCALeafLife = 7 * 24 * time.Hour
	// A CA this close to expiry is replaced, so no leaf outlives it.
	routerCARenewBefore = routerCALeafLife + 24*time.Hour
)

// RouterCA is a loaded certificate authority.
type RouterCA struct {
	cert     *x509.Certificate
	certPEM  []byte
	key      *ecdsa.PrivateKey
	certPath string
}

// RouterCAInsecureError means the CA's files can be read or replaced by another user; the CA is not used.
type RouterCAInsecureError struct {
	Path   string
	Reason string
}

func (e *RouterCAInsecureError) Error() string {
	return fmt.Sprintf("local CA %s: %s", e.Path, e.Reason)
}

// RouterCAInvalidError means ca.key does not hold a CA this package made.
type RouterCAInvalidError struct {
	Path   string
	Reason string
}

func (e *RouterCAInvalidError) Error() string {
	return fmt.Sprintf("local CA %s: %s", e.Path, e.Reason)
}

// LoadOrCreateRouterCA returns the CA kept in dir, creating it on first use. Five routers may call it at once: the
// first to link its ca.key wins and the others load that one.
func LoadOrCreateRouterCA(ctx context.Context, dir string, now time.Time) (*RouterCA, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := checkOwnedPrivate(dir, 0o022); err != nil {
		return nil, err
	}
	keyPath := filepath.Join(dir, routerCAKeyFile)
	ca, err := routerCALoad(keyPath)
	if errors.Is(err, fs.ErrNotExist) {
		ca, err = routerCACreate(dir, keyPath, now)
	}
	if err != nil {
		return nil, err
	}
	if ca.cert.NotAfter.Sub(now) < routerCARenewBefore {
		if ca, err = routerCAReplace(dir, keyPath, now); err != nil {
			return nil, err
		}
	}
	ca.certPath = filepath.Join(dir, routerCACertFile)
	if err := ensureCertFile(dir, ca); err != nil {
		return nil, err
	}
	return ca, nil
}

// CertPath is the PEM file NODE_EXTRA_CA_CERTS names.
func (c *RouterCA) CertPath() string { return c.certPath }

// CACertPath is the PEM file's path in dir, whether or not a CA exists there: the path the
// proxy keys name, answerable without loading (and so creating) the CA.
func CACertPath(dir string) string { return filepath.Join(dir, routerCACertFile) }

// CertPEM is the CA certificate in PEM form.
func (c *RouterCA) CertPEM() []byte { return c.certPEM }

// Certificate is the parsed CA certificate.
func (c *RouterCA) Certificate() *x509.Certificate { return c.cert }

// Leaf signs a short-lived server certificate for host, which must be RouterHost.
func (c *RouterCA) Leaf(host string, now time.Time) (*tls.Certificate, error) {
	if host != RouterHost {
		return nil, fmt.Errorf("local CA signs only %s, not %q", RouterHost, host)
	}
	key := routerCAMust(ecdsa.GenerateKey(elliptic.P256(), rand.Reader))
	notAfter := now.Add(routerCALeafLife)
	if notAfter.After(c.cert.NotAfter) {
		notAfter = c.cert.NotAfter
	}
	template := &x509.Certificate{
		SerialNumber: randomSerial(rand.Reader),
		Subject:      pkix.Name{CommonName: RouterHost},
		DNSNames:     []string{RouterHost},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der := routerCAMust(x509.CreateCertificate(rand.Reader, template, c.cert, &key.PublicKey, c.key))
	leaf := routerCAMust(x509.ParseCertificate(der))
	return &tls.Certificate{Certificate: [][]byte{der, c.cert.Raw}, PrivateKey: key, Leaf: leaf}, nil
}

// routerCALoad reads ca.key: the PEM private key followed by the CA certificate.
func routerCALoad(keyPath string) (*RouterCA, error) {
	if err := checkOwnedPrivate(keyPath, 0o077); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	invalid := func(reason string) error { return &RouterCAInvalidError{Path: keyPath, Reason: reason} }
	keyBlock, rest := pem.Decode(data)
	if keyBlock == nil || keyBlock.Type != "EC PRIVATE KEY" {
		return nil, invalid("no EC private key")
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, invalid(err.Error())
	}
	cert, err := parseCertificate(rest)
	if err != nil {
		return nil, invalid(err.Error())
	}
	if !cert.IsCA || !cert.PermittedDNSDomainsCritical || len(cert.PermittedDNSDomains) != 1 ||
		cert.PermittedDNSDomains[0] != RouterHost {
		return nil, invalid("not a CA constrained to " + RouterHost)
	}
	if !key.PublicKey.Equal(cert.PublicKey) {
		return nil, invalid("the key does not match the certificate")
	}
	certBlock, _ := pem.Decode(rest)
	return &RouterCA{cert: cert, key: key, certPEM: pem.EncodeToMemory(certBlock)}, nil
}

func routerCACreate(dir, keyPath string, now time.Time) (*RouterCA, error) {
	tmp, err := writeTemp(dir, routerCAGenerate(now), 0o600)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp)
	// A hard link never replaces an existing file, so exactly one concurrent creator wins.
	if err := os.Link(tmp, keyPath); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	return routerCALoad(keyPath)
}

// routerCAReplace swaps an expiring CA for a new one. Claude Code sessions started before the swap keep trusting the
// old certificate until they restart, so this happens only near the end of a ten-year life.
func routerCAReplace(dir, keyPath string, now time.Time) (*RouterCA, error) {
	tmp, err := writeTemp(dir, routerCAGenerate(now), 0o600)
	if err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, keyPath); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	return routerCALoad(keyPath)
}

// routerCAGenerate makes a new CA key and certificate. P-256 key generation, self-signing a well-formed template and
// marshalling a P-256 key cannot fail, so a failure there is a broken runtime and panics.
func routerCAGenerate(now time.Time) []byte {
	key := routerCAMust(ecdsa.GenerateKey(elliptic.P256(), rand.Reader))
	template := &x509.Certificate{
		SerialNumber:                randomSerial(rand.Reader),
		Subject:                     pkix.Name{CommonName: "cc-plugins local CA for " + RouterHost},
		NotBefore:                   now.Add(-time.Hour),
		NotAfter:                    now.Add(routerCACALife),
		IsCA:                        true,
		BasicConstraintsValid:       true,
		MaxPathLenZero:              true,
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         []string{RouterHost},
	}
	der := routerCAMust(x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key))
	var out bytes.Buffer
	_ = pem.Encode(&out, &pem.Block{Type: "EC PRIVATE KEY", Bytes: routerCAMust(x509.MarshalECPrivateKey(key))})
	_ = pem.Encode(&out, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	return out.Bytes()
}

// writeTemp writes data to a new temporary file in dir with the given mode and returns its name.
func writeTemp(dir string, data []byte, mode os.FileMode) (string, error) {
	f, err := os.CreateTemp(dir, ".ca-*.tmp")
	if err != nil {
		return "", err
	}
	name := f.Name()
	err = f.Chmod(mode)
	if err == nil {
		_, err = f.Write(data)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

// ensureCertFile writes ca.pem from the key file when it is missing or differs.
func ensureCertFile(dir string, ca *RouterCA) error {
	if current, err := os.ReadFile(ca.certPath); err == nil && bytes.Equal(current, ca.certPEM) {
		return nil
	}
	tmp, err := writeTemp(dir, ca.certPEM, 0o644)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, ca.certPath); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// checkOwnedPrivate refuses a path another user owns or whose mode has any of the forbidden bits.
func checkOwnedPrivate(path string, forbidden os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return &RouterCAInsecureError{Path: path, Reason: "is a symbolic link"}
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return &RouterCAInsecureError{Path: path, Reason: "owned by another user"}
	}
	if info.Mode().Perm()&forbidden != 0 {
		return &RouterCAInsecureError{Path: path, Reason: fmt.Sprintf("mode %o lets other users in", info.Mode().Perm())}
	}
	return nil
}

func routerCAMust[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
