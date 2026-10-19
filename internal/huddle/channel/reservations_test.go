package channel

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// Reservations: a session claims the paths it is about to change, and another session that
// reaches for one is told who holds it. One row per path; a directory covers everything under
// it; rows of sessions that left and expired rows are ignored and swept; nothing is ever
// blocked — a reserve that finds the path taken still answers, it just does not claim it.
func TestReserve(t *testing.T) {
	t.Run("a free path is held; the same path by another session is taken, with who and until", func(t *testing.T) {
		ch := joinedChannel(t)
		r, err := RunOp(bg, "reserve", ch, "a", vv(t, `{"paths": ["/r/shop/src/cart.ts", "/r/shop/README.md"], "note": "rewriting the cart"}`))
		if err != nil {
			t.Fatal(err)
		}
		got := r.Result.(ReserveResult)
		if len(got.Paths) != 2 || !got.Paths[0].Held || !got.Paths[1].Held {
			t.Fatalf("reserve = %+v, want both held", got.Paths)
		}
		if got.Paths[0].Path != "/r/shop/src/cart.ts" {
			t.Errorf("path = %q, want /r/shop/src/cart.ts", got.Paths[0].Path)
		}
		second, err := RunOp(bg, "reserve", ch, "b", vv(t, `{"paths": ["/r/shop/src/cart.ts"]}`))
		if err != nil {
			t.Fatal(err)
		}
		p := second.Result.(ReserveResult).Paths[0]
		if p.Held {
			t.Errorf("b's reserve = %+v, want the path taken", p)
		}
		if deref(p.Holder) != "a" || p.Until == nil || !strings.HasPrefix(deref(p.Until), "20") {
			t.Errorf("taken path = %+v, want holder a and an until", p)
		}
		if text := deref(second.Text); !strings.Contains(text, "/r/shop/src/cart.ts taken by a until ") {
			t.Errorf("reserve text = %q", text)
		}
		if text := deref(r.Text); !strings.Contains(text, "/r/shop/src/cart.ts held") {
			t.Errorf("reserve text = %q, want the held line", text)
		}
	})

	t.Run("re-reserving your own path extends it; ttl_minutes clamps to 1..480, default 60", func(t *testing.T) {
		ch := joinedChannel(t)
		clock := ch.Now()
		ch.Now = func() time.Time { return clock }
		if _, err := RunOp(bg, "reserve", ch, "a", vv(t, `{"paths": ["/r/a.ts"]}`)); err != nil {
			t.Fatal(err)
		}
		expires := reservationExpires(t, ch, "/r/a.ts")
		want := isoOfMs(clock.UnixMilli() + 60*60_000)
		if expires != want {
			t.Fatalf("default ttl expires = %s, want %s", expires, want)
		}
		// an hour later, a re-reserve for 2 h extends from now, not from the old expiry
		clock = clock.Add(time.Hour)
		if _, err := RunOp(bg, "reserve", ch, "a", vv(t, `{"paths": ["/r/a.ts"], "ttl_minutes": 120}`)); err != nil {
			t.Fatal(err)
		}
		expires = reservationExpires(t, ch, "/r/a.ts")
		if want := isoOfMs(clock.UnixMilli() + 120*60_000); expires != want {
			t.Errorf("extended expires = %s, want %s", expires, want)
		}
		// out of range both ways
		if _, err := RunOp(bg, "reserve", ch, "b", vv(t, `{"paths": ["/r/b.ts"], "ttl_minutes": 0}`)); err != nil {
			t.Fatal(err)
		}
		if got := reservationExpires(t, ch, "/r/b.ts"); got != isoOfMs(clock.UnixMilli()+60_000) {
			t.Errorf("ttl 0 expires = %s, want one minute", got)
		}
		if _, err := RunOp(bg, "reserve", ch, "b", vv(t, `{"paths": ["/r/c.ts"], "ttl_minutes": 100000}`)); err != nil {
			t.Fatal(err)
		}
		if got := reservationExpires(t, ch, "/r/c.ts"); got != isoOfMs(clock.UnixMilli()+480*60_000) {
			t.Errorf("ttl 100000 expires = %s, want the 480-minute cap", got)
		}
	})

	t.Run("one row per path: a cleaned duplicate claims once", func(t *testing.T) {
		ch := joinedChannel(t)
		r, err := RunOp(bg, "reserve", ch, "a", vv(t, `{"paths": ["/r/d/", "/r/d/./", "/r/d"]}`))
		if err != nil {
			t.Fatal(err)
		}
		got := r.Result.(ReserveResult)
		if len(got.Paths) != 1 || got.Paths[0].Path != "/r/d" {
			t.Errorf("paths = %+v, want one cleaned /r/d", got.Paths)
		}
	})

	t.Run("paths must be absolute", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := RunOp(bg, "reserve", ch, "a", vv(t, `{"paths": ["src/a.ts"]}`)); err == nil || errStatus(err) != 400 {
			t.Errorf("relative path error = %v, want 400", err)
		}
		if _, err := RunOp(bg, "reserve", ch, "a", vv(t, `{"paths": []}`)); err == nil || errStatus(err) != 400 {
			t.Errorf("no paths error = %v, want 400", err)
		}
	})

	t.Run("release takes the named paths, or all of yours; another session's path is not yours to release", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := RunOp(bg, "reserve", ch, "a", vv(t, `{"paths": ["/r/one.ts", "/r/two.ts"]}`)); err != nil {
			t.Fatal(err)
		}
		if _, err := RunOp(bg, "reserve", ch, "b", vv(t, `{"paths": ["/r/theirs.ts"]}`)); err != nil {
			t.Fatal(err)
		}
		r, err := RunOp(bg, "release", ch, "a", vv(t, `{"paths": ["/r/one.ts", "/r/theirs.ts", "/r/never-held.ts"]}`))
		if err != nil {
			t.Fatal(err)
		}
		got := r.Result.(ReleaseResult)
		if !slices.Equal(got.Released, []string{"/r/one.ts"}) {
			t.Errorf("released = %v, want /r/one.ts", got.Released)
		}
		all, err := RunOp(bg, "release", ch, "a", vv(t, `{}`))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(all.Result.(ReleaseResult).Released, []string{"/r/two.ts"}) {
			t.Errorf("release all = %v, want /r/two.ts", all.Result.(ReleaseResult).Released)
		}
		left, err := RunOp(bg, "reservations", ch, "b", vv(t, `{}`))
		if err != nil {
			t.Fatal(err)
		}
		if got := left.Result.(ReservationsView).Reservations; len(got) != 1 || got[0].Path != "/r/theirs.ts" {
			t.Errorf("reservations after the releases = %+v, want b's one", got)
		}
	})

	t.Run("reservations lists the live ones newest first, with notes, mine when asked", func(t *testing.T) {
		ch := joinedChannel(t)
		clock := ch.Now()
		ch.Now = func() time.Time { return clock }
		if _, err := RunOp(bg, "reserve", ch, "a", vv(t, `{"paths": ["/r/old.ts"], "note": "migrating the schema"}`)); err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(2 * time.Second)
		if _, err := RunOp(bg, "reserve", ch, "b", vv(t, `{"paths": ["/r/new.ts"]}`)); err != nil {
			t.Fatal(err)
		}
		list, err := RunOp(bg, "reservations", ch, "a", vv(t, `{}`))
		if err != nil {
			t.Fatal(err)
		}
		got := list.Result.(ReservationsView).Reservations
		if len(got) != 2 || got[0].Path != "/r/new.ts" || got[1].Path != "/r/old.ts" {
			t.Fatalf("reservations = %+v, want new first", got)
		}
		if got[0].Holder != "b" || got[1].Holder != "a" {
			t.Errorf("holders = %q, %q", got[0].Holder, got[1].Holder)
		}
		if deref(got[1].Note) != "migrating the schema" || got[0].Note != nil {
			t.Errorf("notes = %+v", got)
		}
		mine, err := RunOp(bg, "reservations", ch, "a", vv(t, `{"mine": true}`))
		if err != nil {
			t.Fatal(err)
		}
		if got := mine.Result.(ReservationsView).Reservations; len(got) != 1 || got[0].Path != "/r/old.ts" {
			t.Errorf("mine = %+v, want a's one", got)
		}
		if text := deref(list.Text); !strings.Contains(text, "/r/new.ts") || !strings.Contains(text, "b") ||
			!strings.Contains(text, "migrating the schema") {
			t.Errorf("reservations text = %q", text)
		}
	})
}

