package hub

// The needs-you helpers and the permission row reads at their edges: an item of another kind,
// an input that is not JSON or is empty, a cut that lands inside a multi-byte rune, and a
// refusal that names the item it could not act on.

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTheMissingItemRefusalNamesItsItem(t *testing.T) {
	if msg := (&MissingItemError{ID: 7}).Error(); msg != "no open item 7" {
		t.Errorf("the refusal = %q, want it to name item 7", msg)
	}
}

func TestACutInputBacksOffToARuneStart(t *testing.T) {
	kept, truncated := cutInput(strings.Repeat("a", InputLimit))
	if truncated || len(kept) != InputLimit {
		t.Errorf("an input at the limit reads truncated=%v len=%d, want whole", truncated, len(kept))
	}
	// the limit lands inside a two-byte rune: the cut backs off to the rune's start, so the kept
	// text is whole runes
	kept, truncated = cutInput("a" + strings.Repeat("é", InputLimit))
	if !truncated || !validUTF8(kept) {
		t.Errorf("a cut through a rune: truncated=%v whole=%v", truncated, validUTF8(kept))
	}
	if len(kept) >= InputLimit {
		t.Errorf("the kept text is %d bytes, want the cut backed off below the limit", len(kept))
	}
}

func TestNormalizeInputAnswersCompactJSONOrTheTextItself(t *testing.T) {
	if got := normalizeInput(nil); got != "" {
		t.Errorf("no input = %q, want empty", got)
	}
	if got := normalizeInput([]byte("null")); got != "null" {
		t.Errorf("a null input = %q, want the text itself", got)
	}
	if got := normalizeInput([]byte("  spaced text  ")); got != "spaced text" {
		t.Errorf("a spaced text = %q, want it trimmed", got)
	}
	if got := normalizeInput([]byte(`{"b":1,"a":"two"}`)); got != `{"b":1,"a":"two"}` {
		t.Errorf("an object = %s, want the compact JSON with its key order kept", got)
	}
}

func TestInputStringsWalksTheWholeInput(t *testing.T) {
	if got := inputStrings(""); got != nil {
		t.Errorf("no input = %v, want none", got)
	}
	if got := inputStrings("bare command"); len(got) != 1 || got[0] != "bare command" {
		t.Errorf("a bare text = %v, want itself", got)
	}
	got := inputStrings(`{"command":"npm test","nested":{"path":"secrets/x"},"list":["a",7]}`)
	want := "npm test secrets/x a"
	if strings.Join(got, " ") != want {
		t.Errorf("inputStrings = %q, want %q", strings.Join(got, " "), want)
	}
}

func TestPermissionItemByIDAnswersNothingForAnotherKind(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	if err := db.Report(ctx, Report{SessionID: "s-q", LastEvent: "Notification",
		LastEventAt:  "2026-10-16T09:00:00.000Z",
		Notification: &Notification{Type: "elicitation_dialog", Message: "Pick one"}}); err != nil {
		t.Fatalf("report: %v", err)
	}
	items, err := db.Items(ctx, readNow)
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %v (%v), want the one question", items, err)
	}
	item, err := db.PermissionItemByID(ctx, items[0].ID, readNow)
	if err != nil || item != nil {
		t.Errorf("a question read as a permission = (%v, %v), want none", item, err)
	}
	if item, err = db.PermissionItemByID(ctx, 987654, readNow); err != nil || item != nil {
		t.Errorf("an unknown id = (%v, %v), want none", item, err)
	}
}

func TestRemoteAllowRefusesAnUnparsableListPattern(t *testing.T) {
	// a pattern of a shape the list never spelled matches nothing, so a typo refuses
	for _, pattern := range []string{"", "(npm)", "Bash(", "Bash)", "Bash(npm test*"} {
		if _, ok := RemoteAllow([]string{pattern}, "Bash", `{"command":"npm test"}`); ok {
			t.Errorf("pattern %q allowed the call", pattern)
		}
	}
}

func TestOpenedAtAndTheOpenReadFollowWhatIsStored(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	// a session nobody opened reads no opened moment, so its turn waits
	if err := db.Report(ctx, Report{SessionID: "s-o", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T09:00:00.000Z"}); err != nil {
		t.Fatalf("report: %v", err)
	}
	if at, err := db.openedAt(ctx, "s-o"); err != nil || !at.IsZero() {
		t.Errorf("openedAt = (%v, %v), want the zero time", at, err)
	}
	// the owner opens the session: opened_at moves forward, and a late read never moves it back
	if _, err := db.OpenTheSession(ctx, "s-o", "2026-10-16T09:05:00.000Z"); err != nil {
		t.Fatalf("open: %v", err)
	}
	if at, err := db.openedAt(ctx, "s-o"); err != nil || at.IsZero() {
		t.Errorf("openedAt after a read = (%v, %v), want the read's moment", at, err)
	}
	if _, err := db.OpenTheSession(ctx, "s-o", "2026-10-16T09:01:00.000Z"); err != nil {
		t.Fatalf("open again: %v", err)
	}
	if at, _ := db.openedAt(ctx, "s-o"); !at.Equal(time.Date(2026, 10, 16, 9, 5, 0, 0, time.UTC)) {
		t.Errorf("openedAt after a late read = %v, want it to stay 09:05", at)
	}
	// a session that is not registered was never opened
	if at, err := db.openedAt(ctx, "s-none"); err != nil || !at.IsZero() {
		t.Errorf("openedAt(s-none) = (%v, %v), want the zero time", at, err)
	}
}
