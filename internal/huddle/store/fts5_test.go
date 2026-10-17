package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestFTS5PorterUnicode61 proves the sqlite driver carries FTS5 with the porter unicode61
// tokenizer, the one task and knowledge search run on: the virtual table is created with it,
// a prefix query matches, and porter stems the index ("running" is indexed as "run", so the
// exact term "run" matches; a plain unicode61 index would hold "running" and match nothing).
func TestFTS5PorterUnicode61(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "channel.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	const create = `CREATE VIRTUAL TABLE tasks_fts USING fts5(id UNINDEXED, title, body, tokenize='porter unicode61')`
	if _, err := db.Exec(create); err != nil {
		t.Fatalf("create tasks_fts: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO tasks_fts (id, title, body) VALUES
		(1, 'Running the router', 'restart it'), (2, 'Cooking dinner', 'not router work')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	matching := func(t *testing.T, query string) []int {
		t.Helper()
		rows, err := db.Query(`SELECT id FROM tasks_fts WHERE tasks_fts MATCH ? ORDER BY rank`, query)
		if err != nil {
			t.Fatalf("MATCH %s: %v", query, err)
		}
		defer rows.Close()
		var ids []int
		for rows.Next() {
			var id int
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan: %v", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("rows: %v", err)
		}
		return ids
	}
	if ids := matching(t, `"run"*`); len(ids) != 1 || ids[0] != 1 {
		t.Errorf(`MATCH '"run"*' = %v, want [1]: the prefix query the search builds`, ids)
	}
	if ids := matching(t, `"run"`); len(ids) != 1 || ids[0] != 1 {
		t.Errorf(`MATCH '"run"' = %v, want [1]: porter indexes running as run`, ids)
	}
	if ids := matching(t, `"cook"*`); len(ids) != 1 || ids[0] != 2 {
		t.Errorf(`MATCH '"cook"*' = %v, want [2]: porter indexes cooking as cook`, ids)
	}
}