// reservationExpires reads one reservation's expires straight off the store.
func reservationExpires(t *testing.T, ch *Channel, path string) string {
	t.Helper()
	row, err := ch.dbGet(bg, "SELECT expires FROM reservations WHERE path=?", path)
	if err != nil {
		t.Fatal(err)
	}
	if row == nil {
		t.Fatalf("no reservation for %s", path)
	}
	return rStr(row, "expires")
}

// dataPaths reads one event's paths array.
func dataPaths(v *jsfmt.Value) []string {
	if v == nil || v.Kind() != jsfmt.KindObject {
		return nil
	}
	for i, k := range v.Keys() {
		if k == "paths" {
			f := v.Fields()[i]
			if f == nil || f.Kind() != jsfmt.KindArray {
				return nil
			}
			out := []string{}
			for _, item := range f.Items() {
				out = append(out, strOf(item))
			}
			return out
		}
	}
	return nil
}

// Expiry and presence: an expired row and the rows of a session that left are ignored and swept.
func TestReservationsExpireAndLeave(t *testing.T) {
	t.Run("an expired reservation is ignored and swept", func(t *testing.T) {
		ch := joinedChannel(t)
		clock := ch.Now()
		ch.Now = func() time.Time { return clock }
		if _, err := RunOp(bg, "reserve", ch, "a", vv(t, `{"paths": ["/r/gone.ts"], "ttl_minutes": 1}`)); err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(2 * time.Minute)
		r, err := RunOp(bg, "reserve", ch, "b", vv(t, `{"paths": ["/r/gone.ts"]}`))
		if err != nil {
			t.Fatal(err)
		}
		if p := r.Result.(ReserveResult).Paths[0]; !p.Held {
			t.Errorf("b's reserve = %+v, want the expired path claimable", p)
		}
		n, err := ch.dbGet(bg, "SELECT COUNT(*) n FROM reservations WHERE holder='a'")
		if err != nil {
			t.Fatal(err)
		}
		if rInt(n, "n") != 0 {
			t.Errorf("a's expired row stayed: %v", n)
		}
	})

	t.Run("a session that leaves releases its reservations, and the channel hears it", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := RunOp(bg, "reserve", ch, "a", vv(t, `{"paths": ["/r/left.ts", "/r/also-left.ts"]}`)); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Leave(bg, "a", "done"); err != nil {
			t.Fatal(err)
		}
		r, err := RunOp(bg, "reserve", ch, "b", vv(t, `{"paths": ["/r/left.ts"]}`))
		if err != nil {
			t.Fatal(err)
		}
		if p := r.Result.(ReserveResult).Paths[0]; !p.Held {
			t.Errorf("b's reserve = %+v, want the leaver's path claimable", p)
		}
		if n, err := ch.dbGet(bg, "SELECT COUNT(*) n FROM reservations WHERE holder='a'"); err != nil {
			t.Fatal(err)
		} else if rInt(n, "n") != 0 {
			t.Errorf("the leaver's rows stayed: %v", n)
		}
		evs, err := ch.Events(bg, EventsFilter{Topic: "reserve.release"})
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) != 1 || evs[0].From != "a" {
			t.Fatalf("reserve.release events = %+v, want one from a", evs)
		}
		if got := dataPaths(evs[0].Data); !slices.Equal(got, []string{"/r/left.ts", "/r/also-left.ts"}) {
			t.Errorf("the release event's paths = %v", got)
		}
	})

	t.Run("a subagent's reservation is its session's", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := RunOp(bg, "reserve", ch, "a.helper", vv(t, `{"paths": ["/r/sub.ts"]}`)); err != nil {
			t.Fatal(err)
		}
		r, err := RunOp(bg, "reserve", ch, "b", vv(t, `{"paths": ["/r/sub.ts"]}`))
		if err != nil {
			t.Fatal(err)
		}
		if p := r.Result.(ReserveResult).Paths[0]; deref(p.Holder) != "a" {
			t.Errorf("holder = %q, want the parent a", deref(p.Holder))
		}
		// and the session itself is never warned about its own subagent's reservation
		own, err := RunOp(bg, "reserved", ch, "a", vv(t, `{"path": "/r/sub.ts"}`))
		if err != nil {
			t.Fatal(err)
		}
		if own.Result.(ReservedResult).Held {
			t.Errorf("a's lookup = %+v, want its own reservation silent", own.Result)
		}
	})
}

