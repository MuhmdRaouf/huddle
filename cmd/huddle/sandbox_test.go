package main

import (
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
)

// The hooks and CLI words these tests run in process read os.Environ: under the sandbox they
// never reach the owner's home or the live router.
func TestTheProcessIsSandboxed(t *testing.T) {
	isoenv.RequireSandboxed(t)
}
