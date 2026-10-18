// Huddle's local CA: the answer for HTTPS on a local name no public certificate authority will
// serve. The hub keeps its own root (10-year life) under the home's pki/, signs a short-lived
// intermediate (7 days) with it, and issues the hub listener's leaf certificates (12 hours) from
// the intermediate — the shape Caddy's internal CA settled on. Each certificate is renewed once
// two thirds of its life is gone. Only this package reads root.key, and the root is never
// replaced: a machine that trusted it once keeps trusting the same key.
package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// The CA's lifetimes, matched to Caddy's: a 10-year root, a 7-day intermediate and a 12-hour
// leaf, each renewed once two thirds of its life is gone.
const (
	RootLife         = 10 * 365 * 24 * time.Hour
	IntermediateLife = 7 * 24 * time.Hour
	LeafLife         = 12 * time.Hour
)

// BrokenCAError says the directory holds CA files that cannot serve: a key that does not parse,
// does not match its certificate, or one half of the pair without the other. The CA is never
// silently replaced behind such a refusal — every trust a machine already granted the standing
// root would stop meaning anything.
type BrokenCAError struct {
	Dir string
	Err error
}

func (e *BrokenCAError) Error() string {
	return fmt.Sprintf("huddle: the local CA in %s cannot serve: %v (restore it or remove the directory)", e.Dir, e.Err)
}

func (e *BrokenCAError) Unwrap() error { return e.Err }

// Options shapes the CA's world; every nil member takes the real one. Tests inject both, so the
// lifetimes read as arithmetic against a movable clock.
type Options struct {
	// Now is the clock the lifetimes and renewal points are computed against.
	Now func() time.Time
	// Rand is the randomness keys and serial numbers are minted from.
	Rand io.Reader
}

func (o Options) clock() func() time.Time {
	if o.Now != nil {
		return o.Now
	}
	return time.Now
}

func (o Options) random() io.Reader {
	if o.Rand != nil {
		return o.Rand
	}
	return rand.Reader
}

// Authority is huddle's local CA in one directory (the home's pki/): a root whose key nothing
// outside this package ever reads, and the intermediate the hub listener's leaves are issued
// from.
type Authority struct {
	dir string
	o   Options
}

// New answers the authority over dir; nothing is written until the first certificate is needed.
func New(dir string, o Options) *Authority {
	return &Authority{dir: dir, o: o}
}

// Dir is the directory the CA lives in.
func (a *Authority) Dir() string { return a.dir }

// RootFile, RootKeyFile, IntermediateFile and IntermediateKeyFile are the CA's four files, laid
// with private modes (0700 for the directory, 0600 for each file).
func (a *Authority) RootFile() string         { return filepath.Join(a.dir, "root.crt") }
func (a *Authority) RootKeyFile() string      { return filepath.Join(a.dir, "root.key") }
func (a *Authority) IntermediateFile() string { return filepath.Join(a.dir, "intermediate.crt") }
func (a *Authority) IntermediateKeyFile() string {
	return filepath.Join(a.dir, "intermediate.key")
}

// Root answers the root certificate, creating the CA when the directory holds none. A root that
// exists is reused whatever its age: its 10-year life outlives releases, and the trust machines
// granted it is the point of keeping it.
func (a *Authority) Root() (*x509.Certificate, error) {
	cert, _, err := a.rootPair()
	return cert, err
}

// rootPair answers the root certificate with the key that signs the intermediate.
func (a *Authority) rootPair() (*x509.Certificate, crypto.Signer, error) {
	cert, key, err := a.loadPair(a.RootFile(), a.RootKeyFile())
	switch {
	case err == nil:
		signer, ok := key.(crypto.Signer)
		if !ok {
			return nil, nil, &BrokenCAError{Dir: a.dir, Err: errors.New("the root key cannot sign")}
		}
		return cert, signer, nil
	case errors.Is(err, os.ErrNotExist):
		return a.mintRoot()
	default:
		return nil, nil, err
	}
}

