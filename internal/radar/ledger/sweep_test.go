package ledger

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The body sweep: bodies live BodyTTL, and one sweep removes the body rows past it, the
// blocks whose last use is past it, and the day segments no remaining block names. Run
// against a router.db in a temp directory only.

// TestBodyStoreSweepsPastTheTTL puts one body on each of day 1, day 5 and day 9, every
// body built as the shared span plus a span of its own, then sweeps twice: once at
// day 9 + 1 h, when the day-1 body is past the TTL but the shared span still keeps the
// day-1 segment alive, and once a week later, when the day-1 segment is free to go.
func TestBodyStoreSweepsPastTheTTL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := OpenBodies(filepath.Join(dir, "router.db"), testBodyKey)
	if err != nil {
		t.Fatalf("open the body store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	day1 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	day5 := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	day9 := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

	shared := "shared history every request carries"
	body := func(own string) []byte {
		return []byte(`{"messages":[` + jsonString(shared) + `,` + jsonString(own) + `]}`)
	}
	puts := []struct {
		id  string
		at  time.Time
		own string
	}{
		{"req_day1", day1, "stored on day 1"},
		{"req_day5", day5, "stored on day 5"},
		{"req_day9", day9, "stored on day 9"},
	}
	bodies := make(map[string][]byte, len(puts))
	for _, put := range puts {
		bodies[put.id] = body(put.own)
		if err := st.PutBody(ctx, put.id, "input", bodies[put.id], put.at); err != nil {
			t.Fatalf("put %s: %v", put.id, err)
		}
	}
	// A foreign file and a misnamed segment share the directory; neither is the sweep's
	// to touch.
	for _, name := range []string{"keep-me.txt", "blocks-not-a-date.seg"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("not the sweep's"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	// A segment of the second sweep's own date holds no block row; its day alone keeps it.
	todays := segName(time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC))
	if err := os.WriteFile(filepath.Join(dir, todays), nil, 0o600); err != nil {
		t.Fatalf("write %s: %v", todays, err)
	}

	// The cutoff lands at day 2 + 1 h: the day-1 body row and its own block are past the
	// TTL, the day-5 and day-9 bodies and the shared span are not. The shared span's
	// block lives in the day-1 segment, so the file stays this sweep.
	sweep, err := st.Sweep(ctx, day9.Add(time.Hour))
	if err != nil {
		t.Fatalf("sweep at day 9 + 1 h: %v", err)
	}
	want := BodySweep{Bodies: 1, Blocks: 1, Segments: 0}
	if sweep != want {
		t.Fatalf("the sweep at day 9 + 1 h answered %+v, want %+v", sweep, want)
	}
	if _, ok, err := st.Body(ctx, "req_day1", "input"); err != nil || ok {
		t.Fatalf("the day-1 body = found %t, %v; want it gone", ok, err)
	}
	if rows := countBlockRows(t, st); rows != 5 {
		t.Fatalf("ledger_blocks holds %d rows, want the 5 still in use", rows)
	}
	for _, id := range []string{"req_day5", "req_day9"} {
		got, ok, err := st.Body(ctx, id, "input")
		if err != nil || !ok {
			t.Fatalf("body of %s = %q, %t, %v; want the exact bytes back", id, got, ok, err)
		}
		if !bytes.Equal(got, bodies[id]) {
			t.Fatalf("body of %s reads back %q, want %q", id, got, bodies[id])
		}
	}
	for _, name := range []string{"blocks-2026-10-01.seg", todays, "keep-me.txt", "blocks-not-a-date.seg"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s should survive the first sweep: %v", name, err)
		}
	}

	// A week later every last use is past the cutoff: the remaining bodies and blocks go,
	// and with no row left naming the day-1 and day-5 segments both files go too. The
	// day-9 segment's day is the cutoff's own day, so it stays.
	sweep, err = st.Sweep(ctx, time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("sweep a week later: %v", err)
	}
	want = BodySweep{Bodies: 2, Blocks: 5, Segments: 2}
	if sweep != want {
		t.Fatalf("the sweep a week later answered %+v, want %+v", sweep, want)
	}
	if rows := countBlockRows(t, st); rows != 0 {
		t.Fatalf("ledger_blocks holds %d rows, want none", rows)
	}
	for _, name := range []string{"blocks-2026-10-01.seg", "blocks-2026-10-05.seg"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s = %v, want it deleted once nothing names it", name, err)
		}
	}
	for _, name := range []string{"blocks-2026-10-09.seg", todays, "keep-me.txt", "blocks-not-a-date.seg"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s should survive the second sweep: %v", name, err)
		}
	}
}
