package hub

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// TestACodeOrRecoveryCodeSpendsOnce pins the race the spend guards against: two requests that
// both passed the code check reach the write, and only the first may mint a session.
func TestACodeOrRecoveryCodeSpendsOnce(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	now := int64(1_800_000_000_000)
	if err := db.TOTPEnrol(ctx, "JBSWY3DPEHPK3PXP", []string{"abcd-efgh"}, now, "owner"); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	var spent CodeSpentError

	if err := db.TOTPSignInCode(ctx, 100, "Mac", []byte("h1"), "s1", now+1000, now); err != nil {
		t.Fatalf("first code: %v", err)
	}
	if err := db.TOTPSignInCode(ctx, 100, "Mac", []byte("h2"), "s2", now+1000, now); !errors.As(err, &spent) {
		t.Fatalf("the same step again = %v, want CodeSpentError", err)
	}
	if err := db.TOTPSignInCode(ctx, 99, "Mac", []byte("h3"), "s3", now+1000, now); !errors.As(err, &spent) {
		t.Fatalf("an earlier step = %v, want CodeSpentError", err)
	}
	if _, live, _ := db.DeviceSession(ctx, "s2", now); live {
		t.Fatal("a refused spend left a session behind")
	}

	digest := TOTPDigest("abcd-efgh")
	if err := db.TOTPSignInRecovery(ctx, digest, "Mac", []byte("h4"), "s4", now+1000, now); err != nil {
		t.Fatalf("first recovery: %v", err)
	}
	if err := db.TOTPSignInRecovery(ctx, digest, "Mac", []byte("h5"), "s5", now+1000, now); !errors.As(err, &spent) {
		t.Fatalf("the same recovery code again = %v, want CodeSpentError", err)
	}
	if _, live, _ := db.DeviceSession(ctx, "s5", now); live {
		t.Fatal("a refused recovery spend left a session behind")
	}
}
