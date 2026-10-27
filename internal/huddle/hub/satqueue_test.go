package hub

import (
	"context"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// satFixture grows one forwarded permission item on one machine's session, the shape every
// queue test reads.
type satFixture struct {
	db     *DB
	itemID int64
	key    string
}

func satWithItem(t *testing.T) satFixture {
	t.Helper()
	db := satelliteDB(t)
	ctx := context.Background()
	id, err := db.CreatePermissionItem(ctx, "s-1", "Bash", []byte(`{"command":"npm test"}`),
		time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("create the item: %v", err)
	}
	key, err := db.KeyOfItem(ctx, id)
	if err != nil || key == "" {
		t.Fatalf("the item's key = %q, %v", key, err)
	}
	return satFixture{db: db, itemID: id, key: key}
}

// TestSatOfferHandsOnceAndAgain pins the offer clock (SPEC 6.7): a poll takes a pending row and
// the next poll right after takes nothing, because the row is out; after SatReofferAfter the
// same row is offered again, so a satellite that never answers strands nothing.
func TestSatOfferHandsOnceAndAgain(t *testing.T) {
	f := satWithItem(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	payload, err := EncodeSatPayload(SatRefusalBody{Key: f.key, SessionID: "s-1", Decision: "allow"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	id, err := f.db.SatEnqueue(ctx, "mbp", SatDecision, "s-1", f.itemID, 0, payload, jsscalar.ISOTime(now))
	if err != nil || id == 0 {
		t.Fatalf("enqueue = %d, %v", id, err)
	}
	handouts, err := f.db.SatOffer(ctx, "mbp", now)
	if err != nil || len(handouts) != 1 {
		t.Fatalf("the first offer = %d handouts, %v", len(handouts), err)
	}
	if handouts[0].ID != id || handouts[0].Kind != SatDecision || handouts[0].Payload != payload {
		t.Fatalf("the handout = %+v, want the decision under id %d", handouts[0], id)
	}
	again, err := f.db.SatOffer(ctx, "mbp", now.Add(time.Second))
	if err != nil || len(again) != 0 {
		t.Fatalf("an offer one second later answered %d handouts, %v, want none", len(again), err)
	}
	reoffered, err := f.db.SatOffer(ctx, "mbp", now.Add(SatReofferAfter))
	if err != nil || len(reoffered) != 1 || reoffered[0].ID != id {
		t.Fatalf("the re-offer after %s = %+v, %v, want the same handout", SatReofferAfter, reoffered, err)
	}
}

// TestSatResultEndsAndReoffers pins the three answers a satellite gives: delivered and refused
// end the row (a second answer is refused as late), busy moves the offer forward so the hub
// hands the row again after SatReofferAfter.
func TestSatResultEndsAndReoffers(t *testing.T) {
	f := satWithItem(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	payload, err := EncodeSatPayload(SatMessageBody{SessionID: "s-1", Text: "hello"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	enq := func() int64 {
		t.Helper()
		id, err := f.db.SatEnqueue(ctx, "mbp", SatMessage, "s-1", 0, 7, payload, jsscalar.ISOTime(now))
		if err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		if _, err := f.db.SatOffer(ctx, "mbp", now); err != nil {
			t.Fatalf("offer: %v", err)
		}
		return id
	}

	// busy keeps the row pending and moves its offer to the busy moment: it is handed again a
	// full SatReofferAfter from there, not before
	busy := enq()
	if handout, live, err := f.db.SatResult(ctx, busy, SatBusy, "", jsscalar.ISOTime(now.Add(time.Second))); err != nil || !live || handout.MessageID != 7 {
		t.Fatalf("busy = ( %+v, %v, %v ), want the handout live", handout, live, err)
	}
	if out, _ := f.db.SatOffer(ctx, "mbp", now.Add(time.Second+SatReofferAfter-time.Millisecond)); len(out) != 0 {
		t.Fatalf("the busy row was re-offered early: %d handouts", len(out))
	}
	out, err := f.db.SatOffer(ctx, "mbp", now.Add(time.Second+SatReofferAfter))
	if err != nil || len(out) != 1 || out[0].ID != busy {
		t.Fatalf("the re-offer = %d handouts, %v, want the busy row again", len(out), err)
	}

	// delivered ends the row: a second result for it is refused as late, and no offer carries it
	done := enq()
	if _, live, err := f.db.SatResult(ctx, done, SatDelivered, "", jsscalar.ISOTime(now.Add(time.Second))); err != nil || !live {
		t.Fatalf("delivered = ( %v, %v ), want live", live, err)
	}
	if _, live, _ := f.db.SatResult(ctx, done, SatRefused, "late", jsscalar.ISOTime(now.Add(2*time.Second))); live {
		t.Fatal("a done row took a second result")
	}
	if out, _ := f.db.SatOffer(ctx, "mbp", now.Add(2*time.Second+SatReofferAfter)); len(out) != 0 {
		t.Fatalf("a done row was offered again: %d handouts", len(out))
	}
}

// TestMarkPermissionRefusedRewritesBothStates proves the refusal lands whatever the hub row's
// state: one the owner's allow answered is rewritten to refused with the machine's reason, and
// one still open is answered refused — the machine's answer is an answer. A withdrawn row (the
// terminal answered itself) stays as it lays.
func TestMarkPermissionRefusedRewritesBothStates(t *testing.T) {
	f := satWithItem(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	// the second item stays open, the third is withdrawn by its terminal
	otherID, err := f.db.CreatePermissionItem(ctx, "s-2", "Read", []byte(`"/etc/hosts"`), now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	goneID, err := f.db.CreatePermissionItem(ctx, "s-3", "Bash", []byte(`{"command":"ls"}`), now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.db.WithdrawPermissions(ctx, "s-3", jsscalar.ISOTime(now)); err != nil {
		t.Fatalf("withdraw: %v", err)
	}

	if moved, err := f.db.MarkPermissionRefused(ctx, f.itemID, "refused: a deleting command; allow it in the terminal", jsscalar.ISOTime(now)); err != nil || !moved {
		t.Fatalf("the answered row refused = (%v, %v), want moved", moved, err)
	}
	item, err := f.db.PermissionItemByID(ctx, f.itemID, now)
	if err != nil || item == nil {
		t.Fatalf("read back: %v", err)
	}
	if item.State != StateAnswered || item.Decision != "refused" ||
		item.Message != "refused: a deleting command; allow it in the terminal" {
		t.Fatalf("the answered row = %s/%s/%q, want refused with the reason", item.State, item.Decision, item.Message)
	}

	if moved, err := f.db.MarkPermissionRefused(ctx, otherID, "not in this machine's remote allow list; allow it in the terminal", jsscalar.ISOTime(now)); err != nil || !moved {
		t.Fatalf("the open row refused = (%v, %v), want moved", moved, err)
	}
	other, _ := f.db.PermissionItemByID(ctx, otherID, now)
	if other.State != StateAnswered || other.Decision != "refused" {
		t.Fatalf("the open row = %s/%s, want answered refused", other.State, other.Decision)
	}

	if moved, err := f.db.MarkPermissionRefused(ctx, goneID, "too late", jsscalar.ISOTime(now)); err != nil || moved {
		t.Fatalf("the withdrawn row = (%v, %v), want untouched", moved, err)
	}
	gone, _ := f.db.PermissionItemByID(ctx, goneID, now)
	if gone.State != StateWithdrawn {
		t.Fatalf("the withdrawn row = %s, want withdrawn", gone.State)
	}
}

// TestTouchPollsOfMachineKeepsOnlyThatMachineAlive proves the link's poll is the proof of life
// for exactly its machine's items: the touched items stay open past the silent clock, and a
// foreign machine's item still expires.
func TestTouchPollsOfMachineKeepsOnlyThatMachineAlive(t *testing.T) {
	f := satWithItem(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	// a permission item grows with no session row of its own: the machine's report plants it
	for _, seed := range []struct{ id, machine string }{{"s-1", "mbp"}, {"s-9", "desk"}} {
		if err := f.db.st.Run(ctx,
			"INSERT INTO sessions (session_id, machine, last_event, last_event_at) VALUES (?, ?, 'Stop', ?)",
			seed.id, seed.machine, jsscalar.ISOTime(now)); err != nil {
			t.Fatalf("seed %s: %v", seed.id, err)
		}
	}
	foreign, err := f.db.CreatePermissionItem(ctx, "s-9", "Bash", []byte(`{"command":"ls"}`), now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.db.TouchPollsOfMachine(ctx, "mbp", jsscalar.ISOTime(now.Add(time.Minute))); err != nil {
		t.Fatalf("touch: %v", err)
	}
	if item, _ := f.db.PermissionItemByID(ctx, f.itemID, now.Add(80*time.Second)); item.State != StateOpen {
		t.Fatalf("the polled item reads %s after 80 s, want open", item.State)
	}
	if item, _ := f.db.PermissionItemByID(ctx, foreign, now.Add(80*time.Second)); item.State != StateExpired {
		t.Fatalf("the untouched item reads %s after 80 s, want expired", item.State)
	}
}

// TestForwardListAndSaveDedupeByKey proves the forward loop: every flush carries the whole open
// set, the hub grows one row per key however many flushes carry it, and an answered item stops
// being forwarded.
func TestForwardListAndSaveDedupeByKey(t *testing.T) {
	f := satWithItem(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	list, err := f.db.ForwardList(ctx)
	if err != nil || len(list) != 1 || list[0].Key != f.key || list[0].Tool != "Bash" {
		t.Fatalf("the forward list = %+v, %v", list, err)
	}
	hubDB := satelliteDB(t)
	in := ForwardedItem{Key: f.key, SessionID: "s-1", Tool: "Bash", Input: `{"command":"npm test"}`, CreatedAt: list[0].CreatedAt}
	for i := 0; i < 3; i++ {
		if err := hubDB.SaveForwardedItem(ctx, in); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	items, err := hubDB.Items(ctx, time.Now())
	if err != nil {
		t.Fatalf("items: %v", err)
	}
	count := 0
	for _, item := range items {
		if item.Kind == KindPermission {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%d permission items grew for one key, want 1", count)
	}
	// once the machine's own item is answered, the flush carries it no more
	if _, err := f.db.DecidePermission(ctx, f.itemID, "deny", nil, "", jsscalar.ISOTime(now.Add(time.Minute)), now.Add(time.Minute)); err != nil {
		t.Fatalf("decide: %v", err)
	}
	list, err = f.db.ForwardList(ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("an answered item was still forwarded: %+v, %v", list, err)
	}
}

// TestSatelliteInviteExchangeSpendsOnce proves the join's half of SPEC 6.7: the invite is one
// use — a second exchange with it buys nothing — and the exchanged credential resolves to the
// machine, so a satellite added carries its audit row and alert.
func TestSatelliteInviteExchangeSpendsOnce(t *testing.T) {
	db := satelliteDB(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	invite, err := db.MintSatelliteInvite(ctx, now)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	credential, err := db.ExchangeSatellite(ctx, invite, "mbp", now+1000)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if ok, _ := db.SatelliteKnown(ctx, "mbp"); !ok {
		t.Fatal("the exchanged machine is unknown")
	}
	if name, ok, _ := db.SatelliteSession(ctx, PairDigestOf(credential), now+2000); !ok || name != "mbp" {
		t.Fatalf("the credential answered (%q, %v), want mbp", name, ok)
	}
	if _, err := db.ExchangeSatellite(ctx, invite, "mbp", now+2000); err == nil {
		t.Fatal("a spent invite bought a second credential")
	}
	stale, err := db.MintSatelliteInvite(ctx, now)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := db.ExchangeSatellite(ctx, stale, "desk", now+SatInviteTTL.Milliseconds()+1); err == nil {
		t.Fatal("an expired invite bought a credential")
	}
}
