package domain

import (
	"encoding/json"
	"slices"
)

// The hub stream's wire message and resume ring (ADR 6): one global sequence, the last 2,048 frames
// or 4 MiB of encoded bytes, and a replay-or-gone answer for a reconnecting client.

// StreamOp is one stream frame's kind.
type StreamOp string

const (
	// OpSnapshot carries a topic's whole state and replaces whatever the client holds for it.
	OpSnapshot StreamOp = "snapshot"
	// OpUpsert carries one row, named by the message's ID.
	OpUpsert StreamOp = "upsert"
	// OpRemove drops the row the message's ID names.
	OpRemove StreamOp = "remove"
	// OpTouch says a topic changed in a way rows cannot carry; the client refetches its own view.
	OpTouch StreamOp = "touch"
)

// StreamMessage is one frame on the stream wire.
type StreamMessage struct {
	Topic string          `json:"topic"`
	Seq   uint64          `json:"seq"`
	Op    StreamOp        `json:"op"`
	ID    string          `json:"id"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// MarshalJSON writes the five fields in declaration order and leaves data off remove and touch
// frames: those name a row or a view and carry no state.
func (m StreamMessage) MarshalJSON() ([]byte, error) {
	if m.Op == OpRemove || m.Op == OpTouch {
		m.Data = nil
	}
	type wire StreamMessage
	return json.Marshal(wire(m))
}

// StreamFrame is a message with its encoded wire form. The broker encodes a frame once and the same
// bytes go to the live subscribers and into the ring.
type StreamFrame struct {
	Message StreamMessage
	Encoded []byte
}

const (
	maxRingFrames = 2048
	maxRingBytes  = 4 << 20 // 4 MiB of encoded frame bytes
)

// Ring holds the stream's last frames so a reconnect with an `after` seq can replay what it missed.
// Frames arrive in the order the broker assigns seqs. A Ring is not safe for concurrent use; the
// broker serializes access.
type Ring struct {
	slots []StreamFrame // circular buffer of maxRingFrames entries, allocated on the first Append
	head  int           // index of the oldest frame
	count int
	bytes int // encoded bytes held
}

// Append adds one frame and evicts the oldest frames until the ring fits its caps again. A frame
// larger than the byte cap stays: the ring never evicts the frame it just wrote.
func (r *Ring) Append(frame StreamFrame) {
	if r.slots == nil {
		r.slots = make([]StreamFrame, maxRingFrames)
	}
	if r.count == maxRingFrames {
		r.evict()
	}
	r.slots[(r.head+r.count)%maxRingFrames] = frame
	r.count++
	r.bytes += len(frame.Encoded)
	for r.bytes > maxRingBytes && r.count > 1 {
		r.evict()
	}
}

// evict drops the oldest frame; every caller keeps at least one frame in the ring.
func (r *Ring) evict() {
	r.bytes -= len(r.slots[r.head].Encoded)
	r.slots[r.head] = StreamFrame{}
	r.head = (r.head + 1) % maxRingFrames
	r.count--
}

// After returns the frames after seq for the requested topics, oldest first. It reports false when
// the ring cannot serve the resume point — seq 0 (no resume), below the oldest held frame, or above
// every seq this process issued — and the caller answers with snapshots instead.
func (r *Ring) After(seq uint64, topics []string) ([]StreamFrame, bool) {
	if seq == 0 || r.count == 0 || seq < r.Tail() || seq > r.Head() {
		return nil, false
	}
	var frames []StreamFrame
	for i := 0; i < r.count; i++ {
		frame := r.slots[(r.head+i)%maxRingFrames]
		if frame.Message.Seq <= seq || !slices.Contains(topics, frame.Message.Topic) {
			continue
		}
		frames = append(frames, frame)
	}
	return frames, true
}

// Head is the newest seq in the ring, 0 when the ring is empty.
func (r *Ring) Head() uint64 {
	if r.count == 0 {
		return 0
	}
	return r.slots[(r.head+r.count-1)%maxRingFrames].Message.Seq
}

// Tail is the oldest seq in the ring, 0 when the ring is empty.
func (r *Ring) Tail() uint64 {
	if r.count == 0 {
		return 0
	}
	return r.slots[r.head].Message.Seq
}
