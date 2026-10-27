package ledger

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The router body store's cases, run against a router.db in a temp directory only.

func TestBodyStoreRoundTripsABody(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("a chat body reads back byte for byte", func(t *testing.T) {
		st := openBodies(t)
		body := []byte(`{"model":"m","messages":[{"role":"user","content":"a"}],"stream":true}`)
		at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
		if err := st.PutBody(ctx, "req_1", "input", body, at); err != nil {
			t.Fatalf("put: %v", err)
		}
		got, ok, err := st.Body(ctx, "req_1", "input")
		if err != nil {
			t.Fatalf("body: %v", err)
		}
		if !ok {
			t.Fatal("body = not found, want the bytes just put")
		}
		if string(got) != string(body) {
			t.Fatalf("body = %q, want %q", got, body)
		}
	})

	t.Run("a body that is not JSON reads back byte for byte", func(t *testing.T) {
		st := openBodies(t)
		body := []byte("plain text with, punctuation; and symbols %42")
		at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
		if err := st.PutBody(ctx, "req_2", "input", body, at); err != nil {
			t.Fatalf("put: %v", err)
		}
		got, ok, err := st.Body(ctx, "req_2", "input")
		if err != nil {
			t.Fatalf("body: %v", err)
		}
		if !ok || string(got) != string(body) {
			t.Fatalf("body = %q, %t; want %q, true", got, ok, body)
		}
	})

	t.Run("an empty body records no blocks and reads back empty", func(t *testing.T) {
		st := openBodies(t)
		at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
		if err := st.PutBody(ctx, "req_3", "output", nil, at); err != nil {
			t.Fatalf("put: %v", err)
		}
		got, ok, err := st.Body(ctx, "req_3", "output")
		if err != nil {
			t.Fatalf("body: %v", err)
		}
		if !ok {
			t.Fatal("body = not found, want an empty body")
		}
		if len(got) != 0 {
			t.Fatalf("body = %q, want no bytes", got)
		}
		if rows := countBlockRows(t, st); rows != 0 {
			t.Fatalf("ledger_blocks holds %d rows, want none for an empty body", rows)
		}
	})

	t.Run("the two sides of one request stay apart", func(t *testing.T) {
		st := openBodies(t)
		at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
		if err := st.PutBody(ctx, "req_4", "input", []byte(`{"a":1}`), at); err != nil {
			t.Fatalf("put input: %v", err)
		}
		if err := st.PutBody(ctx, "req_4", "output", []byte(`{"b":2}`), at); err != nil {
			t.Fatalf("put output: %v", err)
		}
		input, ok, err := st.Body(ctx, "req_4", "input")
		if err != nil || !ok || string(input) != `{"a":1}` {
			t.Fatalf("input body = %q, %t, %v; want {\"a\":1}, true, nil", input, ok, err)
		}
		output, ok, err := st.Body(ctx, "req_4", "output")
		if err != nil || !ok || string(output) != `{"b":2}` {
			t.Fatalf("output body = %q, %t, %v; want {\"b\":2}, true, nil", output, ok, err)
		}
	})

	t.Run("a side other than input or output is refused", func(t *testing.T) {
		st := openBodies(t)
		at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
		if err := st.PutBody(ctx, "req_5", "headers", []byte(`{}`), at); err == nil {
			t.Fatal("put a headers side = nil error, want an error")
		}
		if _, _, err := st.Body(ctx, "req_5", "headers"); err != nil {
			t.Fatalf("body of a refused side = %v, want not found", err)
		}
	})

	t.Run("an unknown request answers not found", func(t *testing.T) {
		st := openBodies(t)
		got, ok, err := st.Body(ctx, "req_missing", "input")
		if err != nil {
			t.Fatalf("body: %v", err)
		}
		if ok {
			t.Fatalf("body = %q, true; want not found", got)
		}
	})
}

// TestBodyStoreStoresASharedBlockOnce: two requests that resend the same three messages and
// system prompt add only their new spans to the segment, and both read back exactly.
func TestBodyStoreStoresASharedBlockOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openBodies(t)
	dir := st.dir
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

	first := chatBody("you are helpful", "m1", "m2", "m3")
	second := chatBody("you are helpful", "m1", "m2", "m3", "m4")
	if err := st.PutBody(ctx, "req_first", "input", first, at); err != nil {
		t.Fatalf("put the first request: %v", err)
	}
	rowsBefore := countBlockRows(t, st)
	segmentBefore := segmentBytes(t, dir)

	if err := st.PutBody(ctx, "req_second", "input", second, at); err != nil {
		t.Fatalf("put the second request: %v", err)
	}
	rowsAfter := countBlockRows(t, st)
	segmentAfter := segmentBytes(t, dir)

	// The second request's new spans are the third message with a comma tail and the
	// fourth message with the body's tail; everything else is already stored.
	newSpans := len(splitBody(second)) - sharedSpanCount(first, second)
	if rowsAfter-rowsBefore != int64(newSpans) {
		t.Fatalf("ledger_blocks grew by %d rows, want the %d new spans only",
			rowsAfter-rowsBefore, newSpans)
	}
	rawNew := 0
	for _, span := range splitBody(second) {
		if sharedSpanCount(first, span) == 0 {
			rawNew += len(span)
		}
	}
	if growth := segmentAfter - segmentBefore; growth >= int64(rawNew)+256 {
		t.Fatalf("the segment grew by %d bytes, want less than the %d raw bytes of the new spans plus margin",
			growth, rawNew)
	}
	for _, id := range []string{"req_first", "req_second"} {
		want := first
		if id == "req_second" {
			want = second
		}
		got, ok, err := st.Body(ctx, id, "input")
		if err != nil || !ok || string(got) != string(want) {
			t.Fatalf("body of %s = %q, %t, %v; want the exact bytes back", id, got, ok, err)
		}
	}
}

