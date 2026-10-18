package pki

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"

	"github.com/smallstep/truststore"
)

// Store installs and removes a certificate in the machine's trust stores. The trust words reach
// one through the world they run in, so tests answer from a fake and no test ever touches a real
// store.
type Store interface {
	Install(cert *x509.Certificate) error
	Uninstall(cert *x509.Certificate) error
}

// StoreOffEnv names the variable that turns the machine's real trust stores off. Every test
// process that runs a huddle binary sets it, and the binaries inherit it, so no test, in process
// or exec'd, can install or remove a root on the machine it runs on.
const StoreOffEnv = "HUDDLE_TRUST_STORE_OFF"

// StoreOffError is the refusal a real store answers inside a test or with StoreOffEnv set.
type StoreOffError struct{}

func (StoreOffError) Error() string {
	return "the machine's trust stores are off here (a test is running or " + StoreOffEnv + " is set)"
}

// storeOff answers whether this process must leave the machine's trust stores alone.
func storeOff() bool { return testing.Testing() || os.Getenv(StoreOffEnv) != "" }

// SystemStore reaches the machine's real trust stores through smallstep/truststore (the library
// mkcert and Caddy use): the OS's own store and Firefox's NSS databases; Java's is skipped. On
// macOS the OS asks for the password itself. It refuses inside tests (storeOff), and its only test
// proves that refusal.
type SystemStore struct{}

// Install puts cert into the OS store and Firefox's NSS databases.
func (SystemStore) Install(cert *x509.Certificate) error {
	if storeOff() {
		return StoreOffError{}
	}
	return truststore.Install(cert, truststore.WithFirefox())
}

// Uninstall takes cert out of the OS store and Firefox's NSS databases.
func (SystemStore) Uninstall(cert *x509.Certificate) error {
	if storeOff() {
		return StoreOffError{}
	}
	return truststore.Uninstall(cert, truststore.WithFirefox())
}

// TrustedBy answers whether root verifies against pool: the check the trust words run before
// they touch any store, so each stays idempotent — a root the pool already vouches for is never
// installed twice, and one it does not know is not removed twice.
func TrustedBy(pool *x509.CertPool, root *x509.Certificate) bool {
	if pool == nil {
		return false
	}
	if _, err := root.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		return false
	}
	return true
}

// TrustedFile is the marker a successful install writes: the uninstall paths remove the root
// only when it vouches for them, so a root the owner put in by hand is never taken out by
// huddle's own removal.
func (a *Authority) TrustedFile() string { return filepath.Join(a.dir, "trusted") }

// MarkTrusted records that huddle itself installed this root.
func (a *Authority) MarkTrusted() error {
	return writePrivate(a.TrustedFile(), []byte("installed by huddle trust\n"))
}

// TrustedByUs answers whether the marker says huddle itself installed the root.
func (a *Authority) TrustedByUs() bool {
	_, err := os.Stat(a.TrustedFile())
	return err == nil
}

// UnmarkTrusted drops the marker: the root is no longer one huddle's removal owns.
func (a *Authority) UnmarkTrusted() error {
	if err := os.Remove(a.TrustedFile()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
