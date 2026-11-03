package channel

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// openResults records each store's open result the hub reports.
type openResults struct {
	mu     sync.Mutex
	ok     []string
	failed map[string]error
}

func (r *openResults) OK(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ok = append(r.ok, name)
}

func (r *openResults) Fail(name string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failed == nil {
		r.failed = map[string]error{}
	}
	r.failed[name] = err
}

func (r *openResults) failure(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failed[name]
}

const notADatabase = "this file is not a SQLite database, and it is long enough to be read as a header\n"

// TestAChannelFileThatIsNoDatabaseFailsToOpenAndIsReported: the hub answers the open error,
// reports the channel's store as failed, and keeps no half-opened channel to answer later.
func TestAChannelFileThatIsNoDatabaseFailsToOpenAndIsReported(t *testing.T) {
	dir := t.TempDir()
	h := NewHub(dir, false)
	t.Cleanup(func() { _ = h.Close(bg) })
	health := &openResults{}
	h.Health = health
	if err := os.WriteFile(h.File("broken"), []byte(notADatabase), 0o600); err != nil {
		t.Fatal(err)
	}

	ch, err := h.GetExisting(bg, "broken")
	if err == nil || ch != nil {
		t.Fatalf("GetExisting = (%v, %v), want the open error and no channel", ch, err)
	}
	if reported := health.failure("channel:broken"); reported == nil {
		t.Error("the failed open was not reported under the channel's name")
	}
	if again, err := h.GetExisting(bg, "broken"); err == nil || again != nil {
		t.Errorf("a second GetExisting = (%v, %v), want the same failure: nothing was cached", again, err)
	}
}

// TestAChannelOpensWithoutTheServerWideKnowledgeWhenItsFileIsBroken: the knowledge store's failure
// is reported under its own name, the channel works, and the next open tries the knowledge again.
func TestAChannelOpensWithoutTheServerWideKnowledgeWhenItsFileIsBroken(t *testing.T) {
	dir := t.TempDir()
	h := NewHub(dir, true)
	t.Cleanup(func() { _ = h.Close(bg) })
	health := &openResults{}
	h.Health = health
	knowledge := home.KnowledgeFile(dir)
	if err := os.WriteFile(knowledge, []byte(notADatabase), 0o600); err != nil {
		t.Fatal(err)
	}

	shop, err := h.Get(bg, "shop")
	if err != nil {
		t.Fatalf("Get shop: %v", err)
	}
	if _, err := shop.Join(bg, "a", JoinOpts{}); err != nil {
		t.Fatalf("a channel without the shared knowledge must still work: %v", err)
	}
	if health.failure("knowledge.db") == nil {
		t.Error("the knowledge store's failed open was not reported")
	}

	if err := os.Remove(knowledge); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Get(bg, "beta"); err != nil {
		t.Fatalf("Get beta: %v", err)
	}
	if _, err := os.Stat(knowledge); err != nil {
		t.Errorf("the next open did not try the knowledge store again: %v", err)
	}
}

// TestOpenSharedRefusesAFileThatIsNoDatabase: the server-wide knowledge store does not open over a
// file that is not a database, and leaves the file as it was.
func TestOpenSharedRefusesAFileThatIsNoDatabase(t *testing.T) {
	file := filepath.Join(t.TempDir(), "knowledge.db")
	if err := os.WriteFile(file, []byte(notADatabase), 0o600); err != nil {
		t.Fatal(err)
	}
	if shared, err := OpenShared(bg, file); err == nil {
		_ = shared.Store.Close()
		t.Fatal("OpenShared opened a file that is not a database")
	}
	if kept, _ := os.ReadFile(file); string(kept) != notADatabase {
		t.Errorf("the refused file now holds %q", kept)
	}
}
