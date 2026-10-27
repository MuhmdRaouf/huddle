package board

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// cardBytes answers every card file's bytes keyed by column and file name, so a
// test can hold a board's cards still while a feature closes.
func cardBytes(t *testing.T, f *fixture) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, col := range Columns {
		names, err := os.ReadDir(filepath.Join(f.root, col))
		if err != nil {
			t.Fatalf("read %s: %v", col, err)
		}
		for _, e := range names {
			data, err := os.ReadFile(filepath.Join(f.root, col, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			out[col+"/"+e.Name()] = data
		}
	}
	return out
}

// sameBytes fails when two card-file snapshots differ.
func sameBytes(t *testing.T, want, got map[string][]byte, what string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d card files, want %d", what, len(got), len(want))
	}
	for name, data := range want {
		if !bytes.Equal(got[name], data) {
			t.Errorf("%s: %s = %q, want %q", what, name, got[name], data)
		}
	}
}

func TestCloseFeatureScenarios(t *testing.T) {
	// the boards outlive each Given step, so they live in the test's own temp dir
	keep := t
	bdd.Feature(t, "Close a merged feature", func(f *bdd.F) {
		f.Scenario("closing a feature records who closed it and leaves the cards alone", func(s *bdd.S) {
			var fx *fixture
			var cards map[string][]byte
			s.Given("a board with three swr cards and one web card", func(t *testing.T) {
				fx = newFixtureAt(keep, filepath.Join(keep.TempDir(), "close"))
				fx.card("backlog", "SWR-1", "id: SWR-1\ntitle: \"one\"\ncluster: swr\ntype: docs")
				fx.card("ready", "SWR-2", "id: SWR-2\ntitle: \"two\"\ncluster: swr\ntype: docs")
				fx.card("testing", "SWR-3", "id: SWR-3\ntitle: \"three\"\ncluster: swr\ntype: docs")
				fx.card("backlog", "WEB-1", "id: WEB-1\ntitle: \"web\"\ncluster: web\ntype: docs")
				cards = cardBytes(t, fx)
			})
			s.When("lead closes swr", func(t *testing.T) {
				lines, err := fx.b.CloseFeature(context.Background(), Args{Actor: "lead", ID: "swr"})
				if err != nil {
					t.Fatalf("CloseFeature: %v", err)
				}
				if len(lines) != 1 || lines[0] != "feature swr closed: 3 cards archived" {
					t.Fatalf("CloseFeature lines = %q, want the one archived line", lines)
				}
			})
			s.Then("ClosedFeatures answers swr, closed by lead", func(t *testing.T) {
				closed := fx.b.ClosedFeatures()
				if len(closed) != 1 {
					t.Fatalf("ClosedFeatures = %+v, want swr alone", closed)
				}
				if want := (ClosedFeature{Name: "swr", Actor: "lead", ClosedAt: stamp}); closed[0] != want {
					t.Fatalf("closed feature = %+v, want %+v", closed[0], want)
				}
			})
			s.And("the LOG's last line is the close", func(t *testing.T) {
				entry := fx.lastLog()
				if entry["event"] != "feature-close" || entry["feature"] != "swr" || entry["actor"] != "lead" {
					t.Fatalf("last LOG line = %v, want a feature-close by lead naming swr", entry)
				}
			})
			s.And("list answers the web card alone", func(t *testing.T) {
				got := mustCards(t, fx.list(""))
				if len(got) != 1 || got[0].ID != "WEB-1" {
					t.Fatalf("list after the close = %+v, want WEB-1 alone", got)
				}
			})
			s.And("status counts leave the swr cards out", func(t *testing.T) {
				counts := map[string]int{}
				for _, c := range mustStatus(t, fx).Counts {
					counts[c.Column] = c.Count
				}
				if counts["backlog"] != 1 || counts["ready"] != 0 || counts["testing"] != 0 {
					t.Fatalf("counts after the close = %v, want backlog 1, ready 0, testing 0", counts)
				}
			})
			s.And("show answers an swr card with its feature closed", func(t *testing.T) {
				sh, err := fx.b.Show(context.Background(), "SWR-2")
				if err != nil {
					t.Fatalf("Show: %v", err)
				}
				if sh.Card.Column != "ready" {
					t.Fatalf("SWR-2 column = %q, want ready: closing moves no card", sh.Card.Column)
				}
				if !sh.Card.FeatureClosed {
					t.Fatalf("SWR-2 FeatureClosed = false, want true while swr is closed")
				}
			})
			s.And("every card file keeps its bytes", func(t *testing.T) {
				sameBytes(t, cards, cardBytes(t, fx), "after the close")
			})
		})

		f.Scenario("reopening a feature clears the record", func(s *bdd.S) {
			var fx *fixture
			var cards map[string][]byte
			var wantList []Card
			var wantCounts []ColumnCount
			s.Given("a board with three swr cards whose feature is closed", func(t *testing.T) {
				fx = newFixtureAt(keep, filepath.Join(keep.TempDir(), "reopen"))
				fx.card("backlog", "SWR-1", "id: SWR-1\ntitle: \"one\"\ncluster: swr\ntype: docs")
				fx.card("ready", "SWR-2", "id: SWR-2\ntitle: \"two\"\ncluster: swr\ntype: docs")
				fx.card("testing", "SWR-3", "id: SWR-3\ntitle: \"three\"\ncluster: swr\ntype: docs")
				wantList = mustCards(t, fx.list(""))
				wantCounts = mustStatus(t, fx).Counts
				if _, err := fx.b.CloseFeature(context.Background(), Args{Actor: "lead", ID: "swr"}); err != nil {
					t.Fatalf("CloseFeature: %v", err)
				}
				cards = cardBytes(t, fx)
			})
			s.When("lead reopens swr", func(t *testing.T) {
				lines, err := fx.b.ReopenFeature(context.Background(), Args{Actor: "lead", ID: "swr"})
				if err != nil {
					t.Fatalf("ReopenFeature: %v", err)
				}
				if len(lines) != 1 || lines[0] != "feature swr reopened: 3 cards back" {
					t.Fatalf("ReopenFeature lines = %q, want the one back line", lines)
				}
			})
			s.Then("ClosedFeatures answers none", func(t *testing.T) {
				if closed := fx.b.ClosedFeatures(); len(closed) != 0 {
					t.Fatalf("ClosedFeatures = %+v, want none", closed)
				}
			})
			s.And("the FEATURES file is gone", func(t *testing.T) {
				if _, err := os.Stat(filepath.Join(fx.root, "FEATURES")); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("FEATURES after the reopen = %v, want it gone", err)
				}
			})
			s.And("the LOG's last line is the reopen", func(t *testing.T) {
				entry := fx.lastLog()
				if entry["event"] != "feature-reopen" || entry["feature"] != "swr" || entry["actor"] != "lead" {
					t.Fatalf("last LOG line = %v, want a feature-reopen by lead naming swr", entry)
				}
			})
			s.And("list and status answer as before the close", func(t *testing.T) {
				gotList := mustCards(t, fx.list(""))
				if !reflect.DeepEqual(gotList, wantList) {
					t.Fatalf("list after the reopen = %+v, want %+v", gotList, wantList)
				}
				if gotCounts := mustStatus(t, fx).Counts; !reflect.DeepEqual(gotCounts, wantCounts) {
					t.Fatalf("counts after the reopen = %+v, want %+v", gotCounts, wantCounts)
				}
			})
			s.And("an swr card shows its feature open again", func(t *testing.T) {
				sh, err := fx.b.Show(context.Background(), "SWR-1")
				if err != nil {
					t.Fatalf("Show: %v", err)
				}
				if sh.Card.FeatureClosed {
					t.Fatalf("SWR-1 FeatureClosed = true, want false after the reopen")
				}
			})
			s.And("every card file's bytes are as before the close", func(t *testing.T) {
				sameBytes(t, cards, cardBytes(t, fx), "after the reopen")
			})
		})
	})
}
