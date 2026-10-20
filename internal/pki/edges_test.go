package pki

// The edges the lifetimes tests do not reach: the handshake surface, the real sleep in both its
// outcomes, a spent or broken intermediate re-minted behind the standing root, refusals for a
// corrupt half, an unreadable directory, and a renewal point already past when the loop starts.

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGetCertificateAnswersTheLeafAsItStands(t *testing.T) {
	authority, _ := newTestAuthority(t)
	leaf, err := NewLeaf(authority, HubLeafNames("hub.mine.test"))
	if err != nil {
		t.Fatal(err)
	}
	served, err := leaf.GetCertificate(nil)
	if err != nil || served == nil {
		t.Fatalf("GetCertificate = (%v, %v), want the standing leaf", served, err)
	}
	if !served.Leaf.Equal(leaf.current().Leaf) {
		t.Error("the handshake answers a leaf other than the one on hand")
	}
}

func TestSleepStretchesWaitsAndStopsWithItsContext(t *testing.T) {
	if !sleepStretches(context.Background(), 20*time.Millisecond) {
		t.Error("a live context's wait did not run out")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sleepStretches(ctx, time.Hour) {
		t.Error("an ended context still waited")
	}
}

func TestABrokenIntermediateIsReMintedBehindTheStandingRoot(t *testing.T) {
	authority, _ := newTestAuthority(t)
	first, _, err := authority.Intermediate()
	if err != nil {
		t.Fatal(err)
	}
	root, err := authority.Root()
	if err != nil {
		t.Fatal(err)
	}
	for _, broken := range []struct {
		name string
		body []byte
	}{
		{"a corrupt certificate", []byte("not a certificate")},
		{"a certificate without its key", nil},
	} {
		t.Run(broken.name, func(t *testing.T) {
			if broken.body == nil {
				if err := os.Remove(authority.IntermediateKeyFile()); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(authority.IntermediateFile(), broken.body, 0o600); err != nil {
				t.Fatal(err)
			}
			fresh, _, err := authority.Intermediate()
			if err != nil {
				t.Fatalf("the broken intermediate was not re-minted: %v", err)
			}
			if err := fresh.CheckSignatureFrom(root); err != nil {
				t.Errorf("the re-minted intermediate is not signed by the standing root: %v", err)
			}
			if fresh.Equal(first) {
				t.Error("the broken intermediate was kept")
			}
		})
	}
}

func TestACorruptRootCertificateIsARefusal(t *testing.T) {
	authority, _ := newTestAuthority(t)
	if _, err := authority.Root(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authority.RootFile(), []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	var broken *BrokenCAError
	if _, err := authority.Root(); !errors.As(err, &broken) {
		t.Fatalf("a corrupt root.crt answers %T (%v), want BrokenCAError", err, err)
	}
	if _, err := authority.ReadRoot(); err == nil {
		t.Error("ReadRoot accepted a corrupt root.crt")
	}
}

func TestAnUnreadableCADirectoryIsARefusal(t *testing.T) {
	authority, _ := newTestAuthority(t)
	if _, err := authority.Root(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(authority.Dir(), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(authority.Dir(), 0o700) })
	if _, err := authority.Root(); err == nil {
		t.Error("an unreadable CA directory answered a root")
	}
}

func TestACAPathThatCannotBeADirectoryIsARefusal(t *testing.T) {
	dir := t.TempDir()
	// a regular file where the CA's directory would lay: nothing can be written under it
	blocked := filepath.Join(dir, "pki")
	if err := os.WriteFile(blocked, []byte("a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	authority := New(blocked, Options{Rand: rand.Reader})
	if _, err := authority.Root(); err == nil {
		t.Error("a CA laid over a regular file answered a root")
	}
	if _, err := NewLeaf(authority, HubLeafNames("hub.mine.test")); err == nil {
		t.Error("a leaf was minted over a regular file")
	}
}

func TestTheLoopRenewsAtOnceWhenThePointIsAlreadyPast(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)}
	authority := New(filepath.Join(t.TempDir(), "pki"), Options{Now: clock.Now, Rand: rand.Reader})
	leaf, err := NewLeaf(authority, HubLeafNames("hub.mine.test"))
	if err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(9 * time.Hour) // past two thirds of the leaf's life
	first := leaf.current().Leaf
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	done := make(chan struct{})
	// no options: the loop's own sleep and log serve, and the point already past means the
	// renewal happens before any wait
	go func() {
		defer close(done)
		leaf.KeepFresh(ctx, KeepFreshOptions{})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !leaf.current().Leaf.Equal(first) {
			stop() // the next wait is hours long on the real sleep; the context ends it
			<-done
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("a leaf past its renewal point was not renewed at once")
}

// The default clock and randomness serve an authority built with no options.
func TestADefaultOptionsAuthorityServes(t *testing.T) {
	authority := New(filepath.Join(t.TempDir(), "pki"), Options{})
	if _, err := authority.Root(); err != nil {
		t.Fatalf("the default root: %v", err)
	}
	if _, _, err := authority.Intermediate(); err != nil {
		t.Fatalf("the default intermediate: %v", err)
	}
	if _, err := NewLeaf(authority, HubLeafNames("hub.mine.test")); err != nil {
		t.Fatalf("the default leaf: %v", err)
	}
}

// failingReader answers nothing, the way a spent entropy source does.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("the randomness refused") }

// A randomness source that answers nothing costs the serial its size, never the minting: the
// fallback keeps two certificates from colliding on a refusal.
func TestARandomSerialFallsBackWhenTheSourceRefuses(t *testing.T) {
	if got := randomSerial(failingReader{}); got.Cmp(big.NewInt(1)) != 0 {
		t.Errorf("randomSerial = %v, want the fixed fallback", got)
	}
}

func TestACAIsNotLaidIntoAnUnwritableDirectory(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "pki")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
	if _, err := New(blocked, Options{Rand: rand.Reader}).Root(); err == nil {
		t.Error("a root was laid into a directory nothing can be written to")
	}
}

// A spent intermediate that cannot be re-laid (the directory turned read-only) is an answer, not
// a silent serving of the expired one.
func TestASpentIntermediateCannotBeKeptWhenItCannotBeRelaid(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)}
	dir := filepath.Join(t.TempDir(), "pki")
	authority := New(dir, Options{Now: clock.Now, Rand: rand.Reader})
	if _, _, err := authority.Intermediate(); err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(IntermediateLife) // the intermediate's life is over
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, _, err := authority.Intermediate(); err == nil {
		t.Error("a spent intermediate was kept or relaid into a read-only directory")
	}
}

// An intermediate key in the PKCS8 spelling loads the same as the ECDSA one.
func TestAnIntermediateKeyInThePKCS8SpellingLoads(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pki")
	authority := New(dir, Options{Rand: rand.Reader})
	first, _, err := authority.Intermediate()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(authority.IntermediateKeyFile())
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(body)
	ecKey, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authority.IntermediateKeyFile(),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), 0o600); err != nil {
		t.Fatal(err)
	}
	again, _, err := authority.Intermediate()
	if err != nil {
		t.Fatalf("the PKCS8 key would not load: %v", err)
	}
	if !again.Equal(first) {
		t.Error("the intermediate was re-minted behind a key that only changed spelling")
	}
}
