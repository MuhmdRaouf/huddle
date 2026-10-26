package home

// The home tree's refusals and the certificate's edges: a write that cannot land names its
// reason instead of leaving half a file, a root that cannot be laid out refuses Ensure, and the
// renewal loop's real sleep yields at once to a context that is already done.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubACME is an issuer that answers the bytes it was built with, whatever it is asked.
type stubACME struct {
	cert []byte
	key  []byte
}

func (s *stubACME) Obtain(context.Context, string) ([]byte, []byte, error) {
	return s.cert, s.key, nil
}

func TestNoHomeErrorSaysWhatToSet(t *testing.T) {
	msg := NoHomeError{}.Error()
	if msg == "" || !containsAll(msg, "HOME", "HUDDLE_HOME") {
		t.Errorf("the no-home refusal = %q, want it to name HOME and HUDDLE_HOME", msg)
	}
}

func TestEnsureRefusesARootItCannotLayOut(t *testing.T) {
	root := filepath.Join(t.TempDir(), "huddle")
	if err := os.WriteFile(root, []byte("a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (Home{Root: root}).Ensure(context.Background()); err == nil {
		t.Fatal("Ensure laid out a root that is a file")
	}
}

func TestEnsureRefusesARunFileItCannotWrite(t *testing.T) {
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	if err := os.MkdirAll(h.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	// the temp file the atomic write picks is taken by a directory: the write fails
	if err := os.MkdirAll(fmt.Sprintf("%s.%d.tmp", h.Outbox(), os.Getpid()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := h.Ensure(context.Background()); err == nil {
		t.Fatal("Ensure wrote the outbox over a directory")
	}
}

func TestWriteFile0600NamesItsFailures(t *testing.T) {
	dir := t.TempDir()
	// the target is a directory: the rename refuses
	taken := filepath.Join(dir, "taken")
	if err := os.MkdirAll(taken, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeFile0600(taken, []byte("x")); err == nil {
		t.Error("a write onto a directory succeeded")
	}
	// the parent chain crosses a file: the mkdir refuses
	blocked := filepath.Join(dir, "f")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFile0600(filepath.Join(blocked, "sub", "f"), nil); err == nil {
		t.Error("a write under a file succeeded")
	}
	// a good write lands private and whole
	good := filepath.Join(dir, "good")
	if err := writeFile0600(good, []byte("body")); err != nil {
		t.Fatalf("writeFile0600: %v", err)
	}
	if mode(t, good) != 0o600 {
		t.Errorf("the file is %o, want 0600", mode(t, good))
	}
}

func TestSleepStretchesYieldsToADoneContextAndAnswersWholeAfter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sleepStretches(ctx, time.Hour) {
		t.Error("a sleep over a done context answered keep-going")
	}
	if !sleepStretches(context.Background(), 0) {
		t.Error("a zero sleep did not answer keep-going")
	}
}

func TestIssueCertificateNamesEveryWayAnIssuerFails(t *testing.T) {
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	// the issuer refuses
	if _, err := IssueCertificate(bg(), h, &fakeACME{err: errors.New("dns refused")}, "hub.test"); err == nil {
		t.Error("an issuer that refuses answered a certificate")
	}
	// the issuer answers nothing
	empty := &stubACME{}
	if _, err := IssueCertificate(bg(), h, empty, "hub.test"); err == nil {
		t.Error("an issuer that answered no certificate succeeded")
	}
	// the issuer answers a torn certificate
	torn := &stubACME{cert: []byte("not a pem block"), key: []byte("key")}
	if _, err := IssueCertificate(bg(), h, torn, "hub.test"); err == nil {
		t.Error("a torn certificate succeeded")
	}
	// the pair cannot land: tls/ is a file
	blocked := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	if err := os.MkdirAll(blocked.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocked.TLS(), []byte("a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	good := &stubACME{cert: mintPair(t, time.Hour).certPEM, key: []byte("key")}
	if _, err := IssueCertificate(bg(), blocked, good, "hub.test"); err == nil {
		t.Error("a certificate laid under a file succeeded")
	}
}

func TestSaveCertificateAndExpiryReadWhatTheyWrote(t *testing.T) {
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	pair := mintPair(t, 24*time.Hour)
	if err := SaveCertificate(h, pair.certPEM, pair.keyPEM); err != nil {
		t.Fatalf("SaveCertificate: %v", err)
	}
	if mode(t, h.TLS()) != 0o700 {
		t.Errorf("tls/ is %o, want 0700", mode(t, h.TLS()))
	}
	expiry, err := CertificateExpiry(h)
	if err != nil || !expiry.Equal(pair.notAfter) {
		t.Errorf("expiry = (%v, %v), want the leaf's %v", expiry, err, pair.notAfter)
	}
	// a torn certificate names itself
	if err := os.WriteFile(filepath.Join(h.TLS(), "cert.pem"), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CertificateExpiry(h); err == nil {
		t.Error("a torn certificate answered an expiry")
	}
}

func TestGetRefusesAnAccountThatIsNotAName(t *testing.T) {
	secrets := KeyFiles{Dir: t.TempDir()}
	if _, ok := secrets.Get(bg(), "../escape"); ok {
		t.Error("Get answered an account that is not a name")
	}
	if _, ok := secrets.Get(bg(), "dns:none"); ok {
		t.Error("Get answered an account no file holds")
	}
	// an empty file is no secret
	if err := os.WriteFile(filepath.Join(secrets.Dir, "dns-empty"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := secrets.Get(bg(), "dns:empty"); ok {
		t.Error("Get answered an empty file with a secret")
	}
}

func TestResolveTakesTheEnvironmentWhenGivenNone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HUDDLE_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	got, err := Resolve(nil)
	if err != nil || got.Root != home {
		t.Errorf("Resolve(nil) = (%+v, %v), want the environment's home", got, err)
	}
}

// containsAll says s carries every piece.
func containsAll(s string, pieces ...string) bool {
	for _, piece := range pieces {
		if !strings.Contains(s, piece) {
			return false
		}
	}
	return true
}
