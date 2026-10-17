package cli

// The World's fallbacks: everything a command needs that the caller did not name comes from the
// process itself, the machine's own lookups, or a fake the command was handed.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// TestTheWorldFallsBackToTheProcessForWhatItDoesNotName pins the fallbacks a nil or empty World
// takes: the process's input, variables, directory, binary and lookups, and the machine's own
// answers for DNS names and issuers.
func TestTheWorldFallsBackToTheProcessForWhatItDoesNotName(t *testing.T) {
	var gone *World
	if gone.stdin() == nil {
		t.Error("stdin")
	}
	if gone.dir() == "" || gone.bin() == "" {
		t.Error("dir or bin")
	}
	if len(gone.environ()) != len(os.Environ()) {
		t.Error("environ")
	}
	if gone.proc().Command != nil || gone.proc().ListenOf != nil {
		t.Error("proc")
	}
	if gone.dnsKnown("cloudflare") != (emptyWorld().dnsKnown("cloudflare")) {
		t.Error("dnsKnown answered differently for a nil and an empty world")
	}
	if _, err := gone.certIssue("not-a-provider", "", ""); err == nil {
		t.Error("certIssue over an unknown provider succeeded")
	}
	// the port reader behind a world that names no lookups: a pid that names no process
	if port, ok := gone.proc().listenOf(-1); ok || port != 0 {
		t.Errorf("listenOf(-1) = (%d, %v)", port, ok)
	}
}

// emptyWorld is a world with no overrides at all.
func emptyWorld() *World { return &World{} }

// TestTheWorldHandsBackTheFakesItWasGiven pins that a World's own answers win: the input, the
// secret store, the DNS list and the issuer come back exactly as they were handed in.
func TestTheWorldHandsBackTheFakesItWasGiven(t *testing.T) {
	box := &struct{ h home.Home }{}
	w := &World{
		Stdin: strings.NewReader(""),
		Secrets: func(ctx context.Context, h home.Home) home.SecretStore {
			box.h = h
			return home.KeyFiles{Dir: t.TempDir()}
		},
		DNSKnown: func(name string) bool { return name == "mine" },
		CertIssue: func(provider, token, accountKey string) (home.ACME, error) {
			return nil, nil
		},
	}
	if w.stdin() == os.Stdin {
		t.Error("stdin")
	}
	if w.secrets(context.Background(), home.Home{Root: "/one"}).(home.KeyFiles).Dir == "" || box.h.Root != "/one" {
		t.Error("secrets")
	}
	if !w.dnsKnown("mine") || w.dnsKnown("other") {
		t.Error("dnsKnown")
	}
	if _, err := w.certIssue("mine", "", ""); err != nil {
		t.Errorf("certIssue: %v", err)
	}
	if port, ok := (Proc{ListenOf: func(int) (int, bool) { return 4321, true }}).listenOf(1); !ok || port != 4321 {
		t.Errorf("listenOf = (%d, %v)", port, ok)
	}
}
