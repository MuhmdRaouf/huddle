package stream

import (
	"fmt"
	"sync/atomic"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// CloseReason says why the broker ended a subscription.
type CloseReason string

const (
	// ReasonSlow means the subscriber let its queue fill; it reconnects with its last seq.
	ReasonSlow CloseReason = "slow"
	// ReasonCanceled means the context passed to Subscribe ended.
	ReasonCanceled CloseReason = "canceled"
	// ReasonClosed means the subscriber called Close.
	ReasonClosed CloseReason = "closed"
)

// ClosedError is the reason a subscription ended.
type ClosedError struct{ Reason CloseReason }

func (e *ClosedError) Error() string { return fmt.Sprintf("stream subscription closed: %s", e.Reason) }

// Subscription is one client's view of the stream: the frames it must send first, then a queue of live
// frames. The broker closes the queue when the subscription ends, and Err names why.
type Subscription struct {
	broker  *Broker
	topics  map[string]struct{}
	initial []domain.StreamFrame
	gone    []string
	head    uint64
	queue   chan domain.StreamFrame
	err     atomic.Pointer[ClosedError]
	stop    func() bool // detaches the context watcher
}

// Initial returns the snapshots and replayed frames to send before any live frame.
func (s *Subscription) Initial() []domain.StreamFrame { return s.initial }

// Frames is the queue of live frames, in seq order. It closes when the subscription ends.
func (s *Subscription) Frames() <-chan domain.StreamFrame { return s.queue }

// Gone lists the requested topics that were answered with a snapshot, or with nothing when no
// snapshotter serves them, instead of a replay; the client replaces its state for them.
func (s *Subscription) Gone() []string { return s.gone }

// Head is the newest seq the hub had issued when the subscription began.
func (s *Subscription) Head() uint64 { return s.head }

// Err is nil while the subscription is open and a *ClosedError once the queue has closed.
func (s *Subscription) Err() error {
	if closed := s.err.Load(); closed != nil {
		return closed
	}
	return nil
}

// Close ends the subscription and frees its queue. Closing twice is harmless.
func (s *Subscription) Close() { s.broker.drop(s, ReasonClosed) }

// wants reports whether the subscription asked for the topic.
func (s *Subscription) wants(topic string) bool {
	_, ok := s.topics[topic]
	return ok
}
