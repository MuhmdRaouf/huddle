package shared

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// TestSpoolLineRoundTripsFixtureLines reads every line of the spool fixtures, decodes it
// into a SpoolLine, and proves the line keeps its own bytes: MarshalJSON hands back
// exactly the line jsfmt re-encodes, and Field answers each key's value with the same
// shape and text the decoded line holds.
func TestSpoolLineRoundTripsFixtureLines(t *testing.T) {
	files := fixtureSpoolFiles(t)
	if len(files) == 0 {
		t.Fatal("no spool fixtures found")
	}
	checked := 0
	for _, file := range files {
		for _, text := range fixtureSpoolLines(t, file) {
			reference, err := jsfmt.DecodeValue([]byte(text))
			if err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			if reference.Kind() != jsfmt.KindObject {
				t.Fatalf("%s: fixture line is not an object", file)
			}
			var line SpoolLine
			if err := json.Unmarshal([]byte(text), &line); err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			got, err := domain.MarshalCompactJSON(line)
			if err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			if want := reference.EncodeJSON(); string(got) != want {
				t.Errorf("%s: marshalled\n%s\nwant\n%s", file, got, want)
			}
			keys, fields := reference.Keys(), reference.Fields()
			for i, key := range keys {
				field := line.Field(key)
				if field == nil {
					t.Errorf("%s: Field(%q) = nil, want the line's own value", file, key)
					continue
				}
				if field.Kind() != fields[i].Kind() || field.EncodeJSON() != fields[i].EncodeJSON() {
					t.Errorf("%s: Field(%q) = %s (%v), want %s (%v)",
						file, key, field.EncodeJSON(), field.Kind(), fields[i].EncodeJSON(), fields[i].Kind())
				}
			}
			if line.Field("no_such_key_in_any_fixture_line") != nil {
				t.Errorf("%s: Field of a key the line does not carry answered non-nil", file)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("spool fixtures hold no lines")
	}
}

// fixtureSpoolFiles lists the spool fixtures: the fixture spool and the legacy home's
// spool days, every line a reader can meet.
func fixtureSpoolFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "testdata", "fixture", "spool.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := filepath.Glob(filepath.Join("..", "testdata", "legacy_home", "spool", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return append(files, legacy...)
}

// fixtureSpoolLines reads one fixture's lines, blanks dropped.
func fixtureSpoolLines(t *testing.T, file string) []string {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
