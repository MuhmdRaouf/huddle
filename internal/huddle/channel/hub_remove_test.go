package channel

// Hub.Remove takes a channel off the hub: the open channel closes with its WAL checkpointed
// first, the file moves under channels/.removed/ named with the UTC stamp, and the cache drops
// the name, so the next Get makes a fresh channel or answers not found.
import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

var rmNow = time.Date(2026, 10, 21, 14, 15, 16, 0, time.UTC)

func TestHubRemove(t *testing.T) {
	t.Run("an open channel closes, its file moves under .removed, and a new Get is a fresh channel", func(t *testing.T) {
		dir := t.TempDir()
		h := NewHub(dir, true)
		t.Cleanup(func() { _ = h.Close(bg) })
		ch, err := h.Get(bg, "shop")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Join(bg, "a", JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Publish(bg, "a", PublishArgs{Topic: "n.x", Msg: "hello"}); err != nil {
			t.Fatal(err)
		}
		kept, err := h.Remove(bg, "shop", rmNow)
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(dir, "channels", ".removed", "shop-20261021T141516Z.db")
		if kept != want {
			t.Errorf("kept = %q, want %q", kept, want)
		}
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("the kept file: %v", err)
		}
		if _, err := os.Stat(h.File("shop")); !os.IsNotExist(err) {
			t.Errorf("channels/shop.db stayed: %v", err)
		}
		exists, err := h.Exists(bg, "shop")
		if err != nil || exists {
			t.Errorf("exists after remove = %v (%v), want false", exists, err)
		}
		names, err := h.Names(bg)
		if err != nil || len(names) != 0 {
			t.Errorf("names after remove = %v (%v), want none", names, err)
		}
		fresh, err := h.Get(bg, "shop")
		if err != nil {
			t.Fatal(err)
		}
		if fresh == ch {
			t.Error("the cache still answers the removed channel")
		}
		st, err := fresh.Stats(bg)
		if err != nil {
			t.Fatal(err)
		}
		if st.Events != 0 || st.Sessions != 0 {
			t.Errorf("the fresh channel = %+v, want no events and no sessions", st)
		}
	})

	t.Run("an unknown channel answers not found", func(t *testing.T) {
		h := NewHub(t.TempDir(), true)
		t.Cleanup(func() { _ = h.Close(bg) })
		if _, err := h.Remove(bg, "ghost", rmNow); err == nil || errStatus(err) != 404 {
			t.Errorf("remove(ghost) = %v, want 404", err)
		}
		if _, err := h.Remove(bg, "Ghost", rmNow); err == nil || errStatus(err) != 400 {
			t.Errorf("remove(Ghost) = %v, want 400", err)
		}
	})

	t.Run("a Get during the remove never reopens the old file", func(t *testing.T) {
		dir := t.TempDir()
		h := NewHub(dir, true)
		t.Cleanup(func() { _ = h.Close(bg) })
		ch, err := h.Get(bg, "shop")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Join(bg, "a", JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		stop := make(chan struct{})
		errs := make(chan error, 1)
		var wg sync.WaitGroup
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				c, err := h.GetExisting(bg, "shop")
				if err != nil {
					if errStatus(err) != 404 {
						errs <- err
						return
					}
					continue
				}
				if _, err := c.Stats(bg); err != nil {
					errs <- err
					return
				}
			}
		})
		if _, err := h.Remove(bg, "shop", rmNow); err != nil {
			t.Fatal(err)
		}
		close(stop)
		wg.Wait()
		select {
		case err := <-errs:
			t.Fatalf("a Get during the remove failed: %v", err)
		default:
		}
		if _, err := os.Stat(filepath.Join(dir, "channels", "shop.db")); !os.IsNotExist(err) {
			t.Errorf("channels/shop.db came back: %v", err)
		}
	})
}
