package board

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The session hook asks for the origin inside a part deadline of 2.5 s, so the
// read must end near its one-second bound even when git leaves a child behind
// that keeps the output pipe open after git itself is killed.

func TestOriginOfEndsAtItsBoundWhenGitLeavesAChildHoldingThePipe(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\nsleep 6\necho https://example.com/team/x.git\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	start := time.Now()
	got := OriginOf(context.Background(), t.TempDir())

	if got != "" {
		t.Errorf("OriginOf = %q, want empty: git never answered inside the bound", got)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("OriginOf took %s, want it back within 2s", elapsed)
	}
}
