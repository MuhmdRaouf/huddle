package outside

// The name check that decides between an unknown provider and an unsupported one, decided on
// lego's answer alone.

import (
	"errors"
	"testing"
)

func TestDNSProviderKnownRefusesANameLegoDoesNotKnow(t *testing.T) {
	// a made-up name lego refuses with "unrecognized DNS provider: <name>" is not known
	if DNSProviderKnown("nosuchdns") {
		t.Error(`DNSProviderKnown(nosuchdns) = true, want false for a name lego calls unrecognized`)
	}
	// a provider lego switches on builds without a network and without its token
	if !DNSProviderKnown("cloudflare") {
		t.Error("DNSProviderKnown(cloudflare) = false, want a provider lego builds")
	}
	// a name that is not even shaped like one is refused before lego is asked
	if DNSProviderKnown("Bad Name!") {
		t.Error(`DNSProviderKnown("Bad Name!") = true, want false for a name that is not a provider name`)
	}
}

func TestNewACMENamesAnUnknownProviderAsUnknown(t *testing.T) {
	_, err := NewACME("nosuchdns", "t", "")
	if !errors.As(err, &UnknownProviderError{}) {
		t.Errorf("NewACME(nosuchdns) = %v, want UnknownProviderError", err)
	}
}
