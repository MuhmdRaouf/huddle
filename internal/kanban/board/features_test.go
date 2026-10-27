package board

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An unknown feature refuses and writes nothing: no FEATURES file, no LOG line.
func TestCloseFeatureRefusesAnUnknownFeatureAndWritesNothing(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.card("backlog", "WEB-1", "id: WEB-1\ntitle: \"web\"\ncluster: web\ntype: docs")
	msg := mustRefuse(t, func() ([]string, error) {
		return f.b.CloseFeature(ctx, Args{Actor: "lead", ID: "nosuch"})
	})
	if msg != "no feature nosuch on this board" {
		t.Fatalf("refusal = %q, want the unknown-feature line", msg)
	}
	if _, err := os.Stat(filepath.Join(f.root, "FEATURES")); err == nil {
		t.Fatal("the refusal left a FEATURES file behind")
	}
	if text := f.logText(); text != "" {
		t.Fatalf("LOG after the refusal = %q, want it untouched", text)
	}
}

// A second close answers the already-closed line and writes nothing: the first
// close's entry and its one LOG line stay.
func TestCloseFeatureTwiceAnswersTheAlreadyClosedLine(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.card("backlog", "SWR-1", "id: SWR-1\ntitle: \"one\"\ncluster: swr\ntype: docs")
	mustLines(t, func() ([]string, error) {
		return f.b.CloseFeature(ctx, Args{Actor: "lead", ID: "swr"})
	})
	lines := mustLines(t, func() ([]string, error) {
		return f.b.CloseFeature(ctx, Args{Actor: "dev", ID: "swr"})
	})
	if len(lines) != 1 || lines[0] != "feature swr is already closed" {
		t.Fatalf("the second close = %q, want the already-closed line", lines)
	}
	if got := strings.Count(f.logText(), `"event":"feature-close"`); got != 1 {
		t.Fatalf("LOG holds %d feature-close lines, want the first close's one", got)
	}
	if closed := f.b.ClosedFeatures(); len(closed) != 1 || closed[0].Actor != "lead" {
		t.Fatalf("ClosedFeatures = %+v, want the first close's entry", closed)
	}
}

// A feature that is not closed refuses the reopen.
func TestReopenFeatureRefusesAFeatureThatIsNotClosed(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.card("backlog", "SWR-1", "id: SWR-1\ntitle: \"one\"\ncluster: swr\ntype: docs")
	msg := mustRefuse(t, func() ([]string, error) {
		return f.b.ReopenFeature(ctx, Args{Actor: "lead", ID: "swr"})
	})
	if msg != "feature swr is not closed" {
		t.Fatalf("refusal = %q, want the not-closed line", msg)
	}
}

// An empty name and an empty actor refuse the close.
func TestCloseFeatureRefusesAnEmptyNameAndAnEmptyActor(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.card("backlog", "SWR-1", "id: SWR-1\ntitle: \"one\"\ncluster: swr\ntype: docs")
	msg := mustRefuse(t, func() ([]string, error) {
		return f.b.CloseFeature(ctx, Args{Actor: "lead"})
	})
	if msg != "feature: name required" {
		t.Fatalf("the empty name = %q, want the name-required line", msg)
	}
	msg = mustRefuse(t, func() ([]string, error) {
		return f.b.CloseFeature(ctx, Args{ID: "swr"})
	})
	contains(t, msg, "--actor", "the empty actor")
}

// Two closed features read back sorted by name, and the file is the sorted
// record with one trailing newline.
func TestClosedFeaturesReadBackSortedByName(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.card("backlog", "SWR-1", "id: SWR-1\ntitle: \"one\"\ncluster: swr\ntype: docs")
	f.card("backlog", "WEB-1", "id: WEB-1\ntitle: \"web\"\ncluster: web\ntype: docs")
	mustLines(t, func() ([]string, error) {
		return f.b.CloseFeature(ctx, Args{Actor: "lead", ID: "web"})
	})
	mustLines(t, func() ([]string, error) {
		return f.b.CloseFeature(ctx, Args{Actor: "dev", ID: "swr"})
	})
	closed := f.b.ClosedFeatures()
	want := []ClosedFeature{
		{Name: "swr", Actor: "dev", ClosedAt: stamp},
		{Name: "web", Actor: "lead", ClosedAt: stamp},
	}
	if len(closed) != len(want) || closed[0] != want[0] || closed[1] != want[1] {
		t.Fatalf("ClosedFeatures = %+v, want %+v", closed, want)
	}
	data, err := os.ReadFile(filepath.Join(f.root, "FEATURES"))
	if err != nil {
		t.Fatalf("read FEATURES: %v", err)
	}
	wantFile := `{"closed":[{"name":"swr","actor":"dev","closed_at":"2026-10-15T12:00:00+0300"},` +
		`{"name":"web","actor":"lead","closed_at":"2026-10-15T12:00:00+0300"}]}` + "\n"
	if string(data) != wantFile {
		t.Fatalf("FEATURES = %q, want the sorted record with one trailing newline", data)
	}
}
