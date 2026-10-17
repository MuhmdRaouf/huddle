package channel

import (
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Waiters on an asynchronous database: a waiter registers before it queries, and re-queries after
// every wake-up, so a publish that lands at any point of a wait's life wakes it, and exactly once.
func TestWaiters(t *testing.T) {
	t.Run("every wait wakes when publishes race with the waits (hundreds of iterations)", func(t *testing.T) {
		ch := openChannel(t, "t")
		const n = 40
		const rounds = 12 // 480 waits, each racing a publish
		for i := range n {
			if _, err := ch.Join(bg, "w"+strconv.Itoa(i), JoinOpts{}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := ch.Join(bg, "pub", JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		woke := 0
		base := runtime.NumGoroutine()
		for r := range rounds {
			last, err := ch.LastSeq(bg)
			if err != nil {
				t.Fatal(err)
			}
			for i := range n {
				if _, err := ch.Ack(bg, "w"+strconv.Itoa(i), last); err != nil {
					t.Fatal(err)
				}
			}
			// each waiter starts at a different moment relative to its publish: before, during, after
			type hit struct {
				i  int
				h  WaitHit
				ok bool
			}
			hits := make(chan hit, n)
			var wg sync.WaitGroup
			for i := range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if i%3 == 2 {
						time.Sleep(time.Millisecond)
					} else if i%3 == 1 {
						runtime.Gosched()
					}
					h, err := ch.Wait(bg, "w"+strconv.Itoa(i), []string{"go.r" + strconv.Itoa(r)}, 10)
					hits <- hit{i, h, err == nil}
				}()
			}
			for i := range n {
				// half the publishes land a beat later
				if i%2 == 1 {
					time.Sleep(time.Millisecond)
				}
				if _, err := ch.Publish(bg, "pub", PublishArgs{Topic: "go.r" + strconv.Itoa(r), Msg: strconv.Itoa(i), To: sp("w" + strconv.Itoa(i))}); err != nil {
					t.Fatal(err)
				}
			}
			wg.Wait()
			close(hits)
			for o := range hits {
				if !o.ok || o.h.Kind != "event" || o.h.To == nil || *o.h.To != "w"+strconv.Itoa(o.i) {
					t.Fatalf("round %d waiter %d = %+v, want its event", r, o.i, o.h)
				}
				woke++
			}
		}
		if woke != n*rounds {
			t.Fatalf("woke = %d, want %d", woke, n*rounds)
		}
		// every waiter goroutine is done
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if runtime.NumGoroutine() <= base {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("goroutines did not settle: %d > %d", runtime.NumGoroutine(), base)
	})
}

func TestWaiterRaces(t *testing.T) {
	t.Run("waitReply, waitTask and gate wake when the answer races the wait", func(t *testing.T) {
		ch := openChannel(t, "t")
		if _, err := ch.Join(bg, "a", JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Join(bg, "b", JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		type hit struct {
			h   WaitHit
			g   GateResult
			err error
		}
		for i := range 100 {
			q, err := ch.Send(bg, "a", sp("b"), "q"+strconv.Itoa(i), SendOpts{Ask: true})
			if err != nil {
				t.Fatal(err)
			}
			res := make(chan hit, 1)
			go func() { h, err := ch.WaitReply(bg, "a", q.Seq, 10); res <- hit{h: h, err: err} }()
			if _, err := ch.Reply(bg, "b", q.Seq, "r"+strconv.Itoa(i)); err != nil {
				t.Fatal(err)
			}
			o := <-res
			if o.err != nil || o.h.Msg == nil || *o.h.Msg != "r"+strconv.Itoa(i) {
				t.Fatalf("round %d reply wait = %+v (%v)", i, o.h, o.err)
			}
			id := "t" + strconv.Itoa(i)
			if _, err := ch.CreateTask(bg, "b", CreateTaskInput{ID: id, Title: id}); err != nil {
				t.Fatal(err)
			}
			res = make(chan hit, 1)
			go func() { h, err := ch.WaitTask(bg, "a", id, 10); res <- hit{h: h, err: err} }()
			if _, err := ch.SetStatus(bg, "b", id, "done", ""); err != nil {
				t.Fatal(err)
			}
			o = <-res
			if o.err != nil || o.h.Status != "done" {
				t.Fatalf("round %d task wait = %+v (%v)", i, o.h, o.err)
			}
			if _, err := ch.Control(bg, "a", "b", "pause", ""); err != nil {
				t.Fatal(err)
			}
			res = make(chan hit, 1)
			go func() { g, err := ch.Gate(bg, "b", 10); res <- hit{g: g, err: err} }()
			if _, err := ch.Control(bg, "a", "b", "resume", ""); err != nil {
				t.Fatal(err)
			}
			o = <-res
			if o.err != nil || o.g.Control != "run" {
				t.Fatalf("round %d gate = %+v (%v)", i, o.g, o.err)
			}
		}
	})

	t.Run("a waiter resolves once, even when many publishes wake it at the same time", func(t *testing.T) {
		ch := openChannel(t, "t")
		if _, err := ch.Join(bg, "a", JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Join(bg, "b", JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		last, err := ch.LastSeq(bg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Ack(bg, "b", last); err != nil {
			t.Fatal(err)
		}
		settled := 0
		type hit struct {
			h   WaitHit
			err error
		}
		res := make(chan hit, 1)
		go func() {
			h, err := ch.Wait(bg, "b", []string{"burst.*"}, 10)
			settled++
			res <- hit{h, err}
		}()
		var wg sync.WaitGroup
		for i := range 200 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := ch.Publish(bg, "a", PublishArgs{Topic: "burst.x", Msg: strconv.Itoa(i)}); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		o := <-res
		if o.err != nil || o.h.Kind != "event" {
			t.Fatalf("burst wait = %+v (%v)", o.h, o.err)
		}
		time.Sleep(20 * time.Millisecond)
		if settled != 1 {
			t.Errorf("settled %d times, want once", settled)
		}
		ch.stateMu.Lock()
		waiters := len(ch.waiters)
		ch.stateMu.Unlock()
		if waiters != 0 {
			t.Errorf("%d waiters left registered", waiters)
		}
	})
}
