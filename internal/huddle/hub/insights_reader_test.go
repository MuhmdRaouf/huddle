package hub

// OpenReader opens the registry the way a separate process reads it: an existing file, no
// migration, no write.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenReaderReadsTheHistoryAndRefusesEveryWrite(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "hub.db")
	writer, err := Open(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	growPermission(t, writer, "s-1", "Bash", `{"command":"go vet ./..."}`, insightNow().Add(time.Hour))

	reader, err := OpenReader(ctx, file)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })

	records, err := reader.ItemHistory(ctx, insightNow())
	if err != nil || len(records) != 1 || records[0].Command != "go vet ./..." {
		t.Fatalf("ItemHistory = %+v (%v), want the writer's one ask", records, err)
	}
	if err := reader.st.Run(ctx, "DELETE FROM items"); err == nil {
		t.Error("the reader deleted rows; it must refuse every write")
	}
	if left, _ := writer.ItemHistory(ctx, insightNow()); len(left) != 1 {
		t.Errorf("the writer sees %d items after the reader's delete, want 1", len(left))
	}
}

func TestOpenReaderNeverCreatesAFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "hub.db")
	if reader, err := OpenReader(context.Background(), file); err == nil {
		_ = reader.Close()
		t.Fatal("OpenReader of a missing file succeeded, want an error")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("stat %s = %v, want the file left uncreated", file, err)
	}
}
