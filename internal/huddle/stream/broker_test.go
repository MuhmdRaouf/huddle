package stream

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// fakeClock is a settable clock for the broker's boot time.
type fakeClock struct{ at time.Time }

func (c *fakeClock) now() time.Time { return c.at }

func newTestBroker(bootMicros int64) (*Broker, *fakeClock) {
	clock := &fakeClock{at: time.UnixMicro(bootMicros)}
	return New(clock.now), clock
}

// staticSnapshotter answers every topic with the same rows.
type staticSnapshotter struct{ rows []Row }

func (s staticSnapshotter) Snapshot(context.Context, string) ([]Row, error) { return s.rows, nil }

// failingSnapshotter fails every topic.
type failingSnapshotter struct{ err error }

func (f failingSnapshotter) Snapshot(context.Context, string) ([]Row, error) { return nil, f.err }

func mustPublish(t *testing.T, b *Broker, topic string, op domain.StreamOp, id string, data string) uint64 {
	t.Helper()
	var raw json.RawMessage
	if data != "" {
		raw = json.RawMessage(data)
	}
	seq, err := b.Publish(topic, op, id, raw)
	if err != nil {
		t.Fatalf("Publish(%q): %v", topic, err)
	}
	return seq
}

func mustSubscribe(t *testing.T, b *Broker, req Request) *Subscription {
	t.Helper()
	sub, err := b.Subscribe(context.Background(), req)
	if err != nil {
		t.Fatalf("Subscribe(%+v): %v", req, err)
	}
	return sub
}

func receive(t *testing.T, sub *Subscription) domain.StreamFrame {
	t.Helper()
	select {
	case frame, ok := <-sub.Frames():
		if !ok {
			t.Fatalf("the subscription closed: %v", sub.Err())
		}
		return frame
	case <-time.After(5 * time.Second):
		t.Fatal("no frame within 5 s")
		return domain.StreamFrame{}
	}
}

func TestSeqStartsAtBootMicrosecondsAndNeverGoesBack(t *testing.T) {
	b, clock := newTestBroker(1_770_000_000_123_456)
	first := mustPublish(t, b, "sessions", domain.OpUpsert, "a", `{}`)
	if first != 1_770_000_000_123_456 {
		t.Fatalf("first seq = %d, want the boot time in microseconds", first)
	}
	clock.at = clock.at.Add(-time.Hour)
	second := mustPublish(t, b, "sessions", domain.OpUpsert, "a", `{}`)
	third := mustPublish(t, b, "needs", domain.OpTouch, "usage", "")
	if second != first+1 || third != second+1 {
		t.Fatalf("seqs = %d, %d, %d; want each one above the last even when the clock steps back", first, second, third)
	}
	if head := b.Head(); head != third {
		t.Fatalf("Head = %d, want %d", head, third)
	}
	restarted, _ := newTestBroker(1_770_000_000_123_456 + 5_000_000)
	if seq := mustPublish(t, restarted, "sessions", domain.OpUpsert, "a", `{}`); seq <= third {
		t.Fatalf("a restarted broker issued %d, not above the old process's %d", seq, third)
	}
}

func TestPublishRefusesDataThatIsNotJSONAndKeepsTheSeq(t *testing.T) {
	b, _ := newTestBroker(100)
	_, err := b.Publish("sessions", domain.OpUpsert, "a", json.RawMessage(`{broken`))
	var encode *EncodeError
	if !errors.As(err, &encode) || encode.Topic != "sessions" {
		t.Fatalf("Publish error = %v, want an *EncodeError for sessions", err)
	}
	if seq := mustPublish(t, b, "sessions", domain.OpUpsert, "a", `{}`); seq != 100 {
		t.Fatalf("seq after a refused publish = %d, want 100: a refused frame takes no seq", seq)
	}
}

func TestSubscribersCountsByTopicPrefix(t *testing.T) {
	b, _ := newTestBroker(1)
	mustSubscribe(t, b, Request{Topics: []string{"sessions"}})
	mustSubscribe(t, b, Request{Topics: []string{"channel:e2e", "channel:ops"}})
	mustSubscribe(t, b, Request{Topics: []string{"channel:e2e", "sessions"}})
	for prefix, want := range map[string]int{
		"":             3,
		"sessions":     2,
		"channel:":     2,
		"channel:e2e":  2,
		"channel:ops":  1,
		"radar.":       0,
		"channel:none": 0,
	} {
		if got := b.Subscribers(prefix); got != want {
			t.Errorf("Subscribers(%q) = %d, want %d", prefix, got, want)
		}
	}
}

