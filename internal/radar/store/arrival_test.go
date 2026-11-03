package store

import (
	"fmt"
	"slices"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// arrivalFixture is a list of n requests numbered 1..n.
func arrivalFixture(n int) (*arrivalList[*storedRequest], []*storedRequest) {
	list := newArrivalList[*storedRequest]()
	records := make([]*storedRequest, n)
	for i := range records {
		records[i] = &storedRequest{seq: int64(i + 1), record: shared.RequestRecord{ID: fmt.Sprintf("r%d", i+1)}}
		list.add(records[i])
	}
	return &list, records
}

func seqs(records []*storedRequest) []int64 {
	out := make([]int64, len(records))
	for i, record := range records {
		out[i] = record.seq
	}
	return out
}

// A forgotten record leaves the order a reader gets, and the others keep their places.
func TestAForgottenRecordLeavesTheArrivalOrder(t *testing.T) {
	list, records := arrivalFixture(10)
	list.forget(records[2])
	list.forget(records[7])
	if got, want := seqs(list.inOrder()), []int64{1, 2, 4, 5, 6, 7, 9, 10}; !slices.Equal(got, want) {
		t.Errorf("order after two forgets = %v, want %v", got, want)
	}
}

// Forgetting a record twice buries it once, so the list never compacts early.
func TestForgettingARecordTwiceCountsOnce(t *testing.T) {
	list, records := arrivalFixture(10)
	list.forget(records[0])
	list.forget(records[0])
	if list.buried != 1 {
		t.Errorf("buried = %d after forgetting one record twice, want 1", list.buried)
	}
}

// The buried entries stay in place until they outnumber the live ones, so a drop costs the
// records dropped; the pass that clears them runs once and leaves the live ones in order.
func TestBuriedEntriesLeaveTogetherOnceTheyOutnumberTheLiveOnes(t *testing.T) {
	list, records := arrivalFixture(10)
	for _, record := range records[:5] {
		list.forget(record)
	}
	if len(list.held) != 10 || list.buried != 5 {
		t.Fatalf("after five of ten forgets the list holds %d entries with %d buried, want 10 and 5", len(list.held), list.buried)
	}
	list.forget(records[5])
	if len(list.held) != 4 || list.buried != 0 {
		t.Errorf("after the sixth forget the list holds %d entries with %d buried, want 4 and 0", len(list.held), list.buried)
	}
	if got, want := seqs(list.inOrder()), []int64{7, 8, 9, 10}; !slices.Equal(got, want) {
		t.Errorf("order after the compaction = %v, want %v", got, want)
	}
}

// sortHeld finds records among buried entries: the entries keep their numbers.
func TestSortHeldFindsRecordsAmongBuriedEntries(t *testing.T) {
	list, records := arrivalFixture(10)
	list.forget(records[3])
	list.forget(records[4])
	picked := []*storedRequest{records[8], records[0], records[5]}
	list.sortHeld(picked)
	if got, want := seqs(picked), []int64{1, 6, 9}; !slices.Equal(got, want) {
		t.Errorf("sortHeld = %v, want %v", got, want)
	}
}
