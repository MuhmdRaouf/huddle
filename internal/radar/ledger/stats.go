package ledger

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// The body store's totals, the numbers the owner's switch page serves: how many requests
// the store keeps bodies for, and how much disk the store's own files hold.

// dbFile is the store's database file's base name; its WAL and shared-memory siblings
// carry -wal and -shm on it.
const dbFile = "router.db"

// BodyStats answers what the body store holds: Requests is the count of distinct request
// ids with a stored body; DiskBytes is the store's own footprint on disk.
type BodyStats struct {
	Requests  int
	DiskBytes int64
}

// Stats answers the store's totals. The count is one query; the disk is the size of the
// store's own files — the database and its WAL and shared-memory siblings when present,
// plus every day segment file in its directory. No other file in the directory counts:
// ledger.db lives there too, and it is not the body store's.
func (s *BodyStore) Stats(ctx context.Context) (BodyStats, error) {
	if err := ctx.Err(); err != nil {
		return BodyStats{}, err
	}
	requests, err := countRows(s.db, "SELECT COUNT(DISTINCT request_id) AS n FROM ledger_body")
	if err != nil {
		return BodyStats{}, fmt.Errorf("count the stored requests: %w", err)
	}
	disk, err := s.diskBytes()
	if err != nil {
		return BodyStats{}, err
	}
	return BodyStats{Requests: requests, DiskBytes: disk}, nil
}

// diskBytes answers the size of the store's own files: the database file and its WAL and
// shared-memory siblings when present, plus every day segment file in the store's
// directory, matched the way the sweep matches them.
func (s *BodyStore) diskBytes() (int64, error) {
	var total int64
	for _, name := range []string{dbFile, dbFile + "-wal", dbFile + "-shm"} {
		size, err := fileSize(filepath.Join(s.dir, name))
		if err != nil {
			return 0, err
		}
		total += size
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0, fmt.Errorf("list the segments of %s: %w", s.dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if _, ok := segmentDay(entry.Name()); !ok {
			continue
		}
		size, err := fileSize(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			return 0, err
		}
		total += size
	}
	return total, nil
}

// fileSize answers a file's size, or 0 when it is not there: a sibling exists only while
// the database last shut down uncleanly, and a segment can go between the listing and
// the stat.
func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("stat %s: %w", path, err)
	}
	return info.Size(), nil
}