// The lookup the PreToolUse hook runs: who holds this file, or a directory above it.
func TestReservedLookup(t *testing.T) {
	t.Run("held by another, by a directory above, and silent for your own", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := RunOp(bg, "reserve", ch, "a", vv(t, `{"paths": ["/r/shop/src/cart.ts", "/r/dir"], "note": "the cart is mine"}`)); err != nil {
			t.Fatal(err)
		}
		file, err := RunOp(bg, "reserved", ch, "b", vv(t, `{"path": "/r/shop/src/cart.ts"}`))
		if err != nil {
			t.Fatal(err)
		}
		got := file.Result.(ReservedResult)
		if !got.Held || deref(got.Holder) != "a" || got.Until == nil {
			t.Fatalf("lookup = %+v, want a holding it", got)
		}
		if text := deref(file.Text); !strings.HasPrefix(text, "/r/shop/src/cart.ts is reserved by a until ") ||
			!strings.Contains(text, "(the cart is mine)") ||
			!strings.Contains(text, "Coordinate with them (huddle send a …) before changing it.") {
			t.Errorf("lookup text = %q", text)
		}
		deep, err := RunOp(bg, "reserved", ch, "b", vv(t, `{"path": "/r/dir/under/deep.ts"}`))
		if err != nil {
			t.Fatal(err)
		}
		if !deep.Result.(ReservedResult).Held {
			t.Errorf("a directory reservation must cover what is under it: %+v", deep.Result)
		}
		own, err := RunOp(bg, "reserved", ch, "a", vv(t, `{"path": "/r/shop/src/cart.ts"}`))
		if err != nil {
			t.Fatal(err)
		}
		if own.Result.(ReservedResult).Held || deref(own.Text) != "" {
			t.Errorf("own lookup = (%+v, %q), want no hold and no text", own.Result, deref(own.Text))
		}
		free, err := RunOp(bg, "reserved", ch, "b", vv(t, `{"path": "/r/elsewhere.ts"}`))
		if err != nil {
			t.Fatal(err)
		}
		if free.Result.(ReservedResult).Held {
			t.Errorf("a free path answered held: %+v", free.Result)
		}
	})

	t.Run("the same repository matches on the path inside it, whatever the checkout it lives in", func(t *testing.T) {
		ch := joinedChannel(t)
		// a works in one clone of the repository, b in another machine's; both report the same
		// repository identity, and the paths inside it decide
		if _, err := RunOp(bg, "reserve", ch, "a", vv(t,
			`{"paths": ["/Users/me/proj/web/hub/src/app.ts"], "repo": "github.com/org/proj", "root": "/Users/me/proj"}`)); err != nil {
			t.Fatal(err)
		}
		r, err := RunOp(bg, "reserved", ch, "b", vv(t,
			`{"path": "/srv/you/proj/web/hub/src/app.ts", "repo": "github.com/org/proj", "rel": "web/hub/src/app.ts"}`))
		if err != nil {
			t.Fatal(err)
		}
		if !r.Result.(ReservedResult).Held {
			t.Errorf("same repository, same relative path answered not held: %+v", r.Result)
		}
		// a directory reservation covers what is under it in the other checkout too
		if _, err := RunOp(bg, "reserve", ch, "a", vv(t,
			`{"paths": ["/Users/me/proj/web/hub/src"], "repo": "github.com/org/proj", "root": "/Users/me/proj"}`)); err != nil {
			t.Fatal(err)
		}
		deep, err := RunOp(bg, "reserved", ch, "b", vv(t,
			`{"path": "/srv/you/proj/web/hub/src/app.ts", "repo": "github.com/org/proj", "rel": "web/hub/src/app.ts"}`))
		if err != nil {
			t.Fatal(err)
		}
		if !deep.Result.(ReservedResult).Held {
			t.Errorf("a directory reservation must cover the same relative path: %+v", deep.Result)
		}
		other, err := RunOp(bg, "reserved", ch, "b", vv(t,
			`{"path": "/srv/you/other/web/hub/src/app.ts", "repo": "github.com/org/other", "rel": "web/hub/src/app.ts"}`))
		if err != nil {
			t.Fatal(err)
		}
		if other.Result.(ReservedResult).Held {
			t.Errorf("another repository answered held: %+v", other.Result)
		}
		// no repository reported: absolute paths only
		bare, err := RunOp(bg, "reserved", ch, "b", vv(t, `{"path": "/srv/you/proj/web/hub/src/app.ts"}`))
		if err != nil {
			t.Fatal(err)
		}
		if bare.Result.(ReservedResult).Held {
			t.Errorf("no repository reported answered held: %+v", bare.Result)
		}
		// an absolute match from the other checkout still works without any repository
		if _, err := RunOp(bg, "reserve", ch, "b", vv(t, `{"paths": ["/srv/shared/only.ts"]}`)); err != nil {
			t.Fatal(err)
		}
		abs, err := RunOp(bg, "reserved", ch, "a", vv(t, `{"path": "/srv/shared/only.ts"}`))
		if err != nil {
			t.Fatal(err)
		}
		if !abs.Result.(ReservedResult).Held {
			t.Errorf("an absolute reservation from another checkout answered not held: %+v", abs.Result)
		}
	})

	t.Run("a session that never joined is refused, and the tools are the three the agents see", func(t *testing.T) {
		ch := openChannel(t, "t")
		if _, err := RunOp(bg, "reserve", ch, "stranger", vv(t, `{"paths": ["/r/x.ts"]}`)); err == nil || errStatus(err) != 404 {
			t.Errorf("stranger reserve error = %v, want 404", err)
		}
		names := []string{}
		for _, td := range ToolDefs() {
			names = append(names, td.Name)
		}
		for _, want := range []string{"reserve", "release", "reservations"} {
			if !slices.Contains(names, want) {
				t.Errorf("ToolDefs lacks %s", want)
			}
		}
		if slices.Contains(names, "reserved") {
			t.Error("reserved is listed as a tool")
		}
	})
}

