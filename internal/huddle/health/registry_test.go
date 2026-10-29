package health

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// testClock is a clock a test moves by hand.
type testClock struct{ at time.Time }

func (c *testClock) Now() time.Time { return c.at }

// only answers the registry's one part, failing when it holds another number.
func only(t *testing.T, r *Registry) Part {
	t.Helper()
	parts := r.Snapshot()
	if len(parts) != 1 {
		t.Fatalf("Snapshot = %+v, want one part", parts)
	}
	return parts[0]
}

func TestRegistry(t *testing.T) {
	t0 := time.UnixMilli(1_700_000_000_000)

	t.Run("ok then fail is failing with errors 1", func(t *testing.T) {
		c := &testClock{at: t0}
		r := New(c.Now)
		r.OK("hub.db")
		c.at = c.at.Add(10 * time.Millisecond)
		r.Fail("hub.db", errors.New("locked"))
		p := only(t, r)
		if p.Name != "hub.db" || p.Kind != string(domain.HealthStore) {
			t.Errorf("part = %s/%s, want hub.db/store", p.Name, p.Kind)
		}
		if p.State != string(domain.HealthFailing) {
			t.Errorf("state = %q, want failing", p.State)
		}
		if p.Errors != 1 {
			t.Errorf("errors = %d, want 1", p.Errors)
		}
		if p.LastError != "locked" {
			t.Errorf("lastError = %q, want locked", p.LastError)
		}
		if p.LastOKAt != t0.UnixMilli() {
			t.Errorf("lastOkAt = %d, want %d", p.LastOKAt, t0.UnixMilli())
		}
		if p.LastErrAt != t0.Add(10*time.Millisecond).UnixMilli() {
			t.Errorf("lastErrAt = %d, want %d", p.LastErrAt, t0.Add(10*time.Millisecond).UnixMilli())
		}
		if r.Healthy() {
			t.Errorf("Healthy = true, want false while the part fails")
		}
	})

	t.Run("fail then ok is ok and errors stays 1", func(t *testing.T) {
		c := &testClock{at: t0}
		r := New(c.Now)
		r.Fail("hub.db", errors.New("locked"))
		c.at = c.at.Add(10 * time.Millisecond)
		r.OK("hub.db")
		p := only(t, r)
		if p.State != string(domain.HealthOK) {
			t.Errorf("state = %q, want ok", p.State)
		}
		if p.Errors != 1 {
			t.Errorf("errors = %d, want 1", p.Errors)
		}
		if p.LastError != "locked" || p.LastErrAt != t0.UnixMilli() {
			t.Errorf("last error = %q at %d, want the failure's record kept", p.LastError, p.LastErrAt)
		}
		if !r.Healthy() {
			t.Errorf("Healthy = false, want true after the recovery")
		}
	})

	t.Run("a 300 byte error is cut on a utf-8 boundary", func(t *testing.T) {
		r := New((&testClock{at: t0}).Now)
		r.Fail("ascii", errors.New(strings.Repeat("a", 300)))
		if got := only(t, r).LastError; got != strings.Repeat("a", domain.HealthErrorMax) {
			t.Errorf("LastError = %d bytes, want %d", len(got), domain.HealthErrorMax)
		}
		multi := New((&testClock{at: t0}).Now)
		multi.Fail("multibyte", errors.New(strings.Repeat("日", 100))) // 300 bytes
		got := only(t, multi).LastError
		if !utf8.ValidString(got) {
			t.Errorf("LastError = %q, which is not valid UTF-8", got)
		}
		if len(got) > domain.HealthErrorMax {
			t.Errorf("LastError = %d bytes, want at most %d", len(got), domain.HealthErrorMax)
		}
		if want := strings.Repeat("日", 66); got != want { // 66 runes are all that fits the cut
			t.Errorf("LastError = %d runes, want the %d leading runes", len([]rune(got)), len([]rune(want)))
		}
	})

	t.Run("snapshot is sorted by name", func(t *testing.T) {
		r := New((&testClock{at: t0}).Now)
		r.OK("c")
		r.OK("a")
		r.OK("b")
		parts := r.Snapshot()
		if len(parts) != 3 {
			t.Fatalf("Snapshot = %+v, want three parts", parts)
		}
		for i, want := range []string{"a", "b", "c"} {
			if parts[i].Name != want {
				t.Errorf("part %d = %s, want %s", i, parts[i].Name, want)
			}
		}
	})

	t.Run("a nil registry records nothing and answers empty and healthy", func(t *testing.T) {
		var r *Registry
		r.Register("sweep", domain.HealthLoop, time.Second)
		r.OK("sweep")
		r.Fail("sweep", errors.New("boom"))
		parts := r.Snapshot()
		if parts == nil || len(parts) != 0 {
			t.Errorf("Snapshot = %+v, want an empty non-nil slice", parts)
		}
		if !r.Healthy() {
			t.Errorf("Healthy = false, want true")
		}
	})

	t.Run("a nil error records nothing", func(t *testing.T) {
		c := &testClock{at: t0}
		r := New(c.Now)
		r.OK("hub.db")
		r.Fail("hub.db", nil)
		p := only(t, r)
		if p.State != string(domain.HealthOK) || p.Errors != 0 || p.LastErrAt != 0 || p.LastError != "" {
			t.Errorf("part = %+v, want the success with no error recorded", p)
		}
		r.Fail("never", nil)
		if got := r.Snapshot(); len(got) != 1 {
			t.Errorf("Snapshot = %+v, want the one part still", got)
		}
	})

	t.Run("registering twice keeps the first period", func(t *testing.T) {
		c := &testClock{at: t0}
		r := New(c.Now)
		r.Register("sweep", domain.HealthLoop, time.Second)
		r.Register("sweep", domain.HealthLoop, 10*time.Second)
		r.OK("sweep")
		c.at = c.at.Add(4 * time.Second)
		p := only(t, r)
		if p.Kind != string(domain.HealthLoop) {
			t.Errorf("kind = %s, want loop", p.Kind)
		}
		if p.State != string(domain.HealthStale) {
			t.Errorf("state = %s, want stale on the first period's clock", p.State)
		}
	})

	t.Run("components answers copies sorted by name and a nil registry answers none", func(t *testing.T) {
		c := &testClock{at: t0}
		r := New(c.Now)
		r.OK("sweep")
		r.Fail("hub.db", errors.New("locked"))
		got := r.Components()
		if len(got) != 2 {
			t.Fatalf("Components = %+v, want two records", got)
		}
		for i, want := range []string{"hub.db", "sweep"} {
			if got[i].Name != want {
				t.Errorf("record %d = %s, want %s", i, got[i].Name, want)
			}
		}
		if got[0].LastError != "locked" || got[1].LastOKAt != t0.UnixMilli() {
			t.Errorf("Components = %+v, want each record's own reports", got)
		}
		// the records are copies: editing the answer leaves the registry's own untouched
		got[1].LastOKAt = 0
		if again := r.Components(); again[1].LastOKAt != t0.UnixMilli() {
			t.Errorf("Components handed out the registry's own records: %+v", again)
		}
		var nilRegistry *Registry
		if parts := nilRegistry.Components(); parts == nil || len(parts) != 0 {
			t.Errorf("a nil registry answered %+v, want an empty non-nil slice", parts)
		}
	})

	t.Run("ok without a register shows as a store", func(t *testing.T) {
		c := &testClock{at: t0}
		r := New(c.Now)
		r.OK("hub.db")
		p := only(t, r)
		if p.Name != "hub.db" || p.Kind != string(domain.HealthStore) {
			t.Errorf("part = %s/%s, want hub.db/store", p.Name, p.Kind)
		}
		if p.State != string(domain.HealthOK) {
			t.Errorf("state = %s, want ok", p.State)
		}
	})
}