func TestFramesAreEncodedOnceAndSharedAcrossSubscribers(t *testing.T) {
	b, _ := newTestBroker(1)
	one := mustSubscribe(t, b, Request{Topics: []string{"sessions"}})
	two := mustSubscribe(t, b, Request{Topics: []string{"sessions"}})
	mustPublish(t, b, "sessions", domain.OpUpsert, "a", `{"state":"busy"}`)
	live1, live2 := receive(t, one), receive(t, two)
	if &live1.Encoded[0] != &live2.Encoded[0] {
		t.Fatal("two subscribers got two copies of the encoded frame")
	}
	wire := `{"topic":"sessions","seq":1,"op":"upsert","id":"a","data":{"state":"busy"}}`
	if string(live1.Encoded) != wire {
		t.Fatalf("encoded frame = %s, want %s", live1.Encoded, wire)
	}
	late := mustSubscribe(t, b, Request{Topics: []string{"sessions"}, After: 1})
	mustPublish(t, b, "sessions", domain.OpUpsert, "b", `{}`)
	resumed := mustSubscribe(t, b, Request{Topics: []string{"sessions"}, After: 1})
	if len(resumed.Initial()) != 1 {
		t.Fatalf("resume replayed %d frames, want 1", len(resumed.Initial()))
	}
	liveB, replayedB := receive(t, late), resumed.Initial()[0]
	if &liveB.Encoded[0] != &replayedB.Encoded[0] {
		t.Fatal("the replayed frame is not the live frame's bytes")
	}
}

func TestASlowSubscriberIsClosedNotSkipped(t *testing.T) {
	b, _ := newTestBroker(1)
	slow := mustSubscribe(t, b, Request{Topics: []string{"sessions"}})
	fast := mustSubscribe(t, b, Request{Topics: []string{"sessions"}})
	var fastSeen int
	for i := 0; i < queueCap+1; i++ {
		mustPublish(t, b, "sessions", domain.OpUpsert, "a", `{}`)
		receive(t, fast)
		fastSeen++
	}
	if got := b.Subscribers(""); got != 1 {
		t.Fatalf("Subscribers = %d after the overflow, want 1: the slow one is gone", got)
	}
	var queued int
	for range slow.Frames() {
		queued++
	}
	if queued != queueCap {
		t.Fatalf("the slow subscriber held %d frames, want its full queue of %d", queued, queueCap)
	}
	var closed *ClosedError
	if err := slow.Err(); !errors.As(err, &closed) || closed.Reason != ReasonSlow {
		t.Fatalf("slow.Err() = %v, want a *ClosedError with ReasonSlow", err)
	}
	if fast.Err() != nil || fastSeen != queueCap+1 {
		t.Fatalf("the fast subscriber lost frames or closed: err=%v seen=%d", fast.Err(), fastSeen)
	}
}

func TestCloseReportsItsReason(t *testing.T) {
	b, _ := newTestBroker(1)
	byClient := mustSubscribe(t, b, Request{Topics: []string{"sessions"}})
	byClient.Close()
	byClient.Close()
	ctx, cancel := context.WithCancel(context.Background())
	byContext, err := b.Subscribe(ctx, Request{Topics: []string{"sessions"}})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	for range byContext.Frames() {
	}
	for name, tc := range map[string]struct {
		sub  *Subscription
		want CloseReason
	}{
		"closed by the client": {byClient, ReasonClosed},
		"context canceled":     {byContext, ReasonCanceled},
	} {
		var closed *ClosedError
		if err := tc.sub.Err(); !errors.As(err, &closed) || closed.Reason != tc.want {
			t.Errorf("%s: Err() = %v, want reason %q", name, err, tc.want)
		}
	}
	if got := b.Subscribers(""); got != 0 {
		t.Fatalf("Subscribers = %d, want 0", got)
	}
}

