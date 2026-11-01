package domain

import (
	"bytes"
	"encoding/json"
	"testing"
)

// ringFrameFor builds one frame with its encoded wire form, the way the broker appends: the message
// encoded once, the bytes shared with the ring.
func ringFrameFor(t *testing.T, topic string, seq uint64, op StreamOp, id string, data json.RawMessage) StreamFrame {
	t.Helper()
	encoded, err := json.Marshal(StreamMessage{Topic: topic, Seq: seq, Op: op, ID: id, Data: data})
	if err != nil {
		t.Fatalf("marshal %s/%d: %v", topic, seq, err)
	}
	return StreamFrame{
		Message: StreamMessage{Topic: topic, Seq: seq, Op: op, ID: id, Data: data},
		Encoded: encoded,
	}
}

func TestRingReplaysOnlyTheRequestedTopicsAfterASeq(t *testing.T) {
	ring := &Ring{}
	appended := []StreamFrame{
		ringFrameFor(t, "sessions", 10, OpUpsert, "s1", nil),
		ringFrameFor(t, "needs", 11, OpUpsert, "n1", nil),
		ringFrameFor(t, "sessions", 12, OpUpsert, "s2", nil),
		ringFrameFor(t, "channel:e2e", 13, OpTouch, "task", nil),
	}
	for _, frame := range appended {
		ring.Append(frame)
	}

	frames, ok := ring.After(10, []string{"sessions"})
	if !ok {
		t.Fatal("After(10, sessions) reported gone inside the ring")
	}
	if len(frames) != 1 || frames[0].Message.Seq != 12 || frames[0].Message.Topic != "sessions" {
		t.Fatalf("After(10, sessions) = %+v, want only seq 12", frames)
	}
	if !bytes.Equal(frames[0].Encoded, appended[2].Encoded) {
		t.Fatal("the replayed frame's bytes differ from the appended frame")
	}

	frames, ok = ring.After(10, []string{"sessions", "channel:e2e"})
	if !ok || len(frames) != 2 || frames[0].Message.Seq != 12 || frames[1].Message.Seq != 13 {
		t.Fatalf("After(10, two topics) = %+v, want seqs 12 and 13 in order", frames)
	}

	frames, ok = ring.After(10, []string{"sessions", "needs", "channel:e2e"})
	if !ok || len(frames) != 3 || frames[0].Message.Seq != 11 || frames[1].Message.Seq != 12 || frames[2].Message.Seq != 13 {
		t.Fatalf("After(10, every topic) = %+v, want seqs 11, 12 and 13 in order", frames)
	}

	frames, ok = ring.After(12, []string{"needs"})
	if !ok {
		t.Fatal("After(12, needs) reported gone inside the ring")
	}
	if len(frames) != 0 {
		t.Fatalf("After(12, needs) = %+v, want no frames", frames)
	}
}

func TestRingReportsGoneBelowTheTailAboveTheHeadAndForZero(t *testing.T) {
	ring := &Ring{}
	if ring.Head() != 0 || ring.Tail() != 0 {
		t.Fatalf("empty ring Head/Tail = %d/%d, want 0/0", ring.Head(), ring.Tail())
	}
	if frames, ok := ring.After(5, []string{"sessions"}); ok || frames != nil {
		t.Fatalf("After on an empty ring = (%+v, %v), want gone", frames, ok)
	}

	for seq := uint64(100); seq <= 103; seq++ {
		ring.Append(ringFrameFor(t, "sessions", seq, OpUpsert, "s", nil))
	}
	if ring.Head() != 103 || ring.Tail() != 100 {
		t.Fatalf("Head/Tail = %d/%d, want 103/100", ring.Head(), ring.Tail())
	}

	if frames, ok := ring.After(99, []string{"sessions"}); ok || frames != nil {
		t.Fatalf("After below the tail = (%+v, %v), want gone", frames, ok)
	}
	if frames, ok := ring.After(104, []string{"sessions"}); ok || frames != nil {
		t.Fatalf("After above the head = (%+v, %v), want gone", frames, ok)
	}
	if frames, ok := ring.After(0, []string{"sessions"}); ok || frames != nil {
		t.Fatalf("After zero = (%+v, %v), want gone", frames, ok)
	}

	if frames, ok := ring.After(100, []string{"sessions"}); !ok || len(frames) != 3 {
		t.Fatalf("After at the tail = (%+v, %v), want the three newer frames", frames, ok)
	}
	if frames, ok := ring.After(103, []string{"sessions"}); !ok || len(frames) != 0 {
		t.Fatalf("After at the head = (%+v, %v), want caught up", frames, ok)
	}
}

