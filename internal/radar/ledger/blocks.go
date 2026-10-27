package ledger

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The router's body store. The router alone writes it, radar only reads it: full request
// and response bodies live here as content-addressed blocks, so a conversation resent
// whole on every request is stored once. Each body splits into spans (splitBody); a span's
// SHA-256 names its row in ledger_blocks, which points at the span's compressed bytes in a
// day segment file beside the database. ledger_body maps each request's input and output
// sides to their blocks' hashes in order. The ledger's own database and schema are not
// touched.

// blocksDDL creates the body store's tables in router.db. The ledger's schema (version 2)
// stays in schema.go and never applies here.
const blocksDDL = `CREATE TABLE IF NOT EXISTS ledger_blocks (
hash TEXT PRIMARY KEY,
seg TEXT NOT NULL,
off INTEGER NOT NULL,
len INTEGER NOT NULL,
raw INTEGER NOT NULL,
first_ts INTEGER NOT NULL,
last_ts INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS ledger_body (
request_id TEXT NOT NULL,
side TEXT NOT NULL CHECK (side IN ('input','output')),
blocks BLOB NOT NULL,
bytes INTEGER NOT NULL,
redactions INTEGER NOT NULL,
PRIMARY KEY (request_id, side)
);
PRAGMA user_version = 1`

// BodyStore is the router's body store: one router.db plus the day segment files in its
// directory. The mutex serialises segment appends; the database itself stays on the
// package's single-connection open.
type BodyStore struct {
	db  DB
	dir string
	mu  sync.Mutex
}

// OpenBodies opens (creating when missing) the body store at file, and its segment
// directory beside it.
func OpenBodies(file string) (*BodyStore, error) {
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create the directory of %s: %w", file, err)
	}
	db, err := openDB(file)
	if err != nil {
		return nil, err
	}
	if err := db.Exec(blocksDDL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create the body store schema: %w", err)
	}
	if err := os.Chmod(file, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("chmod %s: %w", file, err)
	}
	return &BodyStore{db: db, dir: dir}, nil
}

// Close closes the body store's database. The segment files are already closed.
func (s *BodyStore) Close() error {
	return s.db.Close()
}

