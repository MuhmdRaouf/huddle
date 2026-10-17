package hub

// The Web Push storage (SPEC 7.6): one subscription per paired device's endpoint, the device's
// own notification choice beside it, and the items' push bookkeeping — one push at create, one
// escalation at five minutes, nothing once decided. A device's subscriptions die with the device.

import (
	"context"
	"testing"
	"time"
)

// pairOneDevice runs one pairing's writes and answers the device's name, the way a paired
// browser exists in the database.
func pairOneDevice(t *testing.T, db *DB, name string) {
	t.Helper()
	if err := db.SavePairing(context.Background(), "digest-"+name, 10_000); err != nil {
		t.Fatalf("save the pairing: %v", err)
	}
	dev := Device{Name: name, Handle: []byte("handle-" + name), CredentialJSON: []byte("{}")}
	if err := db.PairFinish(context.Background(), "digest-"+name, dev, "session-"+name, 2, 1); err != nil {
		t.Fatalf("finish the pairing: %v", err)
	}
}

func TestPushSubscriptionsStorePerDevice(t *testing.T) {
	db := openForTest(t)
	pairOneDevice(t, db, "phone")

	if err := db.SavePushSubscription(context.Background(), PushSubscription{
		Device: "phone", Endpoint: "https://push.example/one", P256dh: "p256dh-one", Auth: "auth-one",
	}, 100); err != nil {
		t.Fatalf("save the subscription: %v", err)
	}
	subs, err := db.PushSubscriptions(context.Background())
	if err != nil {
		t.Fatalf("list the subscriptions: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("the subscriptions = %v, want the one saved", subs)
	}
	want := PushSubscription{
		Device: "phone", Endpoint: "https://push.example/one", P256dh: "p256dh-one",
		Auth: "auth-one", Notify: NotifyNeeds, AddedMS: 100,
	}
	if subs[0] != want {
		t.Errorf("the subscription = %+v, want %+v", subs[0], want)
	}

	// a re-subscribe of the same endpoint under another device replaces the row: the endpoint is
	// the push service's own name for one subscription, whoever holds it now
	pairOneDevice(t, db, "tablet")
	if err := db.SavePushSubscription(context.Background(), PushSubscription{
		Device: "tablet", Endpoint: "https://push.example/one", P256dh: "p256dh-two", Auth: "auth-two",
	}, 200); err != nil {
		t.Fatalf("re-save the subscription: %v", err)
	}
	subs, err = db.PushSubscriptions(context.Background())
	if err != nil {
		t.Fatalf("list again: %v", err)
	}
	if len(subs) != 1 || subs[0].Device != "tablet" || subs[0].P256dh != "p256dh-two" || subs[0].AddedMS != 200 {
		t.Errorf("the replaced subscription = %+v, want tablet's fresh row", subs[0])
	}

	// the device's own choice rides the row: needs only by default, all when it asks for alerts
	if err := db.SetDeviceNotify(context.Background(), "tablet", NotifyAll); err != nil {
		t.Fatalf("set the choice: %v", err)
	}
	subs, _ = db.PushSubscriptions(context.Background())
	if subs[0].Notify != NotifyAll {
		t.Errorf("the choice = %q, want %q", subs[0].Notify, NotifyAll)
	}
}

func TestARemovedDeviceLosesItsSubscriptionsAndItsChoice(t *testing.T) {
	db := openForTest(t)
	pairOneDevice(t, db, "phone")
	pairOneDevice(t, db, "tablet")
	for _, device := range []string{"phone", "tablet"} {
		if err := db.SavePushSubscription(context.Background(), PushSubscription{
			Device: device, Endpoint: "https://push.example/" + device, P256dh: "k", Auth: "a",
		}, 1); err != nil {
			t.Fatalf("save %s's subscription: %v", device, err)
		}
	}
	if _, err := db.DeviceRemove(context.Background(), "phone", 2); err != nil {
		t.Fatalf("remove the device: %v", err)
	}
	subs, err := db.PushSubscriptions(context.Background())
	if err != nil {
		t.Fatalf("list the subscriptions: %v", err)
	}
	if len(subs) != 1 || subs[0].Device != "tablet" {
		t.Fatalf("the subscriptions after the removal = %+v, want tablet's only", subs)
	}
	if _, found, err := db.DeviceNotify(context.Background(), "phone"); err != nil || found {
		t.Errorf("the removed device's choice = (%v, %v), want gone", found, err)
	}
}

func TestRemovePushSubscriptionAnswersWhetherItHeldOne(t *testing.T) {
	db := openForTest(t)
	pairOneDevice(t, db, "phone")
	if err := db.SavePushSubscription(context.Background(), PushSubscription{
		Device: "phone", Endpoint: "https://push.example/one", P256dh: "k", Auth: "a",
	}, 1); err != nil {
		t.Fatalf("save: %v", err)
	}
	gone, err := db.RemovePushSubscription(context.Background(), "https://push.example/one")
	if err != nil || !gone {
		t.Fatalf("remove = (%v, %v), want the held subscription gone", gone, err)
	}
	gone, err = db.RemovePushSubscription(context.Background(), "https://push.example/one")
	if err != nil || gone {
		t.Fatalf("remove again = (%v, %v), want nothing left", gone, err)
	}
}

func TestThePushSweepReadsEachWaitingItemOnce(t *testing.T) {
	db := openForTest(t)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	// a permission item, a question and a dismissed one, all fresh
	perm := growPermission(t, db, "s-1", "Bash", `{"command":"npm test"}`, now)
	if err := db.Report(context.Background(), Report{
		SessionID: "s-2", LastEvent: "Notification", LastEventAt: isoPerm(now),
		Notification: &Notification{Type: "elicitation_dialog", Message: "Pick a database?"},
	}); err != nil {
		t.Fatalf("grow the question: %v", err)
	}
	if err := db.addItem(context.Background(), ItemIn{
		Key: "turn:s-3:1", SessionID: "s-3", Kind: KindTurn, Summary: "a finished turn",
	}, isoPerm(now)); err != nil {
		t.Fatalf("grow the turn: %v", err)
	}
	if err := db.DismissItem(context.Background(), perm); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	fresh, err := db.FreshPushItems(context.Background(), now.Add(time.Second))
	if err != nil {
		t.Fatalf("the fresh items: %v", err)
	}
	if len(fresh) != 2 {
		t.Fatalf("the fresh items = %v, want the question and the turn only", fresh)
	}
	for _, item := range fresh {
		if item.SessionID == "s-1" {
			t.Errorf("a dismissed item waits for a push: %+v", item)
		}
	}
	// the first pass pushed them: the second answers none
	for _, item := range fresh {
		if err := db.MarkItemPushed(context.Background(), item.ID, isoPerm(now.Add(time.Second))); err != nil {
			t.Fatalf("mark pushed: %v", err)
		}
	}
	if again, err := db.FreshPushItems(context.Background(), now.Add(2*time.Second)); err != nil || len(again) != 0 {
		t.Fatalf("the fresh items after the push = (%v, %v), want none", again, err)
	}

	// before five minutes nothing escalates; at five minutes the still-open items do, once
	if before, err := db.EscalatePushItems(context.Background(), now.Add(4*time.Minute)); err != nil || len(before) != 0 {
		t.Fatalf("the escalations before five minutes = (%v, %v), want none", before, err)
	}
	late, err := db.EscalatePushItems(context.Background(), now.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("the escalations: %v", err)
	}
	if len(late) != 2 {
		t.Fatalf("the escalations = %v, want the question and the turn", late)
	}
	for _, item := range late {
		if err := db.MarkItemEscalated(context.Background(), item.ID, isoPerm(now.Add(5*time.Minute))); err != nil {
			t.Fatalf("mark escalated: %v", err)
		}
	}
	if again, err := db.EscalatePushItems(context.Background(), now.Add(6*time.Minute)); err != nil || len(again) != 0 {
		t.Fatalf("the escalations after one = (%v, %v), want none", again, err)
	}
}

func TestThePushSweepSkipsWhatNoLongerWaits(t *testing.T) {
	db := openForTest(t)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	// a snoozed item does not wait on the owner while its snooze holds
	if err := db.addItem(context.Background(), ItemIn{
		Key: "question:s:1", SessionID: "s-1", Kind: KindQuestion, Summary: "Pick a database?",
	}, isoPerm(now)); err != nil {
		t.Fatalf("grow the question: %v", err)
	}
	rows, err := db.st.All(context.Background(), "SELECT id FROM items")
	if err != nil || len(rows) != 1 {
		t.Fatalf("the seeded items: %v (%v)", rows, err)
	}
	id := rowID(rows[0])
	if err := db.SnoozeItem(context.Background(), id, now.Add(time.Hour)); err != nil {
		t.Fatalf("snooze: %v", err)
	}
	if fresh, err := db.FreshPushItems(context.Background(), now.Add(time.Minute)); err != nil || len(fresh) != 0 {
		t.Fatalf("the fresh items behind a snooze = (%v, %v), want none", fresh, err)
	}

	// an expired permission item — its hook stopped polling — pushes nothing
	expired := growPermission(t, db, "s-2", "Bash", `{"command":"npm test"}`, now)
	_ = expired
	if fresh, err := db.FreshPushItems(context.Background(), now.Add(PermissionSilentPollAfter+time.Minute)); err != nil || len(fresh) != 0 {
		t.Fatalf("the fresh items with a dead prompt = (%v, %v), want none", fresh, err)
	}
	// a decided item never escalates: the push bookkeeping keeps its marks, the state refuses it
	decided := growPermission(t, db, "s-3", "Bash", `{"command":"npm test"}`, now)
	if err := db.MarkItemPushed(context.Background(), decided, isoPerm(now)); err != nil {
		t.Fatalf("mark pushed: %v", err)
	}
	if won, err := db.DecidePermission(context.Background(), decided, "deny", "", isoPerm(now), now.Add(time.Second)); err != nil || !won {
		t.Fatalf("decide = (%v, %v), want the decision to win", won, err)
	}
	if late, err := db.EscalatePushItems(context.Background(), now.Add(PermissionExpiresAfter)); err != nil || len(late) != 0 {
		t.Fatalf("the escalations of a decided item = (%v, %v), want none", late, err)
	}
}
