package hub

// The owner's messages to sessions (SPEC 6.3, 6.4): one row per session with the exact header,
// delivered through a channel push when the session's bridge acked one lately (route now) or
// taken by the listen hook on the next prompt (route next_prompt). The registry row says which
// route a message to the session would take.

import (
	"context"
	"testing"
	"time"
)

func TestTheHeadersAreExact(t *testing.T) {
	owner := MessageIn{SessionID: "s-1", FromKind: FromOwner, Text: "ship it"}
	if got := owner.Header(); got != "From the owner via huddle: ship it" {
		t.Errorf("the owner header = %q", got)
	}
	peer := MessageIn{SessionID: "s-1", FromKind: FromSession, FromName: "api", Text: "the digest"}
	want := "From session api via huddle (sent for the owner): the digest"
	if got := peer.Header(); got != want {
		t.Errorf("the session header = %q, want %q", got, want)
	}
}

func TestAChannelMessageNamesItsChannelAndSpeaker(t *testing.T) {
	post := MessageIn{SessionID: "s-1", FromKind: FromChannel, FromName: "kanban-release/api", Text: "the build is green"}
	want := "In #kanban-release, api said: the build is green"
	if got := post.Header(); got != want {
		t.Errorf("the channel header = %q, want %q", got, want)
	}
	// a member name of its own may carry a slash: the split is at the first one
	nested := MessageIn{SessionID: "s-1", FromKind: FromChannel, FromName: "kanban-release/parent.role", Text: "hello"}
	if got := nested.Header(); got != "In #kanban-release, parent.role said: hello" {
		t.Errorf("the nested member header = %q", got)
	}
}

func TestTakeAnswersPendingMessagesOldestFirstAndMarksThemDelivered(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	for _, in := range []MessageIn{
		{SessionID: "s-1", FromKind: FromOwner, Text: "first"},
		{SessionID: "s-1", FromKind: FromSession, FromName: "web", Text: "second"},
		{SessionID: "s-2", FromKind: FromOwner, Text: "another session's"},
	} {
		if _, err := db.QueueMessage(ctx, in, "2026-10-16T09:00:00.000Z"); err != nil {
			t.Fatalf("queue: %v", err)
		}
	}
	got, err := db.TakeMessages(ctx, "s-1", "2026-10-16T09:01:00.000Z")
	if err != nil {
		t.Fatalf("take: %v", err)
	}
	want := []string{
		"From the owner via huddle: first",
		"From session web via huddle (sent for the owner): second",
	}
	if len(got) != len(want) {
		t.Fatalf("taken = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("taken %d = %q, want %q", i, got[i], want[i])
		}
	}
	// the take is once: a second run answers nothing, and the other session's message stayed
	if again, _ := db.TakeMessages(ctx, "s-1", "2026-10-16T09:01:01.000Z"); len(again) != 0 {
		t.Errorf("a second take answered %q", again)
	}
	if left, _ := db.TakeMessages(ctx, "s-2", "2026-10-16T09:01:02.000Z"); len(left) != 1 {
		t.Errorf("the other session's message was taken away: %q", left)
	}
	// the rows carry the route they were delivered by
	rows, err := db.st.All(ctx, "SELECT session_id, state, route FROM messages ORDER BY id")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for i, want := range []struct{ session, state, route string }{
		{"s-1", "delivered", RouteNextPrompt},
		{"s-1", "delivered", RouteNextPrompt},
		{"s-2", "delivered", RouteNextPrompt},
	} {
		if rows[i]["session_id"] != want.session || rows[i]["state"] != want.state || rows[i]["route"] != want.route {
			t.Errorf("row %d = %v, want %v", i, rows[i], want)
		}
	}
}