// ReadRoot answers the root certificate as it stands: nothing is created, so a removal word can
// tell an absent CA from a broken one.
func (a *Authority) ReadRoot() (*x509.Certificate, error) {
	body, err := os.ReadFile(a.RootFile())
	if err != nil {
		return nil, err
	}
	return parseCertificate(body)
}

// mintRoot lays the root certificate and its key, each in one move: a crash between the two
// writes leaves a refusal on the next need, never a silent re-mint of the root.
func (a *Authority) mintRoot() (*x509.Certificate, crypto.Signer, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), a.o.random())
	if err != nil {
		return nil, nil, err
	}
	now := a.o.clock()()
	template := &x509.Certificate{
		SerialNumber:          randomSerial(a.o.random()),
		Subject:               pkix.Name{CommonName: fmt.Sprintf("Huddle Local Authority - %d ECC Root", now.Year())},
		NotBefore:             now,
		NotAfter:              now.Add(RootLife),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1, // the root signs the intermediate and nothing beyond it
	}
	der, err := x509.CreateCertificate(a.o.random(), template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	if err := writePEM(a.RootFile(), "CERTIFICATE", der); err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	if err := writePEM(a.RootKeyFile(), "EC PRIVATE KEY", keyDER); err != nil {
		return nil, nil, err
	}
	cert, err := parseCertificate(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return cert, key, err
}

// Intermediate answers the live intermediate and its key, minting one when none stands and
// re-signing it with the root once two thirds of its life is gone: a short-lived intermediate
// keeps the root's key out of every leaf issuance.
func (a *Authority) Intermediate() (*x509.Certificate, crypto.Signer, error) {
	root, rootKey, err := a.rootPair()
	if err != nil {
		return nil, nil, err
	}
	cert, key, pairErr := a.loadPair(a.IntermediateFile(), a.IntermediateKeyFile())
	signer, canSign := key.(crypto.Signer)
	if pairErr == nil && canSign && cert.CheckSignatureFrom(root) == nil && !RenewDue(cert, a.o.clock()()) {
		return cert, signer, nil
	}
	// a broken or spent pair is re-minted — nothing outside this machine trusts the
	// intermediate directly; anything else (a directory that cannot be read) stops the CA
	if pairErr != nil && !errors.Is(pairErr, os.ErrNotExist) && !isBrokenPair(pairErr) {
		return nil, nil, pairErr
	}
	return a.mintIntermediate(root, rootKey)
}

// isBrokenPair says whether err is the refusal loadPair answers for files that cannot serve.
func isBrokenPair(err error) bool {
	var broken *BrokenCAError
	return errors.As(err, &broken)
}

// mintIntermediate lays a fresh intermediate signed by the root.
func (a *Authority) mintIntermediate(root *x509.Certificate, rootKey crypto.Signer) (*x509.Certificate, crypto.Signer, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), a.o.random())
	if err != nil {
		return nil, nil, err
	}
	now := a.o.clock()()
	template := &x509.Certificate{
		SerialNumber:          randomSerial(a.o.random()),
		Subject:               pkix.Name{CommonName: fmt.Sprintf("Huddle Local Authority - %d ECC Intermediate", now.Year())},
		NotBefore:             now,
		NotAfter:              now.Add(IntermediateLife),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0, // the intermediate signs leaves only
	}
	der, err := x509.CreateCertificate(a.o.random(), template, root, &key.PublicKey, rootKey)
	if err != nil {
		return nil, nil, err
	}
	if err := writePEM(a.IntermediateFile(), "CERTIFICATE", der); err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	if err := writePEM(a.IntermediateKeyFile(), "EC PRIVATE KEY", keyDER); err != nil {
		return nil, nil, err
	}
	cert, err := parseCertificate(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return cert, key, err
}

