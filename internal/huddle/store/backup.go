// Copying and reading a database the caller must not edit (a legacy home's file): SQLite's own
// backup API for the copy, and a plain open for the read. Both open the source without any
// pragma, so its journal stays as it was, and a clean close folds nothing in: the file's bytes
// are the same after as before. A read-only URI open would leave a WAL file's -shm and -wal
// sidecars behind (a read-only connection cannot remove them), so the plain open is the one
// spelling that leaves nothing in the source's folder.

package store

import (
	"context"
	"fmt"
	"os"

	"modernc.org/sqlite"
)

// existsFile says whether path names a file that is there: an open would create a missing
// database, so the caller of a copy or a read checks first.
func existsFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// backuper is the backup face modernc's connection carries; it is unexported there, so the
// interface names it without naming the type.
type backuper interface {
	NewBackup(dst string) (*sqlite.Backup, error)
}

// BackupFile copies one SQLite database into a fresh file dst with SQLite's backup API: one
// consistent image of the source, a live file included, never raw bytes of it. The source is
// only read; dst is overwritten whole.
func BackupFile(ctx context.Context, src, dst string) error {
	if !existsFile(src) {
		return fmt.Errorf("sqlite: no database %s", src)
	}
	pool, err := SQLite(ctx, src)
	if err != nil {
		return err
	}
	defer pool.Close()
	return pool.Raw(func(driverConn any) error {
		source, ok := driverConn.(backuper)
		if !ok {
			return fmt.Errorf("sqlite: the connection carries no backup API")
		}
		backup, err := source.NewBackup(dst)
		if err != nil {
			return err
		}
		if _, err := backup.Step(-1); err != nil {
			_ = backup.Finish()
			return err
		}
		return backup.Finish()
	})
}

// Raw runs fn with the pool's one driver connection, for the faces only a connection carries.
func (d *DB) Raw(fn func(driverConn any) error) error {
	return d.conn.Raw(fn)
}

// ReadAll answers one query over a database opened for the ask alone and closed after: no
// pragma, no schema add, no journal change — a file the caller must not edit is only read.
func ReadAll(ctx context.Context, file, query string, params ...any) ([]Row, error) {
	if !existsFile(file) {
		return nil, fmt.Errorf("sqlite: no database %s", file)
	}
	pool, err := SQLite(ctx, file)
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	return pool.All(ctx, query, params...)
}