func TestRingEvictsByCountAndByBytes(t *testing.T) {
	ring := &Ring{}
	for seq := uint64(1); seq <= maxRingFrames; seq++ {
		ring.Append(ringFrameFor(t, "sessions", seq, OpTouch, "usage", nil))
	}
	if ring.Tail() != 1 || ring.Head() != maxRingFrames {
		t.Fatalf("full ring Tail/Head = %d/%d, want 1/%d", ring.Tail(), ring.Head(), maxRingFrames)
	}
	ring.Append(ringFrameFor(t, "sessions", maxRingFrames+1, OpTouch, "usage", nil))
	if ring.Tail() != 2 || ring.Head() != maxRingFrames+1 {
		t.Fatalf("overfull ring Tail/Head = %d/%d, want 2/%d", ring.Tail(), ring.Head(), maxRingFrames+1)
	}
	if frames, ok := ring.After(2, []string{"sessions"}); !ok || len(frames) != maxRingFrames-1 {
		t.Fatalf("replay of the full ring = %d frames, want %d", len(frames), maxRingFrames-1)
	}

	const mib = 1 << 20
	byteRing := &Ring{}
	for seq := uint64(1); seq <= 5; seq++ {
		byteRing.Append(StreamFrame{
			Message: StreamMessage{Topic: "sessions", Seq: seq, Op: OpUpsert, ID: "big"},
			Encoded: make([]byte, mib),
		})
	}
	if byteRing.Tail() != 2 || byteRing.Head() != 5 {
		t.Fatalf("byte-evicted ring Tail/Head = %d/%d, want 2/5", byteRing.Tail(), byteRing.Head())
	}
	if frames, ok := byteRing.After(2, []string{"sessions"}); !ok || len(frames) != 3 {
		t.Fatalf("replay after the byte eviction = %d frames, want 3", len(frames))
	}

	huge := &Ring{}
	huge.Append(StreamFrame{
		Message: StreamMessage{Topic: "sessions", Seq: 7, Op: OpUpsert, ID: "huge"},
		Encoded: make([]byte, 5*mib),
	})
	if huge.Tail() != 7 || huge.Head() != 7 {
		t.Fatalf("ring with one oversized frame Tail/Head = %d/%d, want 7/7", huge.Tail(), huge.Head())
	}
}

func TestMessageEncodesTheFiveFieldsInOrder(t *testing.T) {
	upsert := StreamMessage{
		Topic: "sessions",
		Seq:   42,
		Op:    OpUpsert,
		ID:    "s1",
		Data:  json.RawMessage(`{"v":1}`),
	}
	encoded, err := json.Marshal(upsert)
	if err != nil {
		t.Fatalf("marshal the upsert: %v", err)
	}
	if want := `{"topic":"sessions","seq":42,"op":"upsert","id":"s1","data":{"v":1}}`; string(encoded) != want {
		t.Fatalf("upsert wire form = %s, want %s", encoded, want)
	}

	var decoded StreamMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal the upsert: %v", err)
	}
	if decoded.Topic != upsert.Topic || decoded.Seq != upsert.Seq || decoded.Op != upsert.Op || decoded.ID != upsert.ID ||
		!bytes.Equal(decoded.Data, upsert.Data) {
		t.Fatalf("round trip = %+v, want %+v", decoded, upsert)
	}

	cases := []struct {
		name    string
		message StreamMessage
		want    string
	}{
		{
			name:    "snapshot carries its rows",
			message: StreamMessage{Topic: "sessions", Seq: 41, Op: OpSnapshot, Data: json.RawMessage(`{"rows":[]}`)},
			want:    `{"topic":"sessions","seq":41,"op":"snapshot","id":"","data":{"rows":[]}}`,
		},
		{
			name:    "remove carries no data even when one was set",
			message: StreamMessage{Topic: "sessions", Seq: 43, Op: OpRemove, ID: "s1", Data: json.RawMessage(`{"v":1}`)},
			want:    `{"topic":"sessions","seq":43,"op":"remove","id":"s1"}`,
		},
		{
			name:    "touch carries no data even when one was set",
			message: StreamMessage{Topic: "radar.touch", Seq: 44, Op: OpTouch, ID: "usage", Data: json.RawMessage(`{"v":1}`)},
			want:    `{"topic":"radar.touch","seq":44,"op":"touch","id":"usage"}`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			encoded, err := json.Marshal(testCase.message)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(encoded) != testCase.want {
				t.Fatalf("wire form = %s, want %s", encoded, testCase.want)
			}
		})
	}
}
