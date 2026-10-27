package hub

// The satellite queue's reads and the answers a store that says no gives: the machine a session
// reports from, the events a link replays from its cursor, the newest cursor, the item a handout
// names, and the payloads both sides trade. Every failure path runs on a closed database, so a
// refused read is a value, never a panic.

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// TestSessionMachineNamesTheSessionsMachine pins what the hub reads a report's session against:
// the machine the session's own row carries, and no machine for a session it never heard of.
func TestSessionMachineNamesTheSessionsMachine(t *testing.T) {
	db := satelliteDB(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	if err := db.Report(ctx, Report{
		SessionID: "s-1", Machine: "mbp", LastEvent: "SessionStart", LastEventAt: jsscalar.ISOTime(now),
	}); err != nil {
		t.Fatalf("report: %v", err)
	}
	machine, known, err := db.SessionMachine(ctx, "s-1")
	if err != nil || !known || machine != "mbp" {
		t.Fatalf("the session's machine = (%q, %v, %v), want mbp", machine, known, err)
	}
	if machine, known, err := db.SessionMachine(ctx, "s-other"); err != nil || known || machine != "" {
		t.Fatalf("an unknown session = (%q, %v, %v), want no machine", machine, known, err)
	}
}

// TestNewestSeqStartsAtZeroAndMovesWithReports pins where a fresh link's cursor starts: 0 on an
// empty database, then the newest event's number, so a restart replays nothing the hub holds.
func TestNewestSeqStartsAtZeroAndMovesWithReports(t *testing.T) {
	db := satelliteDB(t)
	ctx := context.Background()
	if seq, err := db.NewestSeq(ctx); err != nil || seq != 0 {
		t.Fatalf("the newest seq of an empty database = %d, %v, want 0", seq, err)
	}
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	for _, session := range []string{"s-1", "s-2"} {
		if err := db.Report(ctx, Report{
			SessionID: session, Machine: "mbp", LastEvent: "UserPromptSubmit", LastEventAt: jsscalar.ISOTime(now),
		}); err != nil {
			t.Fatalf("report: %v", err)
		}
	}
	if seq, err := db.NewestSeq(ctx); err != nil || seq != 2 {
		t.Fatalf("the newest seq = %d, %v, want 2", seq, err)
	}
}

// TestEventsAfterAnswersTheEventsAfterTheCursor pins the replay's read: the events newer than
// the cursor, oldest first, at most the limit of them, and a torn row skipped without holding
// the ones behind it.
func TestEventsAfterAnswersTheEventsAfterTheCursor(t *testing.T) {
	db := satelliteDB(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	for _, session := range []string{"s-1", "s-2"} {
		if err := db.Report(ctx, Report{
			SessionID: session, Machine: "mbp", LastEvent: "UserPromptSubmit", LastEventAt: jsscalar.ISOTime(now),
		}); err != nil {
			t.Fatalf("report: %v", err)
		}
	}
	// a torn row is lost, never a blocker for the events after it
	if err := db.st.Run(ctx, "INSERT INTO events (session_id, at, kind, json) VALUES (?, ?, ?, ?)",
		"s-1", jsscalar.ISOTime(now), "UserPromptSubmit", "{torn"); err != nil {
		t.Fatalf("insert the torn row: %v", err)
	}
	events, err := db.EventsAfter(ctx, 0, 100)
	if err != nil || len(events) != 2 {
		t.Fatalf("the events after 0 = %d, %v, want the 2 whole ones", len(events), err)
	}
	if events[0].Seq != 1 || events[0].Report.SessionID != "s-1" ||
		events[1].Seq != 2 || events[1].Report.SessionID != "s-2" {
		t.Fatalf("the events = %+v, want seq 1 then 2 oldest first", events)
	}
	if events, err = db.EventsAfter(ctx, 1, 100); err != nil || len(events) != 1 || events[0].Seq != 2 {
		t.Fatalf("the events after 1 = %+v, %v, want only seq 2", events, err)
	}
	if events, err = db.EventsAfter(ctx, 0, 1); err != nil || len(events) != 1 {
		t.Fatalf("the events at the limit = %d, %v, want 1", len(events), err)
	}
	if events, err = db.EventsAfter(ctx, 99, 100); err != nil || len(events) != 0 {
		t.Fatalf("the events after the newest = %d, %v, want none", len(events), err)
	}
}

// TestPermissionItemByKeyAnswersTheItemAndNoOther pins how a decision handout finds its row: by
// the key it carried from the machine, and nil for a key nothing holds.
func TestPermissionItemByKeyAnswersTheItemAndNoOther(t *testing.T) {
	f := satWithItem(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	item, err := f.db.PermissionItemByKey(ctx, f.key, now)
	if err != nil || item == nil || item.ID != f.itemID {
		t.Fatalf("the item by key = %+v, %v, want the fixture's item", item, err)
	}
	if item, err = f.db.PermissionItemByKey(ctx, "no-such-key", now); err != nil || item != nil {
		t.Fatalf("an unknown key = (%+v, %v), want nil", item, err)
	}
}

// TestDecodeSatPayloadReadsWhatEncodeSatPayloadWrote pins the handout payloads' round trip, and
// that a payload no JSON can read is an error, never a silent zero body.
func TestDecodeSatPayloadReadsWhatEncodeSatPayloadWrote(t *testing.T) {
	body := SatRefusalBody{Key: "k-1", SessionID: "s-1", Decision: "allow", Message: "go ahead"}
	payload, err := EncodeSatPayload(body)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var back SatRefusalBody
	// the answers map keeps the body out of =='s reach, so the compare reads every field
	if err := DecodeSatPayload(payload, &back); err != nil || !reflect.DeepEqual(back, body) {
		t.Fatalf("the decoded body = %+v, %v, want what was encoded", back, err)
	}
	if err := DecodeSatPayload("{not json", &back); err == nil {
		t.Fatal("a torn payload decoded")
	}
	var message SatMessageBody
	if err := DecodeSatPayload(`{"session":"s-1","text":"hi"}`, &message); err != nil ||
		message.SessionID != "s-1" || message.Text != "hi" {
		t.Fatalf("the decoded message = %+v, %v", message, err)
	}
}

// TestExchangeSatelliteSpendsOneInviteAndAnswersTheRefusals pins the join's half: an unknown or
// expired invite buys nothing, one exchange mints the credential, and the spent invite joins
// nothing twice.
func TestExchangeSatelliteSpendsOneInviteAndAnswersTheRefusals(t *testing.T) {
	db := satelliteDB(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC).UnixMilli()
	if _, err := db.ExchangeSatellite(ctx, "hsat_no_such_invite_placeholder", "mbp", now); err == nil {
		t.Fatal("an unknown invite exchanged")
	}
	// an invite past its ttl is spent before it is ever used
	expired, err := db.MintSatelliteInvite(ctx, now)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := db.ExchangeSatellite(ctx, expired, "mbp", now+SatInviteTTL.Milliseconds()+1); err == nil {
		t.Fatal("an expired invite exchanged")
	}
	live, err := db.MintSatelliteInvite(ctx, now)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	credential, err := db.ExchangeSatellite(ctx, live, "mbp", now)
	if err != nil || credential == "" {
		t.Fatalf("the exchange = %q, %v, want a credential", credential, err)
	}
	if _, err := db.ExchangeSatellite(ctx, live, "mbp", now+1); err == nil {
		t.Fatal("a spent invite exchanged twice")
	}
}

// TestSatQueueRefusesOnAClosedDatabase proves every queue read and write answers a refused
// store as an error value: a closed database never panics, the callers log and go on.
func TestSatQueueRefusesOnAClosedDatabase(t *testing.T) {
	f := satWithItem(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	payload, err := EncodeSatPayload(SatRefusalBody{Key: f.key, SessionID: "s-1", Decision: "deny"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := f.db.SatEnqueue(ctx, "mbp", SatDecision, "s-1", f.itemID, 0, payload, jsscalar.ISOTime(now)); err == nil {
		t.Error("SatEnqueue stored on a closed database")
	}
	if _, err := f.db.SatOffer(ctx, "mbp", now); err == nil {
		t.Error("SatOffer read a closed database")
	}
	if _, err := f.db.MarkPermissionRefused(ctx, f.itemID, "no", jsscalar.ISOTime(now)); err == nil {
		t.Error("MarkPermissionRefused wrote on a closed database")
	}
	if _, err := f.db.EventsAfter(ctx, 0, 10); err == nil {
		t.Error("EventsAfter read a closed database")
	}
	if _, err := f.db.NewestSeq(ctx); err == nil {
		t.Error("NewestSeq read a closed database")
	}
	if _, _, err := f.db.SessionMachine(ctx, "s-1"); err == nil {
		t.Error("SessionMachine read a closed database")
	}
	if _, err := f.db.PermissionItemByKey(ctx, f.key, now); err == nil {
		t.Error("PermissionItemByKey read a closed database")
	}
	if _, err := f.db.ForwardList(ctx); err == nil {
		t.Error("ForwardList read a closed database")
	}
	if _, err := f.db.ExchangeSatellite(ctx, "hsat_closed_database_placeholder", "mbp", now.UnixMilli()); err == nil {
		t.Error("ExchangeSatellite wrote on a closed database")
	}
	if _, err := f.db.MintSatelliteInvite(ctx, now.UnixMilli()); err == nil {
		t.Error("MintSatelliteInvite wrote on a closed database")
	}
}

// TestForwardListAnswersTheOpenItems pins what a flush carries: the machine's open permission
// items, oldest first, whole.
func TestForwardListAnswersTheOpenItems(t *testing.T) {
	f := satWithItem(t)
	ctx := context.Background()
	items, err := f.db.ForwardList(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("the forward list = %d items, %v, want the fixture's one", len(items), err)
	}
	if items[0].Key != f.key || items[0].SessionID != "s-1" || items[0].Tool != "Bash" {
		t.Fatalf("the forwarded item = %+v, want the fixture's item", items[0])
	}
	// an input past the input limit is cut and marked, so an allow can never land on half a tool
	long := make([]byte, 9*1024)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := f.db.CreatePermissionItem(ctx, "s-1", "Bash", long, time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("create the long item: %v", err)
	}
	items, err = f.db.ForwardList(ctx)
	if err != nil || len(items) != 2 {
		t.Fatalf("the forward list = %d items, %v, want 2", len(items), err)
	}
	cut := false
	for _, item := range items {
		if item.Truncated {
			cut = true
		}
	}
	if !cut {
		t.Error("the long input lost its truncated mark")
	}
}

// TestSatEnqueueAndSatOfferKeepTheirColumns proves a handout survives its queue round trip with
// the ids it was enqueued under, so the hub can end the right rows when results come.
func TestSatEnqueueAndSatOfferKeepTheirColumns(t *testing.T) {
	f := satWithItem(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	payload, err := EncodeSatPayload(SatMessageBody{SessionID: "s-1", Text: "hello"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	id, err := f.db.SatEnqueue(ctx, "mbp", SatMessage, "s-1", f.itemID, 7, payload, jsscalar.ISOTime(now))
	if err != nil || id == 0 {
		t.Fatalf("enqueue = %d, %v", id, err)
	}
	handouts, err := f.db.SatOffer(ctx, "other-machine", now)
	if err != nil || len(handouts) != 0 {
		t.Fatalf("another machine's offer = %d handouts, %v, want none", len(handouts), err)
	}
	handouts, err = f.db.SatOffer(ctx, "mbp", now)
	if err != nil || len(handouts) != 1 {
		t.Fatalf("the offer = %d handouts, %v, want 1", len(handouts), err)
	}
	got := handouts[0]
	if got.ID != id || got.Kind != SatMessage || got.SessionID != "s-1" ||
		got.ItemID != f.itemID || got.MessageID != 7 || got.Payload != payload {
		t.Fatalf("the handout = %+v, want the enqueued row whole", got)
	}
	if _, _, err := f.db.SatResult(ctx, id, SatDelivered, "", jsscalar.ISOTime(now.Add(time.Second))); err != nil {
		t.Fatalf("the result: %v", err)
	}
	if moved, err := f.db.MarkPermissionRefused(ctx, f.itemID, "never answered", jsscalar.ISOTime(now.Add(time.Second))); err != nil || !moved {
		t.Fatalf("the refusal moved = %v, %v, want the item rewritten", moved, err)
	}
}

// TestSatResultKeepsASecondAnswerOut pins the hub's own guard: a row already answered is not
// answered again, so a replayed result writes nothing twice.
func TestSatResultKeepsASecondAnswerOut(t *testing.T) {
	f := satWithItem(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	payload, err := EncodeSatPayload(SatRefusalBody{Key: f.key, SessionID: "s-1", Decision: "allow"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	id, err := f.db.SatEnqueue(ctx, "mbp", SatDecision, "s-1", f.itemID, 0, payload, jsscalar.ISOTime(now))
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, live, err := f.db.SatResult(ctx, id, SatRefused, "not on my machine", jsscalar.ISOTime(now)); err != nil || !live {
		t.Fatalf("the first result = live %v, %v, want it taken", live, err)
	}
	if _, live, err := f.db.SatResult(ctx, id, SatDelivered, "", jsscalar.ISOTime(now.Add(time.Second))); err != nil || live {
		t.Fatalf("the second result = live %v, %v, want it held out", live, err)
	}
	if _, live, err := f.db.SatResult(ctx, 99999, SatDelivered, "", jsscalar.ISOTime(now)); err != nil || live {
		t.Fatalf("an unknown handout = live %v, %v, want it held out", live, err)
	}
}

// TestSatQueueAnswersABrokenStoreInsideItsTransactions proves the queue's writes answer a store
// that says no mid-transaction: the error travels out as a value and no row moves.
func TestSatQueueAnswersABrokenStoreInsideItsTransactions(t *testing.T) {
	f := satWithItem(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	payload, err := EncodeSatPayload(SatRefusalBody{Key: f.key, SessionID: "s-1", Decision: "deny"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	dropTable(t, f.db, "sat_queue")
	if _, err := f.db.SatEnqueue(ctx, "mbp", SatDecision, "s-1", f.itemID, 0, payload, jsscalar.ISOTime(now)); err == nil {
		t.Error("SatEnqueue stored a handout without its table")
	}
	dropTable(t, f.db, "pairings")
	if _, err := f.db.ConsumePairing(ctx, "digest", now.UnixMilli()); err == nil {
		t.Error("ConsumePairing spent a pairing without its table")
	}
	if _, err := f.db.MintSatelliteInvite(ctx, now.UnixMilli()); err == nil {
		t.Error("MintSatelliteInvite minted without the pairings table")
	}
	if _, err := f.db.ExchangeSatellite(ctx, "hsat_broken_store_placeholder_00", "mbp", now.UnixMilli()); err == nil {
		t.Error("ExchangeSatellite joined a machine without the pairings table")
	}

	// the audit row a satellite added owes is part of the save: a store that refuses it refuses
	// the save whole
	fresh := satelliteDB(t)
	dropTable(t, fresh, "satellites")
	if err := fresh.SaveSatellite(ctx, "digest", "mbp", now.UnixMilli()); err == nil {
		t.Error("SaveSatellite stored a machine without its table")
	}
	audited := satelliteDB(t)
	dropTable(t, audited, "audit")
	if err := audited.SaveSatellite(ctx, "digest", "mbp", now.UnixMilli()); err == nil {
		t.Error("SaveSatellite stored a satellite it could not audit")
	}

	// the sync and the withdrawal answer a store that lost its items table the same way, and so
	// does every push read
	itemsGone := satelliteDB(t)
	dropTable(t, itemsGone, "items")
	if err := itemsGone.SyncBlocked(ctx, nil, jsscalar.ISOTime(now)); err == nil {
		t.Error("SyncBlocked read a broken store")
	}
	if _, err := itemsGone.WithdrawPermissions(ctx, "s-1", jsscalar.ISOTime(now)); err == nil {
		t.Error("WithdrawPermissions read a broken store")
	}
	dropTable(t, itemsGone, "push_subscriptions")
	if _, err := itemsGone.PushSubscriptions(ctx); err == nil {
		t.Error("PushSubscriptions read a broken store")
	}
	if held, err := itemsGone.RemovePushSubscription(ctx, "https://push.example/1"); err == nil || held {
		t.Error("RemovePushSubscription deleted from a broken store")
	}
}

// TestKeyOfItemAnswersNothingForAnUnknownItem proves a handout's lookup on a key the registry
// never issued answers empty, never another item's key.
func TestKeyOfItemAnswersNothingForAnUnknownItem(t *testing.T) {
	f := satWithItem(t)
	key, err := f.db.KeyOfItem(context.Background(), 99999)
	if err != nil || key != "" {
		t.Fatalf("the key of an unknown item = (%q, %v), want empty", key, err)
	}
}

// TestEncodeSatPayloadRefusesWhatNoJSONCanCarry proves a payload no encoder can render is an
// error, never an empty handout.
func TestEncodeSatPayloadRefusesWhatNoJSONCanCarry(t *testing.T) {
	if _, err := EncodeSatPayload(make(chan int)); err == nil {
		t.Fatal("a channel encoded")
	}
}

// TestUnknownNotifyErrorNamesTheThreeChoices pins the error a device's bad notification choice
// earns: the three the hub knows, so the caller can pick one.
func TestUnknownNotifyErrorNamesTheThreeChoices(t *testing.T) {
	got := (&UnknownNotifyError{Value: "sometimes"}).Error()
	for _, want := range []string{NotifyNeeds, NotifyAll, NotifyOff} {
		if !strings.Contains(got, want) {
			t.Errorf("the error %q does not name %q", got, want)
		}
	}
}

// TestSetDeviceNotifyRecordsTheChoiceTheDeviceMade pins the notification settings round trip:
// the choice a device makes is stored and read back whole, an unknown choice is refused as the
// error that names the three, and a device that is gone holds no choice.
func TestSetDeviceNotifyRecordsTheChoiceTheDeviceMade(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	if err := db.st.Run(ctx, "INSERT INTO devices (name, handle, credential, added, seen, notify) VALUES (?, ?, ?, ?, ?, ?)",
		"iPhone", []byte("handle"), "cred", 1, 1, NotifyNeeds); err != nil {
		t.Fatalf("seed the device: %v", err)
	}
	if err := db.SetDeviceNotify(ctx, "iPhone", NotifyAll); err != nil {
		t.Fatalf("set the choice: %v", err)
	}
	choice, found, err := db.DeviceNotify(ctx, "iPhone")
	if err != nil || !found || choice != NotifyAll {
		t.Fatalf("the choice = (%q, %v, %v), want all", choice, found, err)
	}
	var unknown *UnknownNotifyError
	if err := db.SetDeviceNotify(ctx, "iPhone", "sometimes"); !errors.As(err, &unknown) || unknown.Value != "sometimes" {
		t.Fatalf("an unknown choice = %v, want an UnknownNotifyError naming it", err)
	}
	if _, found, err := db.DeviceNotify(ctx, "gone"); err != nil || found {
		t.Fatalf("a gone device = found %v, %v, want it not found", found, err)
	}
}

// TestPairFinishesAnswerABrokenStore proves every write a pairing pays for answers a store that
// says no mid-transaction: a device cannot take a held name, a session cannot be paid twice,
// and a save that loses its audit, its alert or its devices refuses whole.
func TestPairFinishesAnswerABrokenStore(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	sessionExpires := pairNow.Add(12 * time.Hour)
	// each link is saved just before it is spent: a save sweeps the links past its own expiry
	pair := func(digest, device, session string) error {
		savePairing(t, db, digest, pairNow.Add(10*time.Minute))
		return db.PairFinish(ctx, digest, pairDevice(device), session, sessionExpires.UnixMilli(), pairNow.UnixMilli())
	}
	if err := pair("d-one", "iphone", "session-a"); err != nil {
		t.Fatalf("the first pair: %v", err)
	}
	// a second device cannot take a name the hub already holds
	if err := pair("d-two", "iphone", "session-b"); err == nil {
		t.Error("a second device took the held name")
	}
	// the spent link came back with the refused save, and still pays for one session only
	if err := pair("d-two", "ipad", "session-a"); err == nil {
		t.Error("a second session took the held digest")
	}
	dropTable(t, db, "audit")
	if err := pair("d-three", "mini", "session-c"); err == nil {
		t.Error("a pairing landed without its audit row")
	}
	dropTable(t, db, "alerts")
	if err := pair("d-four", "air", "session-d"); err == nil {
		t.Error("a pairing landed without its alert event")
	}
	dropTable(t, db, "devices")
	if err := db.LoginFinish(ctx, pairDevice("iphone"), "session-e", sessionExpires.UnixMilli(), pairNow.UnixMilli()); err == nil {
		t.Error("a sign-in landed without the devices table")
	}
	if removed, err := db.DeviceRemove(ctx, "iphone", pairNow.UnixMilli()); err == nil || removed {
		t.Fatalf("a revoke landed without the devices table = (%v, %v)", removed, err)
	}
}
