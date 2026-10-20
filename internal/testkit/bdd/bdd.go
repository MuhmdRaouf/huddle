// Package bdd writes behaviour tests as features and scenarios over the standard testing package: each step is
// a subtest named "Given …", "When …", "Then …", so `go test -v` prints the scenario as sentences and a failing
// step names the sentence that failed.
package bdd

import "testing"

// Feature groups the scenarios of one behaviour under one subtest named "Feature: <name>".
func Feature(t *testing.T, name string, body func(f *F)) {
	t.Helper()
	t.Run("Feature: "+name, func(t *testing.T) {
		body(&F{t: t})
	})
}

// F is one feature.
type F struct{ t *testing.T }

// Scenario runs one scenario as a subtest named "Scenario: <name>". Its steps run in order; after a step fails,
// the later steps are skipped (t.Skip with "skipped: an earlier step failed").
func (f *F) Scenario(name string, body func(s *S)) {
	f.t.Run("Scenario: "+name, func(t *testing.T) {
		body(&S{T: t})
	})
}

// S is one scenario. Its T is the scenario's own *testing.T, for t.TempDir, t.Cleanup and helpers.
type S struct {
	T      *testing.T
	failed bool
}

// Given runs one setup step as a subtest named "Given <text>".
func (s *S) Given(text string, step func(t *testing.T)) { s.step("Given", text, step) }

// When runs one action step as a subtest named "When <text>".
func (s *S) When(text string, step func(t *testing.T)) { s.step("When", text, step) }

// Then runs one assertion step as a subtest named "Then <text>".
func (s *S) Then(text string, step func(t *testing.T)) { s.step("Then", text, step) }

// And runs one follow-on step as a subtest named "And <text>".
func (s *S) And(text string, step func(t *testing.T)) { s.step("And", text, step) }

// step runs one step as a subtest named "<keyword> <text>"; a step that fails marks the scenario so the
// remaining steps skip instead of piling errors onto a setup that already broke.
func (s *S) step(keyword, text string, step func(t *testing.T)) {
	if s.failed {
		s.T.Run(keyword+" "+text, func(t *testing.T) { t.Skip("skipped: an earlier step failed") })
		return
	}
	if ok := s.T.Run(keyword+" "+text, step); !ok {
		s.failed = true
	}
}