// PutBody stores one request's body for the side named. The body is redacted first, so
// no secret shape reaches a block, a segment or the body row; bytes and the block hashes
// are the redacted body's. Spans already stored are not rewritten; their last_ts moves to
// at when at is later. Each new span compresses into the day segment of at (UTC) and gets
// its ledger_blocks row. The body row for the request and side is replaced.
func (s *BodyStore) PutBody(ctx context.Context, requestID, side string, body []byte, at time.Time) error {
	if side != "input" && side != "output" {
		return fmt.Errorf("body side %q is neither input nor output", side)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	redacted, count := redactBody(body)
	spans := splitBody(redacted)
	hashes := make([][]byte, 0, len(spans))
	for _, span := range spans {
		sum := sha256.Sum256(span)
		if err := s.putBlock(sum, span, at); err != nil {
			return err
		}
		hashes = append(hashes, sum[:])
	}
	blocks := bytes.Join(hashes, nil)
	if err := s.db.Run(
		"INSERT OR REPLACE INTO ledger_body (request_id, side, blocks, bytes, redactions) VALUES (?, ?, ?, ?, ?)",
		requestID, side, blocks, len(redacted), count,
	); err != nil {
		return fmt.Errorf("store the body row of %s: %w", requestID, err)
	}
	return nil
}

// putBlock stores one span under its hash unless it is already stored, moving last_ts to at
// when at is later.
func (s *BodyStore) putBlock(sum [32]byte, span []byte, at time.Time) error {
	hash := hex.EncodeToString(sum[:])
	row, err := s.db.Get("SELECT last_ts FROM ledger_blocks WHERE hash = ?", hash)
	if err != nil {
		return fmt.Errorf("look up block %s: %w", hash, err)
	}
	if row != nil {
		if err := s.db.Run(
			"UPDATE ledger_blocks SET last_ts = ? WHERE hash = ? AND last_ts < ?",
			at.UnixMilli(), hash, at.UnixMilli(),
		); err != nil {
			return fmt.Errorf("move the last seen of block %s: %w", hash, err)
		}
		return nil
	}

	var packed bytes.Buffer
	writer, err := flate.NewWriter(&packed, flate.DefaultCompression)
	if err != nil {
		return fmt.Errorf("start the compressor for block %s: %w", hash, err)
	}
	if _, err := writer.Write(span); err != nil {
		return fmt.Errorf("compress block %s: %w", hash, err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish compressing block %s: %w", hash, err)
	}
	seg, off, err := s.appendSegment(segName(at), packed.Bytes())
	if err != nil {
		return err
	}
	if err := s.db.Run(
		"INSERT INTO ledger_blocks (hash, seg, off, len, raw, first_ts, last_ts) VALUES (?, ?, ?, ?, ?, ?, ?)",
		hash, seg, off, packed.Len(), len(span), at.UnixMilli(), at.UnixMilli(),
	); err != nil {
		return fmt.Errorf("record block %s: %w", hash, err)
	}
	return nil
}

// segName answers the day segment file's name for at, in UTC.
func segName(at time.Time) string {
	return "blocks-" + at.UTC().Format("2006-01-02") + ".seg"
}

// appendSegment appends the packed bytes to the named segment file, answering the file's
// base name and the offset the bytes now sit at.
func (s *BodyStore) appendSegment(name string, packed []byte) (string, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, name)
	offset := int64(0)
	if info, err := os.Stat(path); err == nil {
		offset = info.Size()
	} else if !os.IsNotExist(err) {
		return "", 0, fmt.Errorf("stat %s: %w", path, err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", 0, fmt.Errorf("open %s: %w", path, err)
	}
	written, err := file.Write(packed)
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil && written != len(packed) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return "", 0, fmt.Errorf("append to %s: %w", path, err)
	}
	return name, offset, nil
}

// Body reads a stored body back byte for byte, answering false when the request and side
// have no body row.
func (s *BodyStore) Body(ctx context.Context, requestID, side string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	row, err := s.db.Get(
		"SELECT blocks, bytes FROM ledger_body WHERE request_id = ? AND side = ?", requestID, side,
	)
	if err != nil {
		return nil, false, fmt.Errorf("read the body row of %s: %w", requestID, err)
	}
	if row == nil {
		return nil, false, nil
	}
	blocks, _ := row["blocks"].([]byte)
	want, _ := row["bytes"].(int64)
	if len(blocks)%32 != 0 {
		return nil, false, fmt.Errorf("the body row of %s holds %d bytes of hashes", requestID, len(blocks))
	}
	var body bytes.Buffer
	for at := 0; at < len(blocks); at += 32 {
		block, err := s.readBlock(hex.EncodeToString(blocks[at : at+32]))
		if err != nil {
			return nil, false, err
		}
		body.Write(block)
	}
	if int64(body.Len()) != want {
		return nil, false, fmt.Errorf(
			"the body of %s reads back %d bytes, want the %d recorded", requestID, body.Len(), want)
	}
	return body.Bytes(), true, nil
}

// Redactions answers how many secret shapes were replaced when the request and side's
// body was stored, false when they have no body row.
func (s *BodyStore) Redactions(ctx context.Context, requestID, side string) (int, bool, error) {
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	row, err := s.db.Get(
		"SELECT redactions FROM ledger_body WHERE request_id = ? AND side = ?", requestID, side,
	)
	if err != nil {
		return 0, false, fmt.Errorf("read the redactions of %s: %w", requestID, err)
	}
	if row == nil {
		return 0, false, nil
	}
	count, _ := row["redactions"].(int64)
	return int(count), true, nil
}

// readBlock answers one block's raw bytes, read from its segment at its offset and
// inflated back to the length recorded with it.
func (s *BodyStore) readBlock(hash string) ([]byte, error) {
	row, err := s.db.Get("SELECT seg, off, len, raw FROM ledger_blocks WHERE hash = ?", hash)
	if err != nil {
		return nil, fmt.Errorf("look up block %s: %w", hash, err)
	}
	if row == nil {
		return nil, fmt.Errorf("block %s is missing from the store", hash)
	}
	seg, _ := row["seg"].(string)
	off, _ := row["off"].(int64)
	length, _ := row["len"].(int64)
	raw, _ := row["raw"].(int64)

	file, err := os.Open(filepath.Join(s.dir, seg))
	if err != nil {
		return nil, fmt.Errorf("open the segment of block %s: %w", hash, err)
	}
	defer file.Close()
	packed := make([]byte, length)
	if _, err := file.ReadAt(packed, off); err != nil {
		return nil, fmt.Errorf("read block %s in %s: %w", hash, seg, err)
	}
	reader := flate.NewReader(bytes.NewReader(packed))
	inflated, err := io.ReadAll(reader)
	closeErr := reader.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, fmt.Errorf("inflate block %s: %w", hash, err)
	}
	if int64(len(inflated)) != raw {
		return nil, fmt.Errorf("block %s inflates to %d bytes, want the %d recorded",
			hash, len(inflated), raw)
	}
	return inflated, nil
}