// The channel's timeline carries the claims and the releases.
func TestReserveEvents(t *testing.T) {
	ch := joinedChannel(t)
	if _, err := RunOp(bg, "reserve", ch, "a", vv(t, `{"paths": ["/r/e1.ts", "/r/e2.ts"]}`)); err != nil {
		t.Fatal(err)
	}
	claims, err := ch.Events(bg, EventsFilter{Topic: "reserve.claim"})
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 || claims[0].From != "a" {
		t.Fatalf("reserve.claim events = %+v, want one from a", claims)
	}
	if got := dataPaths(claims[0].Data); !slices.Equal(got, []string{"/r/e1.ts", "/r/e2.ts"}) {
		t.Errorf("the claim's paths = %v", got)
	}
	if _, err := RunOp(bg, "release", ch, "a", vv(t, `{"paths": ["/r/e1.ts"]}`)); err != nil {
		t.Fatal(err)
	}
	releases, err := ch.Events(bg, EventsFilter{Topic: "reserve.release"})
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 1 {
		t.Fatalf("reserve.release events = %+v, want one", releases)
	}
	// a reserve that claims nothing new says nothing
	if _, err := RunOp(bg, "reserve", ch, "b", vv(t, `{"paths": ["/r/e2.ts"]}`)); err != nil {
		t.Fatal(err)
	}
	again, err := ch.Events(bg, EventsFilter{Topic: "reserve.claim"})
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 {
		t.Errorf("a taken path published a claim: %+v", again)
	}
	// the topics are the channel's alone: publish cannot fake them
	if _, err := RunOp(bg, "publish", ch, "b", vv(t, `{"topic": "reserve.claim", "msg": "fake"}`)); err == nil || errStatus(err) != 400 {
		t.Errorf("publish reserve.claim error = %v, want 400", err)
	}
}