func TestMarkDeliveredNamesThePushRoute(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	id, err := db.QueueMessage(ctx, MessageIn{SessionID: "s-p", FromKind: FromOwner, Text: "now"}, "2026-10-16T09:00:00.000Z")
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := db.MarkDelivered(ctx, id, RouteNow, "2026-10-16T09:00:01.000Z"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	row, err := db.st.Get(ctx, "SELECT state, route, delivered_at FROM messages WHERE id = ?", id)
	if err != nil || row == nil {
		t.Fatalf("the row: %v %v", row, err)
	}
	if row["state"] != "delivered" || row["route"] != RouteNow || row["delivered_at"] != "2026-10-16T09:00:01.000Z" {
		t.Errorf("the pushed row = %v", row)
	}
	// a pushed message is not the listen hook's to take
	if taken, _ := db.TakeMessages(ctx, "s-p", "2026-10-16T09:00:02.000Z"); len(taken) != 0 {
		t.Errorf("a pushed message was taken again: %q", taken)
	}
}

func TestTheRouteFollowsTheBridgeAcks(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	report := func(id, channel, member string) {
		t.Helper()
		if err := db.Report(ctx, Report{SessionID: id, Channel: channel, Member: member,
			LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T09:00:00.000Z"}); err != nil {
			t.Fatalf("report %s: %v", id, err)
		}
	}
	report("s-live", "shop", "api")
	report("s-quiet", "shop", "web")
	report("s-lonely", "", "") // in no channel: no bridge exists for it
	route := func(id string) string {
		t.Helper()
		row, err := db.SessionRow(ctx, id, readNow)
		if err != nil || row == nil {
			t.Fatalf("the row: %v %v", row, err)
		}
		return row.DeliveryRoute
	}
	if route("s-live") != RouteNextPrompt || route("s-quiet") != RouteNextPrompt || route("s-lonely") != RouteNextPrompt {
		t.Fatalf("before any ack every route = %s/%s/%s, want next_prompt", route("s-live"), route("s-quiet"), route("s-lonely"))
	}
	// the api bridge acks a channel push: its session's route turns now, the others stay
	if err := db.RecordPushAck(ctx, "shop", "api", "2026-10-16T09:00:20.000Z"); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if route("s-live") != RouteNow {
		t.Errorf("the acked session's route = %s, want now", route("s-live"))
	}
	if route("s-quiet") != RouteNextPrompt || route("s-lonely") != RouteNextPrompt {
		t.Errorf("an ack for one member moved another's route")
	}
	// the ack goes stale after ten minutes
	later := readNow.Add(11 * time.Minute)
	if got := db.routeOfMember(ctx, "shop", "api", later); got != RouteNextPrompt {
		t.Errorf("a stale ack still routes %s", got)
	}
	// a newer ack from the same member replaces the older one
	if err := db.RecordPushAck(ctx, "shop", "api", "2026-10-16T09:05:00.000Z"); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if got := db.routeOfMember(ctx, "shop", "api", readNow.Add(6*time.Minute)); got != RouteNow {
		t.Errorf("the newest ack routes %s, want now", got)
	}
}

func TestPendingOfAnswersTheMessagesWaitingOnOneMember(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	report := func(id, channel, member string) {
		t.Helper()
		if err := db.Report(ctx, Report{SessionID: id, Channel: channel, Member: member,
			LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T09:00:00.000Z"}); err != nil {
			t.Fatalf("report %s: %v", id, err)
		}
	}
	report("s-a", "shop", "api")
	report("s-b", "shop", "web")
	for _, in := range []MessageIn{
		{SessionID: "s-a", FromKind: FromOwner, Text: "wake"},
		{SessionID: "s-a", FromKind: FromOwner, Text: "and again"},
		{SessionID: "s-b", FromKind: FromOwner, Text: "not this one"},
	} {
		if _, err := db.QueueMessage(ctx, in, "2026-10-16T09:00:01.000Z"); err != nil {
			t.Fatalf("queue: %v", err)
		}
	}
	pending, err := db.PendingOf(ctx, "shop", "api")
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 2 || pending[0].In.Text != "wake" || pending[1].In.Text != "and again" {
		t.Fatalf("pending = %+v, want api's two oldest first", pending)
	}
}

func TestSessionOfMemberAnswersTheNewestSessionOfOneMember(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	for _, rep := range []Report{
		{SessionID: "s-old", Channel: "shop", Member: "api", LastEvent: "Stop", LastEventAt: "2026-10-16T08:00:00.000Z"},
		{SessionID: "s-new", Channel: "shop", Member: "api", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T09:00:00.000Z"},
		{SessionID: "s-web", Channel: "shop", Member: "web", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T09:00:30.000Z"},
	} {
		if err := db.Report(ctx, rep); err != nil {
			t.Fatalf("report: %v", err)
		}
	}
	if got, err := db.SessionOfMember(ctx, "shop", "api"); err != nil || got != "s-new" {
		t.Errorf("SessionOfMember(shop, api) = %q (%v), want s-new", got, err)
	}
	if got, err := db.SessionOfMember(ctx, "shop", "nope"); err != nil || got != "" {
		t.Errorf("SessionOfMember(shop, nope) = %q (%v), want none", got, err)
	}
}
