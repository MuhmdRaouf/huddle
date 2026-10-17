package shared

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// The golden locks ScrubJSON's bytes: the hook writes redacted payloads to the spool, so
// the output must equal JSON.stringify(scrub(JSON.parse(x), limit)) exactly — same keys in
// the same order, numbers as String(n) prints them, strings with JSON.stringify's escaping.
// The golden holds every input with its limit; change the scrub and the golden in the same
// change.
func TestScrubJSONGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/scrub_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Limit int    `json:"limit"`
		In    string `json:"in"`
		Out   string `json:"out"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 8 {
		t.Fatalf("golden holds %d cases, want at least 8", len(cases))
	}
	for _, want := range cases {
		got, err := ScrubJSON([]byte(want.In), want.Limit)
		if err != nil {
			t.Errorf("ScrubJSON(%s): %v", want.In, err)
			continue
		}
		if got != want.Out {
			t.Errorf("ScrubJSON(%s, %d)\n got %s\nwant %s", want.In, want.Limit, got, want.Out)
		}
	}
}

func TestScrubValueLeavesInputUntouched(t *testing.T) {
	t.Run("the input value re-encodes to the same bytes after a scrub", func(t *testing.T) {
		const input = `{"api_key":"sk-abcdefgh123456","note":"Bearer abc123XYZ and more text than a small limit keeps around here","deep":[{"file_path":".env","command":"cat /x"}]}`
		value, err := jsfmt.DecodeValue([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		before := value.EncodeJSON()
		scrubbed := ScrubValue(value, 32)
		if after := value.EncodeJSON(); after != before {
			t.Errorf("input changed by the scrub:\n was %s\n now %s", before, after)
		}
		if scrubbed.EncodeJSON() == before {
			t.Error("scrubbed value equals the input")
		}
	})
}
