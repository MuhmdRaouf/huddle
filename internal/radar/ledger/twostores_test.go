package ledger

// Two stores on one file: a handover serves router.db from two daemons at once — the old
// one draining while the new one serves — so every body either store puts while the other
// is live must read back byte for byte afterwards.

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestTwoStoresOnOneFileKeepEveryBody opens the same router.db twice with the same key, as
// the draining daemon and its taker do during a handover, and puts 50 bodies from each of
// two goroutines at the same day, each worker's bodies sharing blocks among themselves.
// Every body reads back byte for byte through either store afterwards.
func TestTwoStoresOnOneFileKeepEveryBody(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger", "router.db")
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

	first, err := OpenBodies(path, testBodyKey)
	if err != nil {
		t.Fatalf("open the first store: %v", err)
	}
	defer first.Close()
	second, err := OpenBodies(path, testBodyKey)
	if err != nil {
		t.Fatalf("open the second store: %v", err)
	}
	defer second.Close()

	// One system prompt and one closing message per worker: the worker's bodies share those
	// blocks, and the workers share nothing new — the model and stream members chatBody
	// always repeats are stored the day before, so a put fails only over the segment append,
	// never over a block row the two stores race to record. The warmup body lives in its own
	// day's segment, so the race's own day segment file is still not there when both stores
	// come to create it.
	const puts = 50
	if err := first.PutBody(ctx, "two-warmup", "input", chatBody("warming up the shared members", "one warm message"), at.Add(-24*time.Hour)); err != nil {
		t.Fatalf("warm the shared members up: %v", err)
	}
	want := make(map[string][]byte, 2*puts)
	for w := 0; w < 2; w++ {
		system := fmt.Sprintf("worker %d shares this system prompt across its own bodies", w)
		closing := fmt.Sprintf("worker %d closes every body with this shared block", w)
		for i := 0; i < puts; i++ {
			id := fmt.Sprintf("two-%d-%03d", w, i)
			want[id] = chatBody(system, fmt.Sprintf("worker %d asks its turn number %d", w, i), closing)
		}
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	putErrs := make([]error, 0)
	// A rendezvous per round: both stores arrive before either puts, the way two daemons
	// busy on the same handover moment are nose to tail. Without it the two puts drift
	// milliseconds apart and the append windows never meet.
	barriers := make([]sync.WaitGroup, puts)
	for i := range barriers {
		barriers[i].Add(2)
	}
	for w := 0; w < 2; w++ {
		worker := w
		store := first
		if worker == 1 {
			store = second
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < puts; i++ {
				barriers[i].Done()
				barriers[i].Wait()
				id := fmt.Sprintf("two-%d-%03d", worker, i)
				if err := store.PutBody(ctx, id, "input", want[id], at); err != nil {
					mu.Lock()
					putErrs = append(putErrs, fmt.Errorf("put %s: %w", id, err))
					mu.Unlock()
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	for _, err := range putErrs {
		t.Error(err)
	}
	if t.Failed() {
		t.Fatal("a put through one of the two stores failed")
	}

	// Every body reads back through either store, byte for byte.
	for name, st := range map[string]*BodyStore{"first": first, "second": second} {
		for id, body := range want {
			got, ok, err := st.Body(ctx, id, "input")
			if err != nil {
				t.Fatalf("read %s back through the %s store: %v", id, name, err)
			}
			if !ok {
				t.Fatalf("%s is missing from the %s store", id, name)
			}
			if !bytes.Equal(got, body) {
				t.Fatalf("%s reads back %d bytes through the %s store, want the %d put",
					id, len(got), name, len(body))
			}
		}
	}
}