// TestBodyStoreDaySegments: puts land in the segment file of their day, and a block
// reused the next day stays in the file of its first day.
func TestBodyStoreDaySegments(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openBodies(t)
	dir := st.dir

	monday := time.Date(2026, 10, 26, 23, 0, 0, 0, time.UTC)
	tuesday := time.Date(2026, 10, 27, 1, 0, 0, 0, time.UTC)
	shared := []byte(`{"model":"m","system":"shared history","stream":true}`)
	other := []byte(`{"model":"m","system":"a different body entirely","stream":true}`)

	if err := st.PutBody(ctx, "req_mon", "input", shared, monday); err != nil {
		t.Fatalf("put on the monday: %v", err)
	}
	if err := st.PutBody(ctx, "req_tue", "input", other, tuesday); err != nil {
		t.Fatalf("put on the tuesday: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "blocks-2026-10-26.seg")); err != nil {
		t.Fatalf("the monday's segment is missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "blocks-2026-10-27.seg")); err != nil {
		t.Fatalf("the tuesday's segment is missing: %v", err)
	}

	// The same body again the next day: its blocks stay in the monday's segment, and
	// only the block row's last_seen moves to the tuesday.
	if err := st.PutBody(ctx, "req_tue_again", "input", shared, tuesday); err != nil {
		t.Fatalf("put the shared body again: %v", err)
	}
	seg, lastTS := blockRow(t, st, bodyHash(t, st, "req_mon", "input"))
	if seg != "blocks-2026-10-26.seg" {
		t.Fatalf("the reused block lives in %s, want the monday's segment", seg)
	}
	if lastTS != tuesday.UnixMilli() {
		t.Fatalf("last_ts = %d, want the tuesday %d", lastTS, tuesday.UnixMilli())
	}
	if _, err := os.Stat(filepath.Join(dir, "blocks-2026-10-27.seg")); err != nil {
		t.Fatalf("the tuesday's segment vanished: %v", err)
	}
}

// TestBodyStoreFileModes: the database and every segment file are 0600 and their
// directory 0700, like every other radar state file.
func TestBodyStoreFileModes(t *testing.T) {
	t.Parallel()
	st := openBodies(t)
	ctx := context.Background()
	dir := st.dir

	if mode := fileMode(t, filepath.Join(dir, "router.db")); mode != 0o600 {
		t.Fatalf("router.db mode = %o, want 600", mode)
	}
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	if err := st.PutBody(ctx, "req_1", "input", []byte(`{"a":1}`), at); err != nil {
		t.Fatalf("put: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the store directory: %v", err)
	}
	var segmentSeen bool
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".seg") {
			continue
		}
		segmentSeen = true
		if mode := fileMode(t, filepath.Join(dir, entry.Name())); mode != 0o600 {
			t.Fatalf("segment %s mode = %o, want 600", entry.Name(), mode)
		}
	}
	if !segmentSeen {
		t.Fatal("no segment file was created")
	}
	if mode := fileMode(t, dir); mode != 0o700 {
		t.Fatalf("store directory mode = %o, want 700", mode)
	}
}

