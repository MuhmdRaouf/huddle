package channel

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Every channel, each its own SQLite file under the hub's directory, opened on first use and kept
// open; the server-wide knowledge opened with the first channel and shared by all of them.
func TestHub(t *testing.T) {
	t.Run("channels live as files, opened once and kept open; the shared knowledge serves every channel", func(t *testing.T) {
		dir := t.TempDir()
		h := NewHub(dir, true)
		t.Cleanup(func() { _ = h.Close(bg) })
		if h.Where() != dir {
			t.Errorf("where = %q, want %q", h.Where(), dir)
		}
		if got := h.File("shop"); got != filepath.Join(dir, "channels", "shop.db") {
			t.Errorf("file = %q, want %q", got, filepath.Join(dir, "channels", "shop.db"))
		}
		if names, err := h.Names(bg); err != nil || len(names) != 0 {
			t.Errorf("names = %v (%v), want none", names, err)
		}
		ch, err := h.Get(bg, "shop")
		if err != nil {
			t.Fatal(err)
		}
		if ch == nil {
			t.Fatal("get answered no channel")
		}
		if _, err := ch.Join(bg, "a", JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		g, err := Remember(bg, ch, "a", RememberArgs{Kind: "fact", Title: "the box has 64 GB", Body: "plenty", Scope: "server"})
		if err != nil {
			t.Fatal(err)
		}
		exists, err := h.Exists(bg, "shop")
		if err != nil || !exists {
			t.Errorf("exists(shop) = %v (%v), want true", exists, err)
		}
		names, err := h.Names(bg)
		if err != nil || !slices.Equal(names, []string{"shop"}) {
			t.Errorf("names = %v (%v), want shop", names, err)
		}
		b, err := h.Get(bg, "beta")
		if err != nil {
			t.Fatal(err)
		}
		if b == nil {
			t.Fatal("get answered no channel")
		}
		if _, err := b.Join(bg, "z", JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		fromB, err := Recall(bg, b, "box", RecallOpts{})
		if err != nil {
			t.Fatal(err)
		}
		gs, ok := g.(RememberShared)
		if !ok {
			t.Fatalf("remember = %#v, want a server-wide entry", g)
		}
		if len(fromB) != 1 || fromB[0].ID != gs.ID {
			t.Errorf("recall from beta = %+v, want the server-wide entry", fromB)
		}
		again, err := h.Get(bg, "shop")
		if err != nil {
			t.Fatal(err)
		}
		if again != ch {
			t.Error("a channel opened twice is two channels")
		}
	})

	t.Run("a name that is not a channel name is refused; create=false answers 404", func(t *testing.T) {
		dir := t.TempDir()
		h := NewHub(dir, true)
		t.Cleanup(func() { _ = h.Close(bg) })
		if _, err := h.Get(bg, "Bad Name"); err == nil || errStatus(err) != 400 {
			t.Errorf("bad name error = %v, want 400", err)
		} else if !strings.Contains(err.Error(), "channel name must match") {
			t.Errorf("bad name error = %v, want the pattern in the message", err)
		}
		readOnly := NewHub(dir, false)
		t.Cleanup(func() { _ = readOnly.Close(bg) })
		if _, err := readOnly.Get(bg, "ghost"); err == nil || errStatus(err) != 404 {
			t.Errorf("missing channel error = %v, want 404", err)
		}
		if _, err := h.GetExisting(bg, "ghost"); err == nil || errStatus(err) != 404 {
			t.Errorf("get existing error = %v, want 404", err)
		}
		names, err := readOnly.Names(bg)
		if err != nil || len(names) != 0 {
			t.Errorf("names = %v (%v), want none", names, err)
		}
	})

	t.Run("names lists only channel files, sorted", func(t *testing.T) {
		dir := t.TempDir()
		h := NewHub(dir, true)
		t.Cleanup(func() { _ = h.Close(bg) })
		for _, n := range []string{"beta", "shop"} {
			c, err := h.Get(bg, n)
			if err != nil {
				t.Fatal(err)
			}
			if c == nil {
				t.Fatalf("get(%s) answered no channel", n)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "channels", "stray.txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "channels", "-bad.db"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		names, err := h.Names(bg)
		if err != nil || !slices.Equal(names, []string{"beta", "shop"}) {
			t.Errorf("names = %v (%v), want beta and shop", names, err)
		}
	})

	t.Run("list shows every channel: title, the live sessions, the stats", func(t *testing.T) {
		dir := t.TempDir()
		h := NewHub(dir, true)
		t.Cleanup(func() { _ = h.Close(bg) })
		shop, err := h.Get(bg, "shop")
		if err != nil {
			t.Fatal(err)
		}
		if shop == nil {
			t.Fatal("get answered no channel")
		}
		if _, err := shop.Join(bg, "a", JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		if _, err := shop.Publish(bg, "a", PublishArgs{Topic: "n.x", Msg: "hello"}); err != nil {
			t.Fatal(err)
		}
		beta, err := h.Get(bg, "beta")
		if err != nil {
			t.Fatal(err)
		}
		if beta == nil {
			t.Fatal("get answered no channel")
		}
		if _, err := beta.Join(bg, "z", JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		if _, err := beta.Leave(bg, "z", "done"); err != nil {
			t.Fatal(err)
		}
		entries, err := h.List(bg)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 2 {
			t.Fatalf("entries = %+v, want beta and shop", entries)
		}
		if entries[0].Name != "beta" || entries[1].Name != "shop" {
			t.Errorf("entries = %v %v, want beta then shop", entries[0].Name, entries[1].Name)
		}
		shopEntry := entries[1]
		if shopEntry.Title != "shop" || len(shopEntry.Sessions) != 1 || shopEntry.Sessions[0].Name != "a" {
			t.Errorf("shop entry = %+v", shopEntry)
		}
		if shopEntry.Stats.Events == 0 {
			t.Errorf("shop stats = %+v, want the published event counted", shopEntry.Stats)
		}
		if len(entries[0].Sessions) != 0 {
			t.Errorf("beta entry = %+v, want the left session gone", entries[0])
		}
	})

	t.Run("a channel file that is not a database answers its error; a channels path that is a file lists none", func(t *testing.T) {
		dir := t.TempDir()
		h := NewHub(dir, true)
		t.Cleanup(func() { _ = h.Close(bg) })
		if err := os.Mkdir(filepath.Join(dir, "channels", "broken.db"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := h.Get(bg, "broken"); err == nil {
			t.Error("get over a directory answered no error")
		}
		flat := t.TempDir()
		if err := os.WriteFile(filepath.Join(flat, "channels"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		flatHub := NewHub(flat, true)
		t.Cleanup(func() { _ = flatHub.Close(bg) })
		names, err := flatHub.Names(bg)
		if err != nil || len(names) != 0 {
			t.Errorf("names under a file = %v (%v), want none", names, err)
		}
	})

	t.Run("close ends every channel and the shared store, and the hub opens again", func(t *testing.T) {
		dir := t.TempDir()
		h := NewHub(dir, true)
		ch, err := h.Get(bg, "shop")
		if err != nil {
			t.Fatal(err)
		}
		if ch == nil {
			t.Fatal("get answered no channel")
		}
		if _, err := ch.Join(bg, "x", JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		if _, err := Remember(bg, ch, "x", RememberArgs{Kind: "fact", Title: "t", Body: "b", Scope: "server"}); err != nil {
			t.Fatal(err)
		}
		if err := h.Close(bg); err != nil {
			t.Fatal(err)
		}
		names, err := h.Names(bg)
		if err != nil || !slices.Equal(names, []string{"shop"}) {
			t.Errorf("names after close = %v (%v)", names, err)
		}
		exists, err := h.Exists(bg, "shop")
		if err != nil || !exists {
			t.Errorf("exists after close = %v (%v), want the file to count", exists, err)
		}
		exists, err = h.Exists(bg, "ghost")
		if err != nil || exists {
			t.Errorf("exists(ghost) = %v (%v), want false", exists, err)
		}
		reopened, err := h.Get(bg, "shop")
		if err != nil {
			t.Fatal(err)
		}
		if reopened == nil {
			t.Fatal("get answered no channel")
		}
		r, err := Recall(bg, reopened, "t", RecallOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if len(r) != 1 {
			t.Errorf("recall after reopen = %+v, want the server-wide entry", r)
		}
		if err := h.Close(bg); err != nil {
			t.Fatal(err)
		}
	})
}

// The store's earlier spelling moves onto knowledge.db once: a directory a release before the
// rename wrote keeps its rows, and a second open moves nothing.
func TestHubAdoptsTheEarlierKnowledgeSpellingOnce(t *testing.T) {
	dir := t.TempDir()
	earlier, err := OpenShared(bg, filepath.Join(dir, "shared.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := insertShared(bg, earlier, sharedEntry{
		by: "a", kind: "fact", title: "kept by the rename", body: "the row the earlier spelling holds",
		tags: []string{}, refs: []string{}, origin: "shop", createdAt: "2026-10-01T00:00:00.000Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := earlier.Close(); err != nil {
		t.Fatal(err)
	}
	h := NewHub(dir, true)
	t.Cleanup(func() { _ = h.Close(bg) })
	ch, err := h.Get(bg, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Join(bg, "a", JoinOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "shared.db")); !os.IsNotExist(err) {
		t.Errorf("shared.db stayed: %v", err)
	}
	rows, err := Recall(bg, ch, "rename", RecallOpts{})
	if err != nil || len(rows) != 1 || rows[0].Title != "kept by the rename" {
		t.Fatalf("recall = %+v (%v), want the adopted entry", rows, err)
	}
	if err := h.Close(bg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "knowledge.db")); err != nil {
		t.Fatalf("the adopted store = knowledge.db: %v", err)
	}
}
