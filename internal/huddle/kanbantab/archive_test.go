package kanbantab

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	"github.com/MuhmdRaouf/huddle/internal/kanban/server"
)

// writeDoneCard lands a done card of the board finished the given time before
// the tab's clock.
func writeDoneCard(t *testing.T, repo, id string, ago time.Duration) {
	t.Helper()
	text := "---\nid: " + id + "\ntitle: \"" + id + " title\"\ntype: docs\ncluster: kanban\n" +
		"summary: \"one\"\nfinished_at: " + pinnedNow().Add(-ago).Format("2006-01-02T15:04:05-0700") +
		"\n---\n\n# " + id + "\n\nBody text.\n"
	if err := os.WriteFile(filepath.Join(boardRoot(repo), "done", id+".md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Reading a board's state archives the board once its cards have all been done
// for 24 hours, as the system actor; a board younger than that stays open.
func TestReadingABoardArchivesItWhenItsCardsAreDone(t *testing.T) {
	home := t.TempDir()
	young := newRepo(t, home, "young")
	writeDoneCard(t, young, "TAB-1", 23*time.Hour+59*time.Minute)
	old := newRepo(t, home, "old")
	writeDoneCard(t, old, "TAB-1", 24*time.Hour)
	tab := Mount(context.Background(), Options{Roots: staticRoots(young, old), Now: pinnedNow})
	t.Cleanup(tab.Stop)

	for _, repo := range []string{young, old} {
		key := server.KeyOf(boardRoot(repo))
		if status, body := answer(t, tab, http.MethodGet, Prefix+"/b/"+key+"/api/board"); status != http.StatusOK {
			t.Fatalf("%s state = %d %s, want 200", repo, status, body)
		}
	}
	if _, err := os.Stat(filepath.Join(boardRoot(young), "FEATURES")); err == nil {
		t.Fatal("the board done for under 24 hours was archived")
	}
	data, err := os.ReadFile(filepath.Join(boardRoot(old), "FEATURES"))
	if err != nil || !strings.Contains(string(data), `"name":"kanban"`) || !strings.Contains(string(data), `"actor":"system"`) {
		t.Fatalf("FEATURES of the board done for 24 hours = %q, %v; want kanban closed by system", data, err)
	}
}

// A state read decides from the snapshot the board's app already holds: while
// no board has all its cards done for 24 hours, the read never reaches the
// board's card files. ArchiveDue is the only reader of them on this path, so
// its calls count the reads.
func TestAStateReadWithNothingDueReadsNoCardFile(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "young")
	writeDoneCard(t, repo, "TAB-1", 23*time.Hour)
	writeTicket(t, repo, "ready", "TAB-2")
	tab := Mount(context.Background(), Options{Roots: staticRoots(repo), Now: pinnedNow})
	t.Cleanup(tab.Stop)
	var calls int
	tab.archive = func(ctx context.Context, b board.Board) ([]string, error) {
		calls++
		return b.ArchiveDue(ctx)
	}
	key := server.KeyOf(boardRoot(repo))
	for range 3 {
		if status, body := answer(t, tab, http.MethodGet, Prefix+"/b/"+key+"/api/board"); status != http.StatusOK {
			t.Fatalf("state = %d %s, want 200", status, body)
		}
	}
	if calls != 0 {
		t.Fatalf("ArchiveDue ran %d times on a board with nothing due, want 0", calls)
	}
}

// A due board reaches ArchiveDue, which re-checks under the write's hold.
func TestAStateReadWithABoardDueRunsTheArchive(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "old")
	writeDoneCard(t, repo, "TAB-1", 25*time.Hour)
	tab := Mount(context.Background(), Options{Roots: staticRoots(repo), Now: pinnedNow})
	t.Cleanup(tab.Stop)
	var calls int
	tab.archive = func(ctx context.Context, b board.Board) ([]string, error) {
		calls++
		return b.ArchiveDue(ctx)
	}
	key := server.KeyOf(boardRoot(repo))
	if status, body := answer(t, tab, http.MethodGet, Prefix+"/b/"+key+"/api/board"); status != http.StatusOK {
		t.Fatalf("state = %d %s, want 200", status, body)
	}
	if calls != 1 {
		t.Fatalf("ArchiveDue ran %d times on a due board, want 1", calls)
	}
}
