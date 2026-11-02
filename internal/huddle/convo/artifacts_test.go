package convo

// The artifacts of one session: the Artifact calls its main transcript answers, one row per
// artifact id at its latest publish, in the order the ids were first published.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// wantArtifactRows is what every version's artifacts fixture yields: the revised report at its
// second version ahead of the board, with the failed call and the non-claude.ai url left out.
func wantArtifactRows() []Artifact {
	return []Artifact{
		{ID: "fx-report", URL: "https://claude.ai/artifact/fx-report", Title: "Fixture report, revised", Version: "2", At: "2026-01-01T09:00:06.000Z"},
		{ID: "fx-board", URL: "https://claude.ai/artifact/fx-board", Title: "Fixture board", Version: "1", At: "2026-01-01T09:00:04.000Z"},
	}
}

func TestArtifactsTwoPublishesOfOneArtifactGiveOneRowAtItsLastVersion(t *testing.T) {
	for _, version := range fixtureVersions {
		t.Run(version, func(t *testing.T) {
			file, err := os.Open(filepath.Join("testdata", "cc", version, "artifacts.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			rows, err := Artifacts(file)
			if err != nil {
				t.Fatalf("artifacts: %v", err)
			}
			if !reflect.DeepEqual(rows, wantArtifactRows()) {
				t.Fatalf("artifacts:\ngot  %+v\nwant %+v", rows, wantArtifactRows())
			}
		})
	}
}

func TestArtifactsAnIncompleteLastLineCountsForNothing(t *testing.T) {
	whole, err := os.ReadFile(filepath.Join("testdata", "cc", "2.1.309", "artifacts.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(whole), "\n")
	// the report's second publish, cut mid-line, leaves the first publish standing
	cut := strings.Join(lines[:4], "") + lines[5][:len(lines[5])/2]
	rows, err := Artifacts(strings.NewReader(cut))
	if err != nil {
		t.Fatalf("artifacts: %v", err)
	}
	want := []Artifact{wantArtifactRows()[1]}
	want = append([]Artifact{{ID: "fx-report", URL: "https://claude.ai/artifact/fx-report", Title: "Fixture report", Version: "1", At: "2026-01-01T09:00:02.000Z"}}, want...)
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("artifacts:\ngot  %+v\nwant %+v", rows, want)
	}
}

func TestArtifactsATranscriptWithNoArtifactCallsAnswersNone(t *testing.T) {
	rows, err := Artifacts(strings.NewReader(`{"type":"user","message":{"role":"user","content":"hello"}}` + "\n"))
	if err != nil {
		t.Fatalf("artifacts: %v", err)
	}
	if rows == nil || len(rows) != 0 {
		t.Fatalf("got %+v, want an empty list", rows)
	}
}
