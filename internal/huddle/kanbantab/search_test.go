package kanbantab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	"github.com/MuhmdRaouf/huddle/internal/kanban/server"
)

func searchFor(t *testing.T, tab *Tab, repo, query string) (int, []searchHit) {
	t.Helper()
	path := Prefix + "/b/" + server.KeyOf(boardRoot(repo)) + "/search?q=" + url.QueryEscape(query)
	status, body := answer(t, tab, http.MethodGet, path)
	var got searchAnswer
	if status == http.StatusOK {
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("decode %s: %v", body, err)
		}
	}
	return status, got.Hits
}

// A search covers the cards of every board of the repo, an archived board's
// included, matching the id, title, summary, board name, tags and body without
// regard to case; each hit names its board and whether it is archived.
func TestSearchFindsCardsOfArchivedBoards(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "alpha")
	writeCard(t, repo, "ready", "LOG-1", "login", "Passkey prompt", "text")
	writeCard(t, repo, "done", "OLD-1", "old", "Retire the cookie", "mentions a Passkey in the body")
	writeCard(t, repo, "review", "OLD-2", "old", "Unrelated", "nothing here")
	ctx := context.Background()
	b := board.Board{Root: boardRoot(repo), Now: pinnedNow}
	if _, err := b.CloseFeature(ctx, board.Args{Actor: "owner", ID: "old"}); err != nil {
		t.Fatal(err)
	}
	tab := Mount(ctx, Options{Roots: staticRoots(repo), Now: pinnedNow})
	t.Cleanup(tab.Stop)

	status, hits := searchFor(t, tab, repo, "passkey")
	if status != http.StatusOK {
		t.Fatalf("search = %d, want 200", status)
	}
	want := []searchHit{
		{ID: "LOG-1", Title: "Passkey prompt", Column: "ready", Board: "login"},
		{ID: "OLD-1", Title: "Retire the cookie", Column: "done", Board: "old", Archived: true},
	}
	if len(hits) != len(want) {
		t.Fatalf("hits = %+v, want %+v", hits, want)
	}
	for i, w := range want {
		if hits[i] != w {
			t.Errorf("hit %d = %+v, want %+v", i, hits[i], w)
		}
	}
	if _, hits := searchFor(t, tab, repo, "no such words"); len(hits) != 0 {
		t.Errorf("hits for words no card holds = %+v, want none", hits)
	}
}

func TestSearchRefusesAnEmptyQuery(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "alpha")
	tab := Mount(context.Background(), Options{Roots: staticRoots(repo), Now: pinnedNow})
	t.Cleanup(tab.Stop)
	if status, _ := searchFor(t, tab, repo, "  "); status != http.StatusBadRequest {
		t.Fatalf("search for blanks = %d, want 400", status)
	}
}

func TestSearchOfAnUnknownKeyIs404AndTakesOnlyGET(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "alpha")
	tab := Mount(context.Background(), Options{Roots: staticRoots(repo), Now: pinnedNow})
	t.Cleanup(tab.Stop)
	if status, body := answer(t, tab, http.MethodGet, Prefix+"/b/nope/search?q=x"); status != http.StatusNotFound {
		t.Fatalf("search of an unknown key = %d %s, want 404", status, body)
	}
	key := server.KeyOf(boardRoot(repo))
	if status, body := answer(t, tab, http.MethodPost, Prefix+"/b/"+key+"/search?q=x"); status != http.StatusMethodNotAllowed {
		t.Fatalf("POST search = %d %s, want 405", status, body)
	}
}

// Only a search reads card files: state reads and the boards answer decide
// from the snapshot the board's app already holds.
func TestAStateReadWithNoSearchOrCountsQueryReadsNoCardFile(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "alpha")
	writeCard(t, repo, "ready", "LOG-1", "login", "one", "text")
	tab := Mount(context.Background(), Options{Roots: staticRoots(repo), Now: pinnedNow})
	t.Cleanup(tab.Stop)
	var reads int
	tab.readCards = func(b board.Board) []cardText {
		reads++
		return readCards(b)
	}
	key := server.KeyOf(boardRoot(repo))
	for range 3 {
		for _, path := range []string{"/api/board", "/boards"} {
			if status, body := answer(t, tab, http.MethodGet, Prefix+"/b/"+key+path); status != http.StatusOK {
				t.Fatalf("%s = %d %s, want 200", path, status, body)
			}
		}
	}
	if reads != 0 {
		t.Fatalf("%d card reads on state and boards reads, want 0", reads)
	}
	if status, _ := searchFor(t, tab, repo, "one"); status != http.StatusOK || reads != 1 {
		t.Fatalf("search = %d with %d card reads, want 200 with 1", status, reads)
	}
}
