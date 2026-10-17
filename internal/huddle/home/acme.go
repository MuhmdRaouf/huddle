package home

// The hub's certificate (SPEC 7.2): obtained over DNS-01 by an ACME issuer (the real one talks
// to Let's Encrypt; everything here is issuer-agnostic), laid in the home's tls/ with private
// modes, and kept fresh by a renewal loop that wakes 30 days before the leaf expires and tries
// again every 6 hours after a failure — one line per attempt, so the log says what happened
// without ever saying a secret.

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ACME obtains one certificate for a host over DNS-01: the implementation talks to the
// certificate authority and the DNS provider, the caller stores what it answers.
type ACME interface {
	Obtain(ctx context.Context, host string) (cert, key []byte, err error)
}

// The renewal loop's timing (SPEC 7.2): a renewal is tried 30 days before expiry, and a failed
// one is tried again 6 hours later.
const (
	RenewBefore = 30 * 24 * time.Hour
	RenewRetry  = 6 * time.Hour
)

// RenewalOptions shapes the certificate renewal loop's world: the clock, the sleep and the log.
// Every nil member takes the real one.
type RenewalOptions struct {
	// Now is the clock the waits are computed against; nil takes the wall clock.
	Now func() time.Time
	// Sleep waits one stretch and answers whether the loop goes on (false once its context
	// ended); nil waits for real, in bounded stretches so a stop is never held hostage by one
	// long wait.
	Sleep func(ctx context.Context, d time.Duration) bool
	// Log says one line per attempt's outcome.
	Log func(line string)
}

// IssueCertificate obtains one certificate for host over DNS-01 and lays it in the home's tls/
// (0600, both halves in one move), answering the new leaf's expiry.
func IssueCertificate(ctx context.Context, h Home, acme ACME, host string) (time.Time, error) {
	cert, key, err := acme.Obtain(ctx, host)
	if err != nil {
		return time.Time{}, err
	}
	if len(cert) == 0 || len(key) == 0 {
		return time.Time{}, errors.New("huddle: the issuer answered no certificate")
	}
	if err := SaveCertificate(h, cert, key); err != nil {
		return time.Time{}, err
	}
	expiry, err := leafExpiry(cert)
	if err != nil {
		return time.Time{}, err
	}
	return expiry, nil
}

// SaveCertificate writes the pair into the home's tls/ with private modes: the directory 0700,
// each file 0600 through a temp file and a rename, so the hub listener reading them never sees
// half a pair.
func SaveCertificate(h Home, cert, key []byte) error {
	if err := os.MkdirAll(h.TLS(), 0o700); err != nil {
		return err
	}
	if err := writeFile0600(filepath.Join(h.TLS(), "cert.pem"), cert); err != nil {
		return err
	}
	return writeFile0600(filepath.Join(h.TLS(), "key.pem"), key)
}

// CertificateExpiry answers the leaf certificate's NotAfter from the home's tls/cert.pem.
func CertificateExpiry(h Home) (time.Time, error) {
	cert, err := os.ReadFile(filepath.Join(h.TLS(), "cert.pem"))
	if err != nil {
		return time.Time{}, err
	}
	return leafExpiry(cert)
}

// RenewCertificates keeps the certificate in the home's tls/ fresh: it sleeps until the leaf is
// RenewBefore from expiry, then asks issuer for a fresh one and starts over. A failure — an
// issuer that cannot be built included — logs one line and is tried again RenewRetry later.
// The loop answers when its context ends, or when the home holds no certificate: the first one
// is `huddle cert`'s to obtain, and a certificate that vanished is the owner's to look at.
func RenewCertificates(ctx context.Context, h Home, issuer func(ctx context.Context) (ACME, error), host string, o RenewalOptions) {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	sleep := o.Sleep
	if sleep == nil {
		sleep = sleepStretches
	}
	log := o.Log
	if log == nil {
		log = func(string) {}
	}
	for {
		expiry, err := CertificateExpiry(h)
		if err != nil {
			log(fmt.Sprintf("huddle: no certificate to renew in %s: %v (huddle cert obtains the first one)", h.TLS(), err))
			return
		}
		if wait := expiry.Add(-RenewBefore).Sub(now()); wait > 0 && !sleep(ctx, wait) {
			return
		}
		acme, err := issuer(ctx)
		if err == nil {
			var fresh time.Time
			fresh, err = IssueCertificate(ctx, h, acme, host)
			if err == nil {
				log(fmt.Sprintf("huddle: certificate for %s renewed, expires %s", host, fresh.Format(time.DateOnly)))
				continue
			}
		}
		log(fmt.Sprintf("huddle: certificate renewal for %s failed: %v; trying again in %s", host, err, RenewRetry))
		if !sleep(ctx, RenewRetry) {
			return
		}
	}
}

// sleepStretches waits d for real, in stretches of an hour that each watch the context, so a
// months-long wait never outlives the server it belongs to.
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

// leafExpiry answers the first certificate's NotAfter: the leaf is what expiry means, whatever
// chain follows it in the same PEM.
func leafExpiry(cert []byte) (time.Time, error) {
	block, _ := pem.Decode(cert)
	if block == nil || block.Type != "CERTIFICATE" {
		return time.Time{}, errors.New("huddle: tls/cert.pem holds no certificate")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, err
	}
	return leaf.NotAfter, nil
}
