package pki

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"sync"
	"time"
)

// renewRetry is how long a failed leaf renewal waits before trying again: short, because a leaf
// that runs out strands the hub listener's browsers.
const renewRetry = time.Hour

// KeepFreshOptions shapes the leaf's renewal loop; every nil member takes the real one.
type KeepFreshOptions struct {
	// Sleep waits one stretch and answers whether the loop goes on (false once its context
	// ended); nil waits for real, in bounded stretches so a stop is never held hostage by one
	// long wait.
	Sleep func(ctx context.Context, d time.Duration) bool
	// Log says one line per renewal and per failed attempt.
	Log func(line string)
}

// Leaf answers the hub listener's certificate on every handshake and renews it in the
// background: a fresh 12-hour leaf is minted at two thirds of the old one's life and swapped in
// whole, so a renewal needs no restart and a handshake never waits on minting. The leaf lives in
// memory only — it is gone with the process, and the next start mints a fresh one. The clock and
// the randomness are the authority's.
type Leaf struct {
	ca    *Authority
	names []string
	mu    sync.Mutex
	cert  *tls.Certificate
}

// NewLeaf issues the first leaf for names at once, so the listener it belongs to never starts
// without a certificate to serve.
func NewLeaf(ca *Authority, names []string) (*Leaf, error) {
	leaf := &Leaf{ca: ca, names: names}
	cert, err := leaf.mint()
	if err != nil {
		return nil, err
	}
	leaf.cert = cert
	return leaf, nil
}

// GetCertificate answers the leaf a handshake is served with.
func (l *Leaf) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return l.current(), nil
}

// current answers the leaf as it stands.
func (l *Leaf) current() *tls.Certificate {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cert
}

// KeepFresh renews the leaf until ctx ends: it waits until two thirds of the leaf's life is
// gone, mints the next one and starts over. A failed attempt logs one line and is tried again
// renewRetry later.
func (l *Leaf) KeepFresh(ctx context.Context, o KeepFreshOptions) {
	sleep := o.Sleep
	if sleep == nil {
		sleep = sleepStretches
	}
	log := o.Log
	if log == nil {
		log = func(string) {}
	}
	for {
		cert := l.current()
		wait := cert.Leaf.NotAfter.Add(-LeafLife / 3).Sub(l.ca.o.clock()())
		if wait > 0 && !sleep(ctx, wait) {
			return
		}
		fresh, err := l.mint()
		if err != nil {
			log(fmt.Sprintf("huddle: renewing the local CA leaf failed: %v; trying again in %s", err, renewRetry))
			if !sleep(ctx, renewRetry) {
				return
			}
			continue
		}
		l.mu.Lock()
		l.cert = fresh
		l.mu.Unlock()
		log(fmt.Sprintf("huddle: local CA leaf renewed, expires %s", fresh.Leaf.NotAfter.Format(time.RFC3339)))
	}
}

// mint issues one leaf for the names, served with the intermediate behind it.
func (l *Leaf) mint() (*tls.Certificate, error) {
	intermediate, signer, err := l.ca.Intermediate()
	if err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), l.ca.o.random())
	if err != nil {
		return nil, err
	}
	now := l.ca.o.clock()()
	dns, ips := splitNames(l.names)
	template := &x509.Certificate{
		SerialNumber: randomSerial(l.ca.o.random()),
		Subject:      pkix.Name{CommonName: dns[0]},
		DNSNames:     dns,
		IPAddresses:  ips,
		NotBefore:    now,
		NotAfter:     now.Add(LeafLife),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(l.ca.o.random(), template, intermediate, &key.PublicKey, signer)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{
		Certificate: [][]byte{der, intermediate.Raw},
		PrivateKey:  key,
		Leaf:        leaf,
	}, nil
}

// sleepStretches waits d for real, in stretches of an hour that each watch the context, so a
// long wait never outlives the server it belongs to.
func sleepStretches(ctx context.Context, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return true
		}
		stretch := left
		if stretch > time.Hour {
			stretch = time.Hour
		}
		timer := time.NewTimer(stretch)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}