func TestCloseFreesTheSubscriberAndItsQueue(t *testing.T) {
	b, _ := newTestBroker(1)
	settle := func(want int) int {
		deadline := time.Now().Add(5 * time.Second)
		for runtime.NumGoroutine() > want && time.Now().Before(deadline) {
			runtime.Gosched()
			time.Sleep(time.Millisecond)
		}
		return runtime.NumGoroutine()
	}
	before := runtime.NumGoroutine()
	for i := 0; i < 1000; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		sub, err := b.Subscribe(ctx, Request{Topics: []string{"sessions", "channel:e2e"}})
		if err != nil {
			t.Fatal(err)
		}
		mustPublish(t, b, "sessions", domain.OpUpsert, "a", `{}`)
		if i%2 == 0 {
			sub.Close()
		} else {
			cancel()
		}
		cancel()
		if _, open := <-sub.Frames(); open {
			for range sub.Frames() {
			}
		}
	}
	if got := b.Subscribers(""); got != 0 {
		t.Fatalf("Subscribers = %d after 1,000 subscribe and close cycles, want 0", got)
	}
	if after := settle(before); after > before {
		t.Fatalf("goroutines = %d after the cycles, were %d before", after, before)
	}
}

func TestATopicWithoutASnapshotterIsGoneAndGetsNoSnapshotFrame(t *testing.T) {
	b, _ := newTestBroker(1)
	sub := mustSubscribe(t, b, Request{Topics: []string{"radar.touch"}})
	if len(sub.Initial()) != 0 {
		t.Fatalf("initial frames = %d, want none for a topic nobody registered", len(sub.Initial()))
	}
	if gone := sub.Gone(); len(gone) != 1 || gone[0] != "radar.touch" {
		t.Fatalf("gone = %v, want [radar.touch]", gone)
	}
}

func TestSubscribeNamesWhatItCannotServe(t *testing.T) {
	b, _ := newTestBroker(1)
	if _, err := b.Subscribe(context.Background(), Request{}); !errors.As(err, new(*NoTopicsError)) {
		t.Fatalf("no topics: error = %v, want *NoTopicsError", err)
	}
	cause := errors.New("registry offline")
	b.Register("sessions", failingSnapshotter{err: cause})
	_, err := b.Subscribe(context.Background(), Request{Topics: []string{"sessions"}})
	var failed *SnapshotError
	if !errors.As(err, &failed) || failed.Topic != "sessions" || !errors.Is(err, cause) {
		t.Fatalf("a failing snapshotter: error = %v, want a *SnapshotError wrapping the cause", err)
	}
	if got := b.Subscribers(""); got != 0 {
		t.Fatalf("a refused subscribe left %d subscribers", got)
	}
}

func TestTheLongestRegisteredPrefixAnswersASnapshot(t *testing.T) {
	b, _ := newTestBroker(1)
	b.Register("channel:", staticSnapshotter{rows: []Row{{ID: "x", Data: json.RawMessage(`1`)}}})
	b.Register("channel:ops", staticSnapshotter{rows: []Row{{ID: "y", Data: json.RawMessage(`2`)}}})
	sub := mustSubscribe(t, b, Request{Topics: []string{"channel:e2e", "channel:ops"}})
	var rows []string
	for _, frame := range sub.Initial() {
		rows = append(rows, frame.Message.Topic+"="+string(frame.Message.Data))
	}
	want := []string{`channel:e2e={"rows":[{"id":"x","data":1}]}`, `channel:ops={"rows":[{"id":"y","data":2}]}`}
	if len(rows) != 2 || rows[0] != want[0] || rows[1] != want[1] {
		t.Fatalf("snapshots = %v, want %v", rows, want)
	}
}

func TestATopicNamedTwiceIsServedOnce(t *testing.T) {
	b, _ := newTestBroker(1)
	b.Register("sessions", staticSnapshotter{})
	sub := mustSubscribe(t, b, Request{Topics: []string{"sessions", "needs", "sessions"}})
	if len(sub.Initial()) != 1 || len(sub.Gone()) != 2 {
		t.Fatalf("initial = %d frames, gone = %v; want one snapshot and two gone topics", len(sub.Initial()), sub.Gone())
	}
}