// TestBodyStoreSurvivesAReopen: a closed and reopened store keeps every body readable.
func TestBodyStoreSurvivesAReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

	bodies := map[string][]byte{
		"req_json": []byte(`{"model":"m","messages":[{"role":"user","content":"a"}]}`),
		"req_text": []byte("not json"),
		"req_none": nil,
	}
	st, err := OpenBodies(filepath.Join(dir, "router.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for id, body := range bodies {
		if err := st.PutBody(ctx, id, "input", body, at); err != nil {
			t.Fatalf("put %s: %v", id, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := OpenBodies(filepath.Join(dir, "router.db"))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	for id, want := range bodies {
		got, ok, err := reopened.Body(ctx, id, "input")
		if err != nil {
			t.Fatalf("body of %s: %v", id, err)
		}
		if !ok {
			t.Fatalf("body of %s = not found, want %q", id, want)
		}
		if string(got) != string(want) {
			t.Fatalf("body of %s = %q, want %q", id, got, want)
		}
	}
}

// TestBodyStoreLeavesTheLedgerAlone: a ledger.db opened in the same directory keeps its
// own schema; the body store creates nothing in it.
func TestBodyStoreLeavesTheLedgerAlone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()

	ledger, err := Open(filepath.Join(dir, "ledger.db"))
	if err != nil {
		t.Fatalf("open the ledger: %v", err)
	}
	t.Cleanup(func() { _ = ledger.Close() })

	st, err := OpenBodies(filepath.Join(dir, "router.db"))
	if err != nil {
		t.Fatalf("open the body store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	if err := st.PutBody(ctx, "req_1", "input", []byte(`{"a":1}`), at); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("close the ledger: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close the body store: %v", err)
	}

	db, err := openDB(filepath.Join(dir, "ledger.db"))
	if err != nil {
		t.Fatalf("reopen the ledger raw: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	row, err := db.Get("SELECT COUNT(*) AS n FROM sqlite_master WHERE type = 'table' " +
		"AND name IN ('ledger_blocks', 'ledger_body')")
	if err != nil {
		t.Fatalf("count the body store's tables in ledger.db: %v", err)
	}
	if n := row["n"]; n != int64(0) {
		t.Fatalf("ledger.db holds %v body-store tables, want none", n)
	}
	row, err = db.Get("PRAGMA user_version")
	if err != nil {
		t.Fatalf("read ledger.db's user_version: %v", err)
	}
	// 2 is the ledger schema's version; the body store must not have moved it.
	if version := row["user_version"]; version != int64(2) {
		t.Fatalf("ledger.db's user_version = %v, want 2", version)
	}
}

// openBodies opens a router.db in a temp directory's ledger directory — the layout the
// radar state dir uses — closed when the test ends.
func openBodies(t *testing.T) *BodyStore {
	t.Helper()
	st, err := OpenBodies(filepath.Join(t.TempDir(), "ledger", "router.db"))
	if err != nil {
		t.Fatalf("open the body store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// chatBody builds one chat-completions-style request body: the system prompt and one
// message per argument, each message 2 KB apart from its neighbours.
func chatBody(system string, messages ...string) []byte {
	var body strings.Builder
	body.WriteString(`{"model":"m","system":` + jsonString(system) + `,"messages":[`)
	for at, message := range messages {
		if at > 0 {
			body.WriteString(",")
		}
		text := message + strings.Repeat("x", len(message)*256)
		body.WriteString(`{"role":"user","content":` + jsonString(text) + `}`)
	}
	body.WriteString(`],"stream":true}`)
	return []byte(body.String())
}

// jsonString quotes one string the way encoding/json would, for bodies the tests build.
func jsonString(text string) string {
	quoted, err := json.Marshal(text)
	if err != nil {
		panic(err)
	}
	return string(quoted)
}

// countBlockRows answers how many block rows the store's ledger_blocks holds.
func countBlockRows(t *testing.T, st *BodyStore) int64 {
	t.Helper()
	row, err := st.db.Get("SELECT COUNT(*) AS n FROM ledger_blocks")
	if err != nil {
		t.Fatalf("count the block rows: %v", err)
	}
	return row["n"].(int64)
}

// segmentBytes answers the total size of the store's segment files.
func segmentBytes(t *testing.T, dir string) int64 {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the store directory: %v", err)
	}
	var total int64
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".seg") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			t.Fatalf("stat %s: %v", entry.Name(), err)
		}
		total += info.Size()
	}
	return total
}

// blockRow answers one block's segment and last_seen timestamp.
func blockRow(t *testing.T, st *BodyStore, hash string) (string, int64) {
	t.Helper()
	row, err := st.db.Get("SELECT seg, last_ts FROM ledger_blocks WHERE hash = ?", hash)
	if err != nil {
		t.Fatalf("read the block row: %v", err)
	}
	if row == nil {
		t.Fatalf("no block row for %s", hash)
	}
	seg, _ := row["seg"].(string)
	lastTS, _ := row["last_ts"].(int64)
	return seg, lastTS
}

// bodyHash answers the hex hash of a stored body's first block.
func bodyHash(t *testing.T, st *BodyStore, requestID, side string) string {
	t.Helper()
	row, err := st.db.Get("SELECT blocks FROM ledger_body WHERE request_id = ? AND side = ?", requestID, side)
	if err != nil || row == nil {
		t.Fatalf("read the body row of %s: %v", requestID, err)
	}
	blocks, _ := row["blocks"].([]byte)
	if len(blocks) < 32 {
		t.Fatalf("the body row of %s holds %d bytes of hashes, want at least one 32-byte hash", requestID, len(blocks))
	}
	return hex.EncodeToString(blocks[:32])
}

// fileMode answers a path's permission bits.
func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

// sharedSpanCount answers how many of a's spans are also spans of b.
func sharedSpanCount(a, b []byte) int {
	count := 0
	for _, span := range splitBody(a) {
		for _, other := range splitBody(b) {
			if bytes.Equal(span, other) {
				count++
				break
			}
		}
	}
	return count
}
