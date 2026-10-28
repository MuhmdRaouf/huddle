package health

// The registry as the hub's parts meet it: a store reports its open, a loop reports each beat,
// and the snapshot names every part's state.

import (
	"errors"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

func TestTheHubKnowsEachPartsHealth(t *testing.T) {
	bdd.Feature(t, "the hub knows each part's health", func(f *bdd.F) {
		f.Scenario("a store that fails to open shows failing", func(s *bdd.S) {
			var r *Registry
			s.Given("a registry with a clock and no part in it", func(t *testing.T) {
				r = New((&testClock{at: time.UnixMilli(1_700_000_000_000)}).Now)
			})
			s.When("the channels store fails to open", func(t *testing.T) {
				r.Fail("channels.db", errors.New("database is locked"))
			})
			s.Then("the registry shows the store failing though nothing registered it", func(t *testing.T) {
				parts := r.Snapshot()
				if len(parts) != 1 {
					t.Fatalf("Snapshot = %+v, want the one store", parts)
				}
				p := parts[0]
				if p.Name != "channels.db" || p.Kind != "store" {
					t.Errorf("part = %s/%s, want channels.db/store", p.Name, p.Kind)
				}
				if p.State != "failing" {
					t.Errorf("state = %q, want failing", p.State)
				}
				if p.Errors != 1 || p.LastError != "database is locked" {
					t.Errorf("errors = %d on %q, want 1 on the open's error", p.Errors, p.LastError)
				}
			})
			s.And("the registry is not healthy", func(t *testing.T) {
				if r.Healthy() {
					t.Errorf("Healthy = true, want false while the store fails")
				}
			})
		})

		f.Scenario("a loop that recovers shows ok", func(s *bdd.S) {
			var r *Registry
			var c *testClock
			s.Given("a sweep loop with a one second period that reported ok", func(t *testing.T) {
				c = &testClock{at: time.UnixMilli(1_700_000_000_000)}
				r = New(c.Now)
				r.Register("sweep", "loop", time.Second)
				r.OK("sweep")
			})
			s.When("the sweep fails twice and then reports ok again", func(t *testing.T) {
				c.at = c.at.Add(500 * time.Millisecond)
				r.Fail("sweep", errors.New("read timeout"))
				c.at = c.at.Add(500 * time.Millisecond)
				r.Fail("sweep", errors.New("read timeout"))
				c.at = c.at.Add(100 * time.Millisecond)
				r.OK("sweep")
			})
			s.Then("the registry shows the loop ok with its two errors still counted", func(t *testing.T) {
				parts := r.Snapshot()
				if len(parts) != 1 {
					t.Fatalf("Snapshot = %+v, want the one loop", parts)
				}
				p := parts[0]
				if p.State != "ok" {
					t.Errorf("state = %q, want ok after the recovery", p.State)
				}
				if p.Errors != 2 {
					t.Errorf("errors = %d, want 2", p.Errors)
				}
				if !r.Healthy() {
					t.Errorf("Healthy = false, want true after the recovery")
				}
			})
		})

		f.Scenario("a loop that misses three beats shows stale", func(s *bdd.S) {
			var r *Registry
			var c *testClock
			s.Given("a sweep loop with a one second period that reported ok", func(t *testing.T) {
				c = &testClock{at: time.UnixMilli(1_700_000_000_000)}
				r = New(c.Now)
				r.Register("sweep", "loop", time.Second)
				r.OK("sweep")
			})
			s.When("three periods pass with no report", func(t *testing.T) {
				c.at = c.at.Add(3*time.Second + time.Millisecond)
			})
			s.Then("the registry shows the loop stale and unhealthy", func(t *testing.T) {
				parts := r.Snapshot()
				if len(parts) != 1 {
					t.Fatalf("Snapshot = %+v, want the one loop", parts)
				}
				if p := parts[0]; p.State != "stale" {
					t.Errorf("state = %q, want stale", p.State)
				}
				if r.Healthy() {
					t.Errorf("Healthy = true, want false while the loop is stale")
				}
			})
		})
	})
}
