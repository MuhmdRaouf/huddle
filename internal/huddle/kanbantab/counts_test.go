package kanbantab

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	"github.com/MuhmdRaouf/huddle/internal/kanban/server"
)

// writeCard lands one ticket of the given board (its cluster) in a column.
func writeCard(t *testing.T, repo, col, id, cluster, title, body string) {
	t.Helper()
	text := "---\nid: " + id + "\ntitle: \"" + title + "\"\ntype: docs\ncluster: " + cluster + "\n" +
		"summary: \"one\"\n---\n\n# " + id + "\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(boardRoot(repo), col, id+".md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A repo's boards answer lists every board, open and archived, sorted by name,
// with two counts each: the cards in progress, and the cards waiting for the
// owner (review plus blocked). A registered board with no card is listed, and
// an archived board counts nothing.
func TestBoardCountsSplitInProgressAndWaitingForYou(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "alpha")
	writeCard(t, repo, "in-progress", "LOG-1", "login", "one", "text")
	writeCard(t, repo, "in-progress", "LOG-2", "login", "two", "text")
	writeCard(t, repo, "review", "LOG-3", "login", "three", "text")
	writeCard(t, repo, "blocked", "LOG-4", "login", "four", "text")
	writeCard(t, repo, "ready", "LOG-5", "login", "five", "text")
	writeCard(t, repo, "done", "LOG-6", "login", "six", "text")
	writeCard(t, repo, "review", "SRC-1", "search", "seven", "text")
	writeCard(t, repo, "in-progress", "OLD-1", "old", "eight", "text")
	ctx := context.Background()
	b := board.Board{Root: boardRoot(repo), Now: pinnedNow}
	if _, err := b.CreateBoard(ctx, board.Args{Actor: "owner", ID: "empty"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CloseFeature(ctx, board.Args{Actor: "owner", ID: "old"}); err != nil {
		t.Fatal(err)
	}
	tab := Mount(ctx, Options{Roots: staticRoots(repo), Now: pinnedNow})
	t.Cleanup(tab.Stop)

	status, body := answer(t, tab, http.MethodGet, Prefix+"/b/"+server.KeyOf(boardRoot(repo))+"/boards")
	if status != http.StatusOK {
		t.Fatalf("boards = %d %s, want 200", status, body)
	}
	var got struct {
		Boards []boardSummary `json:"boards"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	want := []boardSummary{
		{Name: "empty"},
		{Name: "login", InProgress: 2, Waiting: 2},
		{Name: "old", Archived: true},
		{Name: "search", Waiting: 1},
	}
	if len(got.Boards) != len(want) {
		t.Fatalf("boards = %s, want %+v", body, want)
	}
	for i, w := range want {
		if got.Boards[i] != w {
			t.Errorf("board %d = %+v, want %+v", i, got.Boards[i], w)
		}
	}
}

func TestBoardsOfAnUnknownKeyIs404(t *testing.T) {
	tab := Mount(context.Background(), Options{Roots: staticRoots(), Now: pinnedNow})
	t.Cleanup(tab.Stop)
	if status, body := answer(t, tab, http.MethodGet, Prefix+"/b/nope/boards"); status != http.StatusNotFound {
		t.Fatalf("boards of an unknown key = %d %s, want 404", status, body)
	}
}

func TestBoardsTakesOnlyGET(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "alpha")
	tab := Mount(context.Background(), Options{Roots: staticRoots(repo), Now: pinnedNow})
	t.Cleanup(tab.Stop)
	key := server.KeyOf(boardRoot(repo))
	if status, body := answer(t, tab, http.MethodPost, Prefix+"/b/"+key+"/boards"); status != http.StatusMethodNotAllowed {
		t.Fatalf("POST boards = %d %s, want 405", status, body)
	}
}
