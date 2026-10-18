package pki

// The leaf's renewal loop against a fake clock: the leaf is renewed at exactly two thirds of its
// 12-hour life, the handshake always answers the freshest leaf, and a failed minting is retried
// an hour later without losing the standing certificate.

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTheLeafIsRenewedAtTwoThirdsOfItsLife(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)}
	authority := New(filepath.Join(t.TempDir(), "pki"), Options{Now: clock.Now, Rand: rand.Reader})
	leaf, err := NewLeaf(authority, HubLeafNames("hub.mine.test"))
	if err != nil {
		t.Fatalf("the first leaf: %v", err)
	}
	first := leaf.current()
	// the loop's goroutine and the test's wait read the same tallies: one lock keeps them honest
	var mu sync.Mutex
	var waits []time.Duration
	lines := 0
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		leaf.KeepFresh(ctx, KeepFreshOptions{
			// the sleep moves the clock by exactly the wait asked for, so the loop runs on paper
			Sleep: func(_ context.Context, d time.Duration) bool {
				mu.Lock()
				waits = append(waits, d)
				keepGoing := len(waits) < 4
				mu.Unlock()
				clock.now = clock.now.Add(d)
				return keepGoing
			},
			Log: func(string) {
				mu.Lock()
				lines++
				mu.Unlock()
			},
		})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		counted := len(waits)
		mu.Unlock()
		if counted >= 4 || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	stop()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(waits) < 4 {
		t.Fatalf("the renewal loop stopped after %d waits: %v", len(waits), waits)
	}
	// every wait reaches a renewal point: 8 h after each minting, two thirds of the 12 h life
	for i, wait := range waits {
		if want := 8 * time.Hour; wait != want {
			t.Errorf("wait %d = %v, want %v (two thirds of the leaf's life)", i, wait, want)
		}
	}
	if lines != 3 {
		t.Errorf("%d renewal lines, want one per renewal (3)", lines)
	}
	final := leaf.current()
	if final.Leaf.Equal(first.Leaf) {
		t.Error("the handshake still answers the first leaf after the renewals")
	}
	// three renewals in, the leaf on hand was minted at the third renewal point
	if since := final.Leaf.NotBefore.Sub(first.Leaf.NotBefore); since != 3*8*time.Hour {
		t.Errorf("the leaf on hand started %v after the first, want 24 h", since)
	}
}

func TestAFailedRenewalKeepsTheStandingLeafAndRetries(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)}
	authority := New(filepath.Join(t.TempDir(), "pki"), Options{Now: clock.Now, Rand: rand.Reader})
	leaf, err := NewLeaf(authority, HubLeafNames("hub.mine.test"))
	if err != nil {
		t.Fatalf("the first leaf: %v", err)
	}
	first := leaf.current()
	// a corrupt root.key fails every minting after the first: the root is never re-minted
	if err := writeFile0600ForTest(authority.RootKeyFile(), []byte("not a key")); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	retries := 0
	var lines []string
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		leaf.KeepFresh(ctx, KeepFreshOptions{
			Sleep: func(_ context.Context, d time.Duration) bool {
				clock.now = clock.now.Add(d)
				mu.Lock()
				if d == renewRetry {
					retries++
				}
				mu.Unlock()
				// the loop ends on its own after the third retry: once the clock passes the
				// leaf's expiry no wait stands between the mintings, only the sleep's answer
				return retries < 3
			},
			Log: func(line string) {
				mu.Lock()
				lines = append(lines, line)
				mu.Unlock()
			},
		})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		counted := retries
		mu.Unlock()
		if counted >= 2 || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	stop()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(lines) < 2 || !strings.Contains(lines[0], "failed") {
		t.Fatalf("the failed renewals said %q, want lines naming the failure", lines)
	}
	if now := leaf.current(); !now.Leaf.Equal(first.Leaf) {
		t.Error("a failed renewal swapped the standing leaf out")
	}
}

// writeFile0600ForTest lays a file the way a corrupt one lands on disk.
func writeFile0600ForTest(path string, body []byte) error {
	return writePrivate(path, body)
}

// The handshake-facing surface is a tls.Certificate whose halves belong together, so
// tls.Config.GetCertificate serves it as it stands.
func TestTheLeafAnswersATLSCertificateWhole(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)}
	authority := New(t.TempDir(), Options{Now: clock.Now, Rand: rand.Reader})
	leaf, err := NewLeaf(authority, HubLeafNames("hub.mine.test"))
	if err != nil {
		t.Fatal(err)
	}
	cert := leaf.current()
	if cert.Leaf == nil || cert.PrivateKey == nil {
		t.Fatal("the leaf answers without its parsed certificate or its key")
	}
	keyDER, err := x509.MarshalECPrivateKey(cert.PrivateKey.(*ecdsa.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	); err != nil {
		t.Errorf("the served halves are not a tls pair: %v", err)
	}
}