// RenewDue answers whether a certificate is past its renewal point: less than a third of its own
// life is left.
func RenewDue(cert *x509.Certificate, now time.Time) bool {
	life := cert.NotAfter.Sub(cert.NotBefore)
	return life > 0 && cert.NotAfter.Sub(now) < life/3
}

// HubLeafNames answers the names the hub listener's own leaf carries: the hub's name and the
// loopback spellings, so the hub answers by name and as localhost alike.
func HubLeafNames(host string) []string {
	names := []string{}
	if host != "" && host != "localhost" {
		names = append(names, host)
	}
	return append(names, "localhost", "127.0.0.1", "::1")
}

// splitNames sorts the leaf's names into DNS names and addresses the way a certificate carries
// them.
func splitNames(names []string) (dns []string, ips []net.IP) {
	for _, name := range names {
		if ip := net.ParseIP(name); ip != nil {
			ips = append(ips, ip)
			continue
		}
		dns = append(dns, name)
	}
	return dns, ips
}

// randomSerial answers a serial number big enough never to collide between two certificates of
// one CA.
func randomSerial(source io.Reader) *big.Int {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(source, limit)
	if err != nil {
		return big.NewInt(1)
	}
	return n
}

// loadPair reads one certificate-and-key pair and answers them only when they belong together.
// A missing pair is os.ErrNotExist; a pair that cannot serve is BrokenCAError.
func (a *Authority) loadPair(certFile, keyFile string) (*x509.Certificate, crypto.PrivateKey, error) {
	certMissing := false
	certBody, err := os.ReadFile(certFile)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, err
		}
		certMissing = true
	}
	keyBody, err := os.ReadFile(keyFile)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, err
		}
		if certMissing {
			return nil, nil, os.ErrNotExist
		}
		return nil, nil, &BrokenCAError{Dir: a.dir, Err: fmt.Errorf("%s stands without its key", filepath.Base(certFile))}
	}
	if certMissing {
		return nil, nil, &BrokenCAError{Dir: a.dir, Err: fmt.Errorf("%s stands without its certificate", filepath.Base(certFile))}
	}
	cert, err := parseCertificate(certBody)
	if err != nil {
		return nil, nil, &BrokenCAError{Dir: a.dir, Err: fmt.Errorf("%s: %w", filepath.Base(certFile), err)}
	}
	key, err := parseKey(keyBody)
	if err != nil {
		return nil, nil, &BrokenCAError{Dir: a.dir, Err: fmt.Errorf("%s: %w", filepath.Base(keyFile), err)}
	}
	switch pub := cert.PublicKey.(type) {
	case *ecdsa.PublicKey:
		priv, ok := key.(*ecdsa.PrivateKey)
		if !ok || !pub.Equal(&priv.PublicKey) {
			return nil, nil, &BrokenCAError{Dir: a.dir, Err: errors.New("the key does not match the certificate")}
		}
	default:
		return nil, nil, &BrokenCAError{Dir: a.dir, Err: errors.New("the certificate's key is not ECDSA")}
	}
	return cert, key, nil
}

// parseCertificate answers the certificate one PEM body holds.
func parseCertificate(body []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(body)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("no certificate in the file")
	}
	return x509.ParseCertificate(block.Bytes)
}

// parseKey answers the ECDSA key one PEM body holds.
func parseKey(body []byte) (crypto.PrivateKey, error) {
	block, _ := pem.Decode(body)
	if block == nil {
		return nil, errors.New("no key in the file")
	}
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return x509.ParsePKCS8PrivateKey(block.Bytes)
}

// writePEM lays one PEM file atomically with private modes: the directory 0700, the file 0600
// through a temp file and a rename, so a reader never sees half of it.
func writePEM(path, kind string, der []byte) error {
	return writePrivate(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}))
}

// writePrivate lays one file atomically with private modes: a temp file beside it, then a
// rename, so a reader never sees half of it.
func writePrivate(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
