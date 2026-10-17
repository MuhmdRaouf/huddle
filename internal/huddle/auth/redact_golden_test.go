package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// TestRedactGolden locks Redact's bytes: a decoded request body, redacted and re-encoded,
// must equal JSON.stringify(auth.redact(JSON.parse(input))) exactly — the same keys in the
// same order, numbers as String(n) prints them, "<redacted>" literal (never <) and
// U+2028/U+2029 raw. The golden also records the run's secrets, so this test replays the
// same live set that wrote the expected bytes.
func TestRedactGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "redact_golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Root        string `json:"root"`
		InviteID    string `json:"invite_id"`
		InviteToken string `json:"invite_token"`
		MemberCred  string `json:"member_cred"`
		Code        string `json:"code"`
		Cases       []struct {
			In  string `json:"in"`
			Out string `json:"out"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	if len(golden.Cases) < 8 {
		t.Fatalf("golden holds %d cases, want at least 8", len(golden.Cases))
	}
	// The tokens are random, so the recorded run is replayed: New puts the root in live, then the
	// invite token, the member's credential and the code land in the order the recorded run
	// issued them.
	a := New(golden.Root, nil, nil, "")
	a.issued.add(golden.InviteID)
	a.live.add(golden.InviteToken)
	a.live.add(golden.MemberCred)
	a.live.add(golden.Code)
	for _, want := range golden.Cases {
		v, err := jsfmt.DecodeValue([]byte(want.In))
		if err != nil {
			t.Errorf("DecodeValue(%s): %v", want.In, err)
			continue
		}
		if got := a.Redact(v).EncodeJSON(); got != want.Out {
			t.Errorf("Redact(%s)\n got %s\nwant %s", want.In, got, want.Out)
		}
	}
}
