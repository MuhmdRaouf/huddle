package channel

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

var bg = context.Background()

// openChannel opens one channel over a fresh SQLite file, closed with the test.
func openChannel(t *testing.T, name string) *Channel {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(bg, filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ch, err := Open(bg, name, st)
	if err != nil {
		t.Fatalf("open channel: %v", err)
	}
	t.Cleanup(func() { _ = ch.Close(bg) })
	return ch
}

// joinedChannel is one channel with a and b joined, the shape every channel case starts from.
func joinedChannel(t *testing.T) *Channel {
	t.Helper()
	ch := openChannel(t, "t")
	if _, err := ch.Join(bg, "a", JoinOpts{Role: sp("builds")}); err != nil {
		t.Fatalf("join a: %v", err)
	}
	if _, err := ch.Join(bg, "b", JoinOpts{Role: sp("tests")}); err != nil {
		t.Fatalf("join b: %v", err)
	}
	return ch
}

func ip(i int64) *int64 { return &i }

func vv(t *testing.T, json string) *jsfmt.Value {
	t.Helper()
	v, err := jsfmt.DecodeValue([]byte(json))
	if err != nil {
		t.Fatalf("decode %s: %v", json, err)
	}
	return v
}

func errStatus(err error) int {
	var he *HuddleError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

// opStatus runs an op and answers its HTTP status: 200 when it ran.
func opStatus(err error) int {
	if err == nil {
		return 200
	}
	return errStatus(err)
}

func topics(evs []Ev) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Topic
	}
	return out
}

// dataStr reads one string field of an event's data object.
func dataStr(v *jsfmt.Value, key string) string {
	if v == nil || v.Kind() != jsfmt.KindObject {
		return ""
	}
	for i, k := range v.Keys() {
		if k == key {
			f := v.Fields()[i]
			if f != nil && f.Kind() == jsfmt.KindString {
				return f.Text()
			}
		}
	}
	return ""
}

// dataBool reads one boolean field of an event's data object.
func dataBool(v *jsfmt.Value, key string) bool {
	if v == nil || v.Kind() != jsfmt.KindObject {
		return false
	}
	for i, k := range v.Keys() {
		if k == key {
			f := v.Fields()[i]
			return f != nil && f.EncodeJSON() == "true"
		}
	}
	return false
}

// dataNum reads one number field of an event's data object, as an integer.
func dataNum(v *jsfmt.Value, key string) int64 {
	if v == nil || v.Kind() != jsfmt.KindObject {
		return 0
	}
	for i, k := range v.Keys() {
		if k == key {
			f := v.Fields()[i]
			if f == nil || f.Kind() != jsfmt.KindNumber {
				return 0
			}
			n, err := strconv.ParseInt(f.EncodeJSON(), 10, 64)
			if err != nil {
				return 0
			}
			return n
		}
	}
	return 0
}

// runtimeNumGoroutine is one name for the leak checks.
func runtimeNumGoroutine() int { return runtime.NumGoroutine() }

// until polls cond until it holds or five seconds pass; the test fails on a timeout.
func until(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// waiting reports the session's presence state, for polls.
func waiting(t *testing.T, ch *Channel, name string) func() bool {
	return func() bool {
		p, err := ch.PresenceOf(bg, name)
		return err == nil && p != nil && p.State == "waiting"
	}
}

// isoNow renders t the way the channel stamps its rows: UTC, seconds, Z.
func isoNow(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// planValue reads a plan out of its JSON shape.
func planValue(t *testing.T, v *jsfmt.Value) Plan {
	t.Helper()
	p, err := PlanFromValue(v)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return p
}

// storeOpen opens one channel store, for the reopen case.
func storeOpen(ctx context.Context, file string) (*store.Store, error) {
	return store.Open(ctx, file)
}
