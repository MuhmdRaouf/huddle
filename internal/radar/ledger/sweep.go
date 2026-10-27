package ledger

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The sweeps: how long each file keeps what it stores, and the one call that cuts it
// back. An hour beat drives both.

// BodyTTL is how long a stored body stays in router.db.
const BodyTTL = 7 * 24 * time.Hour

// MetaTTL is how long a request row and its facts stay in ledger.db.
const MetaTTL = 90 * 24 * time.Hour

// BodySweep answers what one body sweep removed.
type BodySweep struct {
	Bodies   int
	Blocks   int
	Segments int
}

// Sweep cuts the body store back to the TTL: every ledger_body row stored before the
// cutoff — now minus BodyTTL — goes, then every ledger_blocks row whose last use is
// before it, then the day segment files no remaining block row names. last_ts already
// moves to the newest body that uses a block (putBlock), so a block stays exactly while
// a kept body can still name it, and no read of the body rows is needed here. A segment
// whose day is the cutoff's own day or later stays even with no row naming it: it may be
// receiving an append. Files with any other name are not the sweep's to touch.
func (s *BodyStore) Sweep(ctx context.Context, now time.Time) (BodySweep, error) {
	if err := ctx.Err(); err != nil {
		return BodySweep{}, err
	}
	cutoff := now.Add(-BodyTTL).UnixMilli()
	sweep := BodySweep{}

	bodies, err := countRows(s.db, "SELECT COUNT(*) AS n FROM ledger_body WHERE at < ?", cutoff)
	if err != nil {
		return BodySweep{}, fmt.Errorf("count the spent body rows: %w", err)
	}
	if err := s.db.Run("DELETE FROM ledger_body WHERE at < ?", cutoff); err != nil {
		return BodySweep{}, fmt.Errorf("sweep the body rows: %w", err)
	}
	sweep.Bodies = bodies

	// Safe without reading the body rows: PutBody moves a block's last_ts to the newest
	// body that uses it.
	blocks, err := countRows(s.db, "SELECT COUNT(*) AS n FROM ledger_blocks WHERE last_ts < ?", cutoff)
	if err != nil {
		return BodySweep{}, fmt.Errorf("count the spent blocks: %w", err)
	}
	if err := s.db.Run("DELETE FROM ledger_blocks WHERE last_ts < ?", cutoff); err != nil {
		return BodySweep{}, fmt.Errorf("sweep the block rows: %w", err)
	}
	sweep.Blocks = blocks

	sweep.Segments, err = s.removeSpentSegments(cutoff)
	if err != nil {
		return BodySweep{}, err
	}
	return sweep, nil
}

// removeSpentSegments deletes the day segment files no remaining block row names whose
// day is before the cutoff's UTC day, answering how many went. The segment mutex is held
// throughout, so no append lands in a file being removed.
func (s *BodyStore) removeSpentSegments(cutoffMs int64) (int, error) {
	cutDay := time.UnixMilli(cutoffMs).UTC().Format("2006-01-02")
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0, fmt.Errorf("list the segments of %s: %w", s.dir, err)
	}
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		day, ok := segmentDay(name)
		if !ok || day >= cutDay {
			continue
		}
		named, err := countRows(s.db, "SELECT COUNT(*) AS n FROM ledger_blocks WHERE seg = ?", name)
		if err != nil {
			return removed, fmt.Errorf("look up the blocks of %s: %w", name, err)
		}
		if named > 0 {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, name)); err != nil {
			return removed, fmt.Errorf("remove the spent segment %s: %w", name, err)
		}
		removed++
	}
	return removed, nil
}

// segmentDay answers the YYYY-MM-DD day in a segment file's name, false when the name is
// not one.
func segmentDay(name string) (string, bool) {
	if !strings.HasPrefix(name, "blocks-") || !strings.HasSuffix(name, ".seg") {
		return "", false
	}
	day := strings.TrimSuffix(strings.TrimPrefix(name, "blocks-"), ".seg")
	if _, err := time.Parse("2006-01-02", day); err != nil {
		return "", false
	}
	return day, true
}

// countRows answers the n column of a COUNT(*) AS n query.
func countRows(db DB, query string, args ...any) (int, error) {
	found, err := db.Get(query, args...)
	if err != nil {
		return 0, err
	}
	if found == nil {
		return 0, nil
	}
	n, _ := found["n"].(int64)
	return int(n), nil
}
