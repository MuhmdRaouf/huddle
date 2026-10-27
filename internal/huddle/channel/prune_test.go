package channel

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
)

func TestHubPruneEventsPrunesEveryChannelAndSkipsOneThatWillNotOpen(t *testing.T) {
	ctx := bg
	h := NewHub(t.TempDir(), false)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	youngTS := now.Add(-time.Hour).UTC().Format("2006-01-02T15:04:05Z")
	open := func(name string) *Channel {
		t.Helper()
		st, err := store.Open(ctx, h.File(name))
		if err != nil {
			t.Fatalf("open the store of %s: %v", name, err)
		}
		ch, err := Open(ctx, name, st)
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		t.Cleanup(func() { _ = ch.Close(ctx) })
		return ch
	}
	seed := func(ch *Channel, age time.Duration) {
		t.Helper()
		old := now.Add(-age).UTC().Format("2006-01-02T15:04:05Z")
		if err := ch.dbRun(ctx, "INSERT INTO events (ts, from_name, topic) VALUES (?, 'seed', 'chat')", old); err != nil {
			t.Fatalf("seed %s: %v", old, err)
		}
	}
	one, two := open("one"), open("two")
	seed(one, 100*24*time.Hour)
	seed(one, time.Hour)
	seed(two, 100*24*time.Hour)
	seed(two, time.Hour)
	if err := os.WriteFile(h.File("bad"), []byte("this is not a database"), 0o600); err != nil {
		t.Fatalf("write the broken file: %v", err)
	}

	total, err := h.PruneEvents(ctx, now)
	if err == nil {
		t.Fatal("PruneEvents answered no error, want the broken channel named")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("the prune error %q does not name the broken channel", err)
	}
	if total != 2 {
		t.Fatalf("PruneEvents pruned %d rows, want 2", total)
	}
	for _, ch := range map[string]*Channel{"one": one, "two": two} {
		row, err := ch.dbGet(ctx, "SELECT ts FROM events")
		if err != nil {
			t.Fatalf("read what is left: %v", err)
		}
		if row["ts"] != youngTS {
			t.Errorf("%s keeps %v, want only the young event", ch.Name, row["ts"])
		}
	}
	raw, err := os.ReadFile(h.File("bad"))
	if err != nil || string(raw) != "this is not a database" {
		t.Errorf("the broken file changed (%v)", err)
	}
}
