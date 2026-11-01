package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// Row is one row of a topic's snapshot: the row's key and its JSON.
type Row struct {
	ID   string          `json:"id"`
	Data json.RawMessage `json:"data"`
}

// Snapshotter reads a topic's whole current state. The broker calls it while it holds its lock, so a
// snapshot is consistent with the head seq it carries; an implementation reads its own store and never
// calls back into the broker.
type Snapshotter interface {
	Snapshot(ctx context.Context, topic string) ([]Row, error)
}

// SnapshotError is a snapshotter's failure on one topic.
type SnapshotError struct {
	Topic string
	Err   error
}

func (e *SnapshotError) Error() string { return fmt.Sprintf("snapshot of %s: %v", e.Topic, e.Err) }

func (e *SnapshotError) Unwrap() error { return e.Err }

// snapshotData is the data of a snapshot frame.
type snapshotData struct {
	Rows []Row `json:"rows"`
}

// snapshotterFor returns the snapshotter registered under the longest prefix of topic, or nil.
func snapshotterFor(registered map[string]Snapshotter, topic string) Snapshotter {
	var best Snapshotter
	bestLen := -1
	for prefix, snapshotter := range registered {
		if strings.HasPrefix(topic, prefix) && len(prefix) > bestLen {
			best, bestLen = snapshotter, len(prefix)
		}
	}
	return best
}

// snapshotFrame reads topic's state and encodes it as a snapshot frame stamped with seq. It returns false
// when no snapshotter serves the topic.
func snapshotFrame(ctx context.Context, registered map[string]Snapshotter, topic string, seq uint64) (domain.StreamFrame, bool, error) {
	snapshotter := snapshotterFor(registered, topic)
	if snapshotter == nil {
		return domain.StreamFrame{}, false, nil
	}
	rows, err := snapshotter.Snapshot(ctx, topic)
	if err != nil {
		return domain.StreamFrame{}, false, &SnapshotError{Topic: topic, Err: err}
	}
	if rows == nil {
		rows = []Row{}
	}
	data, err := json.Marshal(snapshotData{Rows: rows})
	if err != nil {
		return domain.StreamFrame{}, false, &SnapshotError{Topic: topic, Err: err}
	}
	frame, err := encode(domain.StreamMessage{Topic: topic, Seq: seq, Op: domain.OpSnapshot, Data: data})
	if err != nil {
		return domain.StreamFrame{}, false, &SnapshotError{Topic: topic, Err: err}
	}
	return frame, true, nil
}
