// Package stream is the hub stream's broker: it numbers each published frame, encodes it once, fans it
// out to the subscribers of its topic, keeps the last frames for a resume and answers a snapshot when a
// reconnecting client's seq has left the ring.
package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// queueCap is how many live frames a subscriber may hold unread. A subscriber that falls further behind
// is closed with ReasonSlow rather than skipped, so it reconnects and resumes from its last seq.
const queueCap = 256

// Request is what a client asks for: its topics, the last seq it saw (0 for none) and the topics that
// must be answered with a snapshot whatever After says.
type Request struct {
	Topics   []string
	After    uint64
	Snapshot []string
}

// NoTopicsError is a Subscribe call that named no topic.
type NoTopicsError struct{}

func (*NoTopicsError) Error() string { return "stream subscribe names no topic" }

// EncodeError is a frame that could not be encoded, such as data that is not valid JSON.
type EncodeError struct {
	Topic string
	Err   error
}

func (e *EncodeError) Error() string { return fmt.Sprintf("encode a %s frame: %v", e.Topic, e.Err) }

func (e *EncodeError) Unwrap() error { return e.Err }

// Broker numbers, encodes, stores and fans out the hub's frames. All of its state sits behind one
// mutex, and a Broker must be made with New.
type Broker struct {
	now func() time.Time

	mu       sync.Mutex
	last     uint64 // the newest seq issued
	ring     domain.Ring
	subs     map[*Subscription]struct{}
	snappers map[string]Snapshotter
}

// New returns a broker whose first seq is the current time in microseconds, so a restarted hub issues
// seqs above every one its predecessor did.
func New(now func() time.Time) *Broker {
	return &Broker{
		now:      now,
		last:     uint64(now().UnixMicro()) - 1,
		subs:     map[*Subscription]struct{}{},
		snappers: map[string]Snapshotter{},
	}
}

// Register serves the snapshots of every topic that starts with prefix; a longer prefix wins.
func (b *Broker) Register(prefix string, snapshotter Snapshotter) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.snappers[prefix] = snapshotter
}

// Head is the newest seq issued.
func (b *Broker) Head() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.last
}

// Publish stamps the next seq on a frame, encodes it once, stores it in the ring and queues it for each
// subscriber of the topic. It returns the seq. A frame that cannot be encoded takes no seq.
func (b *Broker) Publish(topic string, op domain.StreamOp, id string, data json.RawMessage) (uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	seq := b.last + 1
	frame, err := encode(domain.StreamMessage{Topic: topic, Seq: seq, Op: op, ID: id, Data: data})
	if err != nil {
		return 0, err
	}
	b.last = seq
	b.ring.Append(frame)
	for sub := range b.subs {
		if !sub.wants(topic) {
			continue
		}
		select {
		case sub.queue <- frame:
		default:
			b.dropLocked(sub, ReasonSlow)
		}
	}
	return seq, nil
}

// Subscribe starts a subscription. Topics the ring can replay get the frames after req.After; the others,
// and those named in req.Snapshot, get a snapshot stamped with the head seq. The replay, the snapshots
// and the live queue join without a gap or a repeat. The subscription ends when ctx ends.
func (b *Broker) Subscribe(ctx context.Context, req Request) (*Subscription, error) {
	topics := distinct(req.Topics)
	if len(topics) == 0 {
		return nil, &NoTopicsError{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	replay, canReplay := b.ring.After(req.After, topics)
	var snapshotTopics []string
	for _, topic := range topics {
		if !canReplay || slices.Contains(req.Snapshot, topic) {
			snapshotTopics = append(snapshotTopics, topic)
		}
	}
	sub := &Subscription{
		broker: b,
		topics: make(map[string]struct{}, len(topics)),
		gone:   snapshotTopics,
		head:   b.last,
		queue:  make(chan domain.StreamFrame, queueCap),
	}
	for _, snapshotTopic := range snapshotTopics {
		frame, served, err := snapshotFrame(ctx, b.snappers, snapshotTopic, b.last)
		if err != nil {
			return nil, err
		}
		if served {
			sub.initial = append(sub.initial, frame)
		}
	}
	for _, frame := range replay {
		if !slices.Contains(snapshotTopics, frame.Message.Topic) {
			sub.initial = append(sub.initial, frame)
		}
	}
	for _, topic := range topics {
		sub.topics[topic] = struct{}{}
	}
	b.subs[sub] = struct{}{}
	sub.stop = context.AfterFunc(ctx, func() { b.drop(sub, ReasonCanceled) })
	return sub, nil
}

// Subscribers counts the subscriptions holding at least one topic that starts with prefix; "" counts
// them all. Producers that only matter while someone listens check it before they work.
func (b *Broker) Subscribers(prefix string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	count := 0
	for sub := range b.subs {
		for topic := range sub.topics {
			if strings.HasPrefix(topic, prefix) {
				count++
				break
			}
		}
	}
	return count
}

// drop ends a subscription and frees its queue.
func (b *Broker) drop(sub *Subscription, reason CloseReason) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dropLocked(sub, reason)
}

// dropLocked is drop with the broker's lock held. Only a subscription still registered is closed, so the
// queue closes once.
func (b *Broker) dropLocked(sub *Subscription, reason CloseReason) {
	if _, registered := b.subs[sub]; !registered {
		return
	}
	delete(b.subs, sub)
	sub.err.Store(&ClosedError{Reason: reason})
	close(sub.queue)
	sub.stop()
}

// encode renders a message once into the frame every consumer shares.
func encode(message domain.StreamMessage) (domain.StreamFrame, error) {
	encoded, err := json.Marshal(message)
	if err != nil {
		return domain.StreamFrame{}, &EncodeError{Topic: message.Topic, Err: err}
	}
	return domain.StreamFrame{Message: message, Encoded: encoded}, nil
}

// distinct returns topics without repeats, in the order first named.
func distinct(topics []string) []string {
	var unique []string
	for _, topic := range topics {
		if !slices.Contains(unique, topic) {
			unique = append(unique, topic)
		}
	}
	return unique
}
