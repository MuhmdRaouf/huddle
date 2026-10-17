package kanbantab

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	"github.com/MuhmdRaouf/huddle/internal/kanban/server"
)

// The key a board's row carries: the repository's identity — the normalised
// origin remote — so clones of one repository answer one board whatever path
// each sits in, and a repo with no origin keeps the key its path decides.

// listedBoards answers the rows /boards lists.
func listedBoards(t *testing.T, tab *Tab) []boardRow {
	t.Helper()
	status, body := answer(t, tab, http.MethodGet, Prefix+"/boards")
	if status != http.StatusOK {
		t.Fatalf("/boards = %d %s, want 200", status, body)
	}
	var listed boardsAnswer
	if err := json.Unmarshal([]byte(body), &listed); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return listed.Boards
}

func TestTwoClonesOfOneRepositoryShareOneKey(t *testing.T) {
	home := t.TempDir()
	repoA := newRepo(t, home, "alpha")
	repoB := newRepo(t, home, "beta")
	origins := map[string]string{
		repoA: "git@github.com:MuhmdRaouf/x.git",
		repoB: "https://github.com/MuhmdRaouf/x",
	}
	tab := Mount(context.Background(), Options{
		Roots: staticRoots(repoA, repoB),
		Origin: func(ctx context.Context, repoRoot string) string {
			return origins[repoRoot]
		},
	})
	rows := listedBoards(t, tab)
	if len(rows) != 1 {
		t.Fatalf("/boards listed %d rows (%v), want the two clones folded into one", len(rows), rows)
	}
	key := board.RepoKey("git@github.com:MuhmdRaouf/x.git", server.KeyOf(boardRoot(repoA)))
	want := boardRow{Key: key, Root: resolvedRoot(t, boardRoot(repoA)), Name: "alpha"}
	if rows[0] != want {
		t.Errorf("the shared row = %+v, want %+v", rows[0], want)
	}
	// the shared key answers the board's API: the clones name one board
	if status, body := answer(t, tab, http.MethodGet, Prefix+"/b/"+key+"/api/board"); status != http.StatusOK {
		t.Fatalf("the board through the shared key = %d %s, want 200", status, body)
	}
}

func TestARepoWithNoOriginKeepsThePathKey(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "alpha")
	tab := Mount(context.Background(), Options{
		Roots:  staticRoots(repo),
		Origin: func(context.Context, string) string { return "" },
	})
	rows := listedBoards(t, tab)
	key := server.KeyOf(boardRoot(repo))
	if len(rows) != 1 || rows[0].Key != key {
		t.Fatalf("/boards listed %+v, want one row keyed %s", rows, key)
	}
	if status, body := answer(t, tab, http.MethodGet, Prefix+"/b/"+key+"/api/board"); status != http.StatusOK {
		t.Fatalf("the board through the path key = %d %s, want 200", status, body)
	}
}

// TestOriginReadsTheRepositoryConfig runs the default Origin: git's own config
// answers the origin URL, and the key follows it; a repo with no origin keeps
// the path key.
func TestOriginReadsTheRepositoryConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	home := t.TempDir()
	withOrigin := gitRepo(t, home, "alpha", "git@github.com:MuhmdRaouf/x.git")
	withoutOrigin := newRepo(t, home, "beta")

	tab := Mount(context.Background(), Options{Roots: staticRoots(withOrigin, withoutOrigin)})
	rows := listedBoards(t, tab)
	if len(rows) != 2 {
		t.Fatalf("/boards listed %d rows (%v), want one per repo", len(rows), rows)
	}
	// a different spelling of the same origin: the key proves normalisation
	originKey := board.RepoKey("https://github.com/MuhmdRaouf/x", server.KeyOf(boardRoot(withOrigin)))
	pathKey := server.KeyOf(boardRoot(withoutOrigin))
	want := []boardRow{
		{Key: originKey, Root: resolvedRoot(t, boardRoot(withOrigin)), Name: "alpha"},
		{Key: pathKey, Root: resolvedRoot(t, boardRoot(withoutOrigin)), Name: "beta"},
	}
	for i, w := range want {
		if rows[i] != w {
			t.Errorf("row %d = %+v, want %+v", i, rows[i], w)
		}
	}
}

// gitRepo answers one repo git itself initialised with an origin remote,
// holding a board at .agents/kanban.
func gitRepo(t *testing.T, home, name, origin string) string {
	t.Helper()
	repo := filepath.Join(home, name)
	run := func(args ...string) {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q", repo)
	run("-C", repo, "remote", "add", "origin", origin)
	if _, err := board.Init(context.Background(), boardRoot(repo), "", pinnedNow()); err != nil {
		t.Fatal(err)
	}
	return repo
}
