// The kanban.db schema, in migrations numbered from 1: every migration runs once, in its
// number's order, at Open, and the version it brought the file to sits in the meta table.
// An older kanban refuses a newer file rather than guess at its shape.

package store

// SchemaVersion is the version an opened database is brought to, and the newest this
// build reads.
const SchemaVersion = 1

// migrations holds migration n's statements at index n-1. A ticket's whole card text
// (front matter, body and log) stays one value: the board's text functions keep working
// unchanged, and kanban show prints it as is.
var migrations = []string{
	// 1: one row per ticket, keyed by id.
	`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
	 CREATE TABLE tickets (
	   id TEXT PRIMARY KEY,
	   col TEXT NOT NULL,
	   text TEXT NOT NULL,
	   updated_at TEXT NOT NULL
	 );
	 CREATE INDEX tickets_col ON tickets(col, id);`,
}
