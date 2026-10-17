package store

import (
	"context"
	"path/filepath"
	"testing"
)

// TestACancelledTransactionNeverStrandsTheConnection pins the end of a transaction to its own
// context: a request cancelled while its transaction runs still rolls back, so the store's one
// connection is never left inside an open transaction that refuses every later BEGIN.
func TestACancelledTransactionNeverStrandsTheConnection(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Run(ctx, "CREATE TABLE t (v TEXT)"); err != nil {
		t.Fatal(err)
	}
	reqCtx, cancel := context.WithCancel(ctx)
	err = s.Tx(reqCtx, func(ctx context.Context, q Q) error {
		if err := q.Run(ctx, "INSERT INTO t (v) VALUES ('lost')"); err != nil {
			return err
		}
		cancel()
		return ctx.Err()
	})
	if err == nil {
		t.Fatal("a cancelled transaction answered nil")
	}
	if err := s.Tx(ctx, func(ctx context.Context, q Q) error {
		return q.Run(ctx, "INSERT INTO t (v) VALUES ('kept')")
	}); err != nil {
		t.Fatalf("the next transaction: %v", err)
	}
	rows, err := s.All(ctx, "SELECT v FROM t")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["v"] != "kept" {
		t.Fatalf("rows = %v, want only the second transaction's", rows)
	}
}
