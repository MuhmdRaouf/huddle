package kanbantab

// BoardKey answers the one key the boards list gives a board's root, so a
// caller outside the tab names a board the way the list does and the two
// spellings can never drift apart.

import (
	"context"
	"path/filepath"
	"testing"
)

func TestBoardKeyAnswersTheKeyTheBoardsListGives(t *testing.T) {
	home := t.TempDir()
	repoA := newRepo(t, home, "alpha")
	repoB := newRepo(t, home, "beta")
	origins := map[string]string{
		repoA: "git@github.com:MuhmdRaouf/one.git",
		repoB: "https://github.com/MuhmdRaouf/two",
	}
	tab := Mount(context.Background(), Options{
		Roots: staticRoots(repoA, repoB),
		Origin: func(_ context.Context, repoRoot string) string {
			return origins[repoRoot]
		},
	})
	rows := tab.boardsOf(context.Background())
	if len(rows) != 2 {
		t.Fatalf("boardsOf listed %d rows, want the two repos' boards", len(rows))
	}
	for _, row := range rows {
		repo := filepath.Join(home, row.Name)
		if key := tab.BoardKey(context.Background(), boardRoot(repo)); key != row.Key {
			t.Errorf("BoardKey(%s) = %s, want the boards list's key %s", row.Name, key, row.Key)
		}
	}
	if rows[0].Key == rows[1].Key {
		t.Errorf("the two repos share the key %s, want one key each", rows[0].Key)
	}
}
