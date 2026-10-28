package ledger

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The router body store's cases, run against a router.db in a temp directory only. The
// keys are fakes built here, never a real key.

// testBodyKey is the key every case opens its store with; otherBodyKey is a different
// key for the wrong-key cases.
var (
	testBodyKey  = []byte("0123456789abcdef0123456789abcdef")
	otherBodyKey = []byte("fedcba9876543210fedcba9876543210")
)

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
	st, err := OpenBodies(filepath.Join(dir, "router.db"), testBodyKey)
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

	reopened, err := OpenBodies(filepath.Join(dir, "router.db"), testBodyKey)
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

// TestBodyStoreRefusesAShortKey: the body key is 32 bytes; a shorter one opens nothing
// and creates no file.
func TestBodyStoreRefusesAShortKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st, err := OpenBodies(filepath.Join(dir, "router.db"), make([]byte, 16))
	if err == nil {
		t.Fatal("open with a 16-byte key = nil error, want an error")
	}
	if st != nil {
		t.Fatal("open with a 16-byte key answers a store, want none")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the temp directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("a refused open created %d entries, want none", len(entries))
	}
}

// TestBodyStoreSealsBlocks: a body is stored sealed — the segment files hold neither the
// body's bytes nor their compressed form, the same key reads the body back byte for byte,
// another key answers an error and never the body, and a flipped bit in a segment answers
// an error naming the block and never bytes.
func TestBodyStoreSealsBlocks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	// A body that is not JSON stays one span, so the segment would hold exactly these 64
	// bytes, or exactly their flate stream, if the block were not sealed.
	body := []byte(strings.Repeat("M", 64))

	path := filepath.Join(dir, "router.db")
	st, err := OpenBodies(path, testBodyKey)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := st.PutBody(ctx, "req_sealed", "input", body, at); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	t.Run("the same key reads the body back byte for byte", func(t *testing.T) {
		reopened, err := OpenBodies(path, testBodyKey)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		got, ok, err := reopened.Body(ctx, "req_sealed", "input")
		if err != nil || !ok {
			t.Fatalf("body = %q, %t, %v; want the body found", got, ok, err)
		}
		if string(got) != string(body) {
			t.Fatalf("body = %q, want the exact bytes back", got)
		}
	})

	t.Run("the segment holds neither the body nor its compressed bytes", func(t *testing.T) {
		var packed bytes.Buffer
		writer, err := flate.NewWriter(&packed, flate.DefaultCompression)
		if err != nil {
			t.Fatalf("start the compressor: %v", err)
		}
		if _, err := writer.Write(body); err != nil {
			t.Fatalf("compress the body: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("finish compressing: %v", err)
		}
		for _, raw := range segmentFiles(t, dir) {
			if bytes.Contains(raw, body) {
				t.Fatal("a segment holds the body verbatim")
			}
			if bytes.Contains(raw, packed.Bytes()) {
				t.Fatal("a segment holds the body's compressed bytes")
			}
		}
	})

	t.Run("another key answers an error and never the body", func(t *testing.T) {
		reopened, err := OpenBodies(path, otherBodyKey)
		if err != nil {
			t.Fatalf("reopen under another key: %v", err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		got, _, err := reopened.Body(ctx, "req_sealed", "input")
		if err == nil {
			t.Fatal("body under another key = nil error, want an error")
		}
		if got != nil {
			t.Fatalf("body under another key = %d bytes, want none", len(got))
		}
		if strings.Contains(err.Error(), string(body)) {
			t.Fatal("the error carries body bytes")
		}
	})

	t.Run("a flipped bit answers an error and never bytes", func(t *testing.T) {
		reopened, err := OpenBodies(path, testBodyKey)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		flipOneBit(t, dir)
		got, _, err := reopened.Body(ctx, "req_sealed", "input")
		if err == nil {
			t.Fatal("body of a flipped block = nil error, want an error")
		}
		if got != nil {
			t.Fatalf("body of a flipped block = %d bytes, want none", len(got))
		}
		if strings.Contains(err.Error(), string(body)) {
			t.Fatal("the error carries body bytes")
		}
	})
}

// TestBodyStoreNamesBlocksWithTheKey: a block's name is the span's HMAC-SHA-256 under the
// store's key — not its bare SHA-256, and not its HMAC under another key — and the same
// span stores once under that name however many bodies carry it.
func TestBodyStoreNamesBlocksWithTheKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openBodies(t)
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

	body := []byte(`{"model":"m","messages":[{"role":"user","content":"a known message"}]}`)
	if err := st.PutBody(ctx, "req_named", "input", body, at); err != nil {
		t.Fatalf("put: %v", err)
	}
	spans := splitBody(body)
	first := spans[0]
	want := hmacName(testBodyKey, first)

	if want == sha256Name(first) {
		t.Fatal("a span's name equals its bare SHA-256")
	}
	if want == hmacName(otherBodyKey, first) {
		t.Fatal("a span's name equals its HMAC under another key")
	}
	if got := bodyHash(t, st, "req_named", "input"); got != want {
		t.Fatalf("the body row's first block = %s, want the span's HMAC %s", got, want)
	}
	row, err := st.db.Get("SELECT hash FROM ledger_blocks WHERE hash = ?", want)
	if err != nil {
		t.Fatalf("look up the named block: %v", err)
	}
	if row == nil {
		t.Fatalf("ledger_blocks holds no row named %s", want)
	}

	if err := st.PutBody(ctx, "req_again", "input", body, at); err != nil {
		t.Fatalf("put the same body again: %v", err)
	}
	if rows := countBlockRows(t, st); rows != int64(len(spans)) {
		t.Fatalf("ledger_blocks holds %d rows after the same body again, want the %d spans once",
			rows, len(spans))
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

	st, err := OpenBodies(filepath.Join(dir, "router.db"), testBodyKey)
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
	// 3 is the ledger schema's version; the body store must not have moved it.
	if version := row["user_version"]; version != int64(3) {
		t.Fatalf("ledger.db's user_version = %v, want 3", version)
	}
}

// TestBodyStoreRedactsSecrets: a body carrying one fake value of every secret shape is
// stored redacted — no segment file and no block holds a value, the body reads back with
// its labels, and the row records how many values were replaced.
func TestBodyStoreRedactsSecrets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openBodies(t)
	dir := st.dir
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

	fakes := []struct{ name, fake, want string }{
		{"bearer", "Bearer " + strings.Repeat("t", 20), "Bearer [redacted:bearer]"},
		{"api-key", "sk-ant-" + strings.Repeat("x", 20), "[redacted:api-key]"},
		{"api-key", strings.Repeat("ab", 16) + "." + strings.Repeat("z", 16), "[redacted:api-key]"},
		{"huddle-credential", "hcred_" + strings.Repeat("x", 20), "[redacted:huddle-credential]"},
		{"join-token", "k3m2x9." + strings.Repeat("a", 16), "[redacted:join-token]"},
		{"sign-in-code", "?code=" + strings.Repeat("q", 24), "?code=[redacted:sign-in-code]"},
	}
	var body, want strings.Builder
	body.WriteString(`{"model":"m","messages":[`)
	want.WriteString(`{"model":"m","messages":[`)
	for i, fake := range fakes {
		if i > 0 {
			body.WriteString(",")
			want.WriteString(",")
		}
		body.WriteString(`{"role":"user","content":"seed ` + fake.fake + ` end"}`)
		want.WriteString(`{"role":"user","content":"seed ` + fake.want + ` end"}`)
	}
	body.WriteString(`],"stream":true}`)
	want.WriteString(`],"stream":true}`)

	if err := st.PutBody(ctx, "req_secrets", "input", []byte(body.String()), at); err != nil {
		t.Fatalf("put: %v", err)
	}

	t.Run("no segment file holds a value verbatim", func(t *testing.T) {
		for _, raw := range segmentFiles(t, dir) {
			for _, fake := range fakes {
				if bytes.Contains(raw, []byte(fake.fake)) {
					t.Fatalf("a segment file holds the %s value verbatim", fake.name)
				}
			}
		}
	})
	t.Run("no block inflates to a value", func(t *testing.T) {
		for _, hash := range blockHashes(t, st) {
			block, err := st.readBlock(hash)
			if err != nil {
				t.Fatalf("read block %s: %v", hash, err)
			}
			for _, fake := range fakes {
				if bytes.Contains(block, []byte(fake.fake)) {
					t.Fatalf("block %s holds the %s value verbatim", hash, fake.name)
				}
			}
		}
	})
	t.Run("the body reads back with its labels", func(t *testing.T) {
		got, ok, err := st.Body(ctx, "req_secrets", "input")
		if err != nil {
			t.Fatalf("body: %v", err)
		}
		if !ok {
			t.Fatal("body = not found, want the redacted bytes")
		}
		if string(got) != want.String() {
			t.Fatalf("body = %s, want %s", got, want.String())
		}
	})
	t.Run("the row records how many values were replaced", func(t *testing.T) {
		count, ok, err := st.Redactions(ctx, "req_secrets", "input")
		if err != nil {
			t.Fatalf("redactions: %v", err)
		}
		if !ok {
			t.Fatal("redactions = not found, want the stored count")
		}
		if count != len(fakes) {
			t.Fatalf("redactions = %d, want %d", count, len(fakes))
		}
	})
}

// TestBodyStoreKeepsACleanBodyExact: a body with no secret shape in it reads back byte
// for byte, with no replacements recorded.
func TestBodyStoreKeepsACleanBodyExact(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openBodies(t)
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"nothing secret here"}],"stream":true}`)
	if err := st.PutBody(ctx, "req_clean", "input", body, at); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok, err := st.Body(ctx, "req_clean", "input")
	if err != nil || !ok {
		t.Fatalf("body = %q, %t, %v; want the body found", got, ok, err)
	}
	if string(got) != string(body) {
		t.Fatalf("body = %q, want the exact bytes back", got)
	}
	count, ok, err := st.Redactions(ctx, "req_clean", "input")
	if err != nil || !ok {
		t.Fatalf("redactions = %d, %t, %v; want the row found", count, ok, err)
	}
	if count != 0 {
		t.Fatalf("redactions = %d, want 0", count)
	}
}

// segmentFiles answers the raw bytes of every segment file in the store's directory.
func segmentFiles(t *testing.T, dir string) [][]byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the store directory: %v", err)
	}
	var raw [][]byte
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".seg") {
			continue
		}
		segment, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		raw = append(raw, segment)
	}
	if len(raw) == 0 {
		t.Fatal("no segment file exists")
	}
	return raw
}

// blockHashes answers every block hash the store's ledger_blocks holds.
func blockHashes(t *testing.T, st *BodyStore) []string {
	t.Helper()
	row, err := st.db.Get("SELECT GROUP_CONCAT(hash) AS hashes FROM ledger_blocks")
	if err != nil {
		t.Fatalf("list the blocks: %v", err)
	}
	if row == nil {
		t.Fatal("no blocks were stored")
	}
	joined, _ := row["hashes"].(string)
	if joined == "" {
		return nil
	}
	return strings.Split(joined, ",")
}

// openBodies opens a router.db in a temp directory's ledger directory — the layout the
// radar state dir uses — closed when the test ends.
func openBodies(t *testing.T) *BodyStore {
	t.Helper()
	st, err := OpenBodies(filepath.Join(t.TempDir(), "ledger", "router.db"), testBodyKey)
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

// hmacName answers the name the store gives a span under key: the hex HMAC-SHA-256 of it.
func hmacName(key, span []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(span)
	return hex.EncodeToString(mac.Sum(nil))
}

// sha256Name answers a span's bare SHA-256, the name it would carry under no key at all.
func sha256Name(span []byte) string {
	sum := sha256.Sum256(span)
	return hex.EncodeToString(sum[:])
}

// flipOneBit turns one bit of the store's first segment file, leaving the rest as they were.
func flipOneBit(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the store directory: %v", err)
	}
	var segment string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".seg") {
			segment = filepath.Join(dir, entry.Name())
			break
		}
	}
	if segment == "" {
		t.Fatal("no segment file exists")
	}
	raw, err := os.ReadFile(segment)
	if err != nil {
		t.Fatalf("read %s: %v", segment, err)
	}
	raw[len(raw)/2] ^= 0x01
	if err := os.WriteFile(segment, raw, 0o600); err != nil {
		t.Fatalf("write %s: %v", segment, err)
	}
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
