package channel

import (
	"context"
	"testing"
	"time"
)

// fakeLiveness answers from a map: a member the map lists is known and answers its value; a
// member it does not list is not known.
type fakeLiveness map[string]bool

func (f fakeLiveness) MemberAlive(ctx context.Context, channel, member string, now time.Time) (bool, bool) {
	alive, known := f[member]
	return alive, known
}

// TestPresenceAsksTheLivenessSeamBeforeTheAge pins the seam's rule: a member the liveness
// answerer knows reads its answer instead of its last_seen age, and the age rule stays the
// answer for a member it never heard of.
func TestPresenceAsksTheLivenessSeamBeforeTheAge(t *testing.T) {
	ctx := bg
	ch := openChannel(t, "t")
	clock := ch.Now()
	ch.Now = func() time.Time { return clock }
	if _, err := ch.Join(ctx, "old", JoinOpts{Role: sp("builds")}); err != nil {
		t.Fatalf("join old: %v", err)
	}
	if _, err := ch.Join(ctx, "fresh", JoinOpts{Role: sp("tests")}); err != nil {
		t.Fatalf("join fresh: %v", err)
	}
	if err := ch.Store.Run(ctx, "UPDATE sessions SET last_seen=? WHERE name=?",
		clock.Add(-20*time.Minute).UTC().Format("2006-01-02T15:04:05Z"), "old"); err != nil {
		t.Fatalf("age old: %v", err)
	}

	staleOf := func(t *testing.T) map[string]bool {
		t.Helper()
		sessions, err := ch.Sessions(ctx)
		if err != nil {
			t.Fatalf("sessions: %v", err)
		}
		stale := map[string]bool{}
		for _, s := range sessions {
			stale[s.Name] = s.Stale
		}
		return stale
	}

	// no answerer: the age rule says who is stale
	if got := staleOf(t); !got["old"] || got["fresh"] {
		t.Fatalf("age rule = %v, want old stale and fresh not", got)
	}

	// the answerer's word outranks the age
	ch.Liveness = fakeLiveness{"old": true, "fresh": false}
	if got := staleOf(t); got["old"] || !got["fresh"] {
		t.Fatalf("liveness answer = %v, want old alive and fresh stale", got)
	}

	// a member the answerer never heard of falls back to the age rule
	ch.Liveness = fakeLiveness{}
	if got := staleOf(t); !got["old"] || got["fresh"] {
		t.Fatalf("unknown members = %v, want the age rule again", got)
	}
}
