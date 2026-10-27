package channel

import (
	"context"
	"testing"
	"time"
)

// fakeLiveness answers from a map: a member the map lists is known and answers its registry
// state; a member it does not list is not known.
type fakeLiveness map[string]string

func (f fakeLiveness) MemberState(ctx context.Context, channel, member string, now time.Time) (string, bool) {
	state, known := f[member]
	return state, known
}

// TestPresenceCarriesTheRegistrysState pins the seam's rule: a member the liveness answerer
// knows carries the registry's state as its own and reads stale only when the registry says
// ended or stuck, whatever its last_seen age; a member the answerer never heard of carries no
// registry state and keeps the age rule.
func TestPresenceCarriesTheRegistrysState(t *testing.T) {
	ctx := bg
	ch := openChannel(t, "t")
	clock := ch.Now()
	ch.Now = func() time.Time { return clock }
	if _, err := ch.Join(ctx, "worker", JoinOpts{Role: sp("builds")}); err != nil {
		t.Fatalf("join worker: %v", err)
	}
	if _, err := ch.Join(ctx, "fresh", JoinOpts{Role: sp("tests")}); err != nil {
		t.Fatalf("join fresh: %v", err)
	}
	if err := ch.Store.Run(ctx, "UPDATE sessions SET last_seen=? WHERE name=?",
		clock.Add(-20*time.Minute).UTC().Format("2006-01-02T15:04:05Z"), "worker"); err != nil {
		t.Fatalf("age worker: %v", err)
	}

	viewOf := func(t *testing.T) map[string]SessionView {
		t.Helper()
		sessions, err := ch.Sessions(ctx)
		if err != nil {
			t.Fatalf("sessions: %v", err)
		}
		views := map[string]SessionView{}
		for _, s := range sessions {
			views[s.Name] = s
		}
		return views
	}

	// no answerer: the age rule says who is stale, and no view names a registry state
	views := viewOf(t)
	if !views["worker"].Stale || views["fresh"].Stale {
		t.Fatalf("age rule = %v and %v, want worker stale and fresh not",
			views["worker"].Stale, views["fresh"].Stale)
	}
	if views["worker"].Registry != "" || views["fresh"].Registry != "" {
		t.Fatalf("registry = %q and %q, want none without an answerer",
			views["worker"].Registry, views["fresh"].Registry)
	}

	// the registry's word outranks the age, and only ended and stuck read stale
	for _, state := range []string{"needs-you", "working", "idle", "stuck", "ended"} {
		ch.Liveness = fakeLiveness{"worker": state}
		views := viewOf(t)
		if views["worker"].Registry != state {
			t.Fatalf("registry = %q, want %q", views["worker"].Registry, state)
		}
		wantStale := state == "ended" || state == "stuck"
		if views["worker"].Stale != wantStale {
			t.Fatalf("state %s: stale = %v, want %v", state, views["worker"].Stale, wantStale)
		}
	}

	// a member the answerer never heard of carries no registry state and keeps the age rule,
	// while a member it does know reads its answer
	ch.Liveness = fakeLiveness{"fresh": "working"}
	views = viewOf(t)
	if views["worker"].Registry != "" {
		t.Fatalf("registry = %q, want none for an unknown member", views["worker"].Registry)
	}
	if !views["worker"].Stale || views["fresh"].Stale {
		t.Fatalf("mixed knowledge = %v and %v, want worker stale by age and fresh not",
			views["worker"].Stale, views["fresh"].Stale)
	}
}
