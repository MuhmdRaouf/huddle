package store

import (
	"cmp"
	"slices"
)

// arrived is a stored record with an arrival number.
type arrived interface {
	arrivalNumber() int64
}

func (r *storedRequest) arrivalNumber() int64 { return r.seq }

func (t *storedTool) arrivalNumber() int64 { return t.seq }

func (r *storedRequest) buried() bool { return r.gone }

func (r *storedRequest) bury() { r.gone = true }

func (t *storedTool) buried() bool { return t.gone }

func (t *storedTool) bury() { t.gone = true }

// heldRecord is an arrived record the list can find again by identity and mark as no
// longer held by the store.
type heldRecord interface {
	arrived
	comparable
	buried() bool
	bury()
}

// arrivalList keeps the held records in arrival order, so a reader gets the order without
// copying the map or sorting. A live record arrives with the highest number yet and is
// appended; a reloaded one carries the position its file wrote it at and may land below
// the tail, which settle puts right once the load has placed its batch.
//
// A record the store drops is buried where it stands, so a drop costs the records dropped,
// not the records held. The buried entries leave the slice together: when they outnumber
// the live ones, and when a reader asks for the order.
type arrivalList[T heldRecord] struct {
	held   []T
	sorted bool
	buried int // the entries of held that are buried
}

func newArrivalList[T heldRecord]() arrivalList[T] {
	return arrivalList[T]{sorted: true}
}

// add appends a record.
func (l *arrivalList[T]) add(record T) {
	if n := len(l.held); n > 0 && l.held[n-1].arrivalNumber() > record.arrivalNumber() {
		l.sorted = false
	}
	l.held = append(l.held, record)
}

// settle puts the list in arrival order; a list already in order costs nothing.
func (l *arrivalList[T]) settle() {
	if l.sorted {
		return
	}
	slices.SortStableFunc(l.held, func(a, b T) int {
		switch {
		case a.arrivalNumber() < b.arrivalNumber():
			return -1
		case a.arrivalNumber() > b.arrivalNumber():
			return 1
		}
		return 0
	})
	l.sorted = true
}

// inOrder is the held records in arrival order. The slice is the list's own: valid while
// the store's lock is held and no record is added or dropped.
func (l *arrivalList[T]) inOrder() []T {
	l.compact()
	return l.held
}

// forget buries a record the store no longer holds; burying one twice counts once.
func (l *arrivalList[T]) forget(record T) {
	if record.buried() {
		return
	}
	record.bury()
	l.buried++
	if l.buried*2 > len(l.held) {
		l.compact()
	}
}

// compact drops the buried entries in place, one pass over the list; a list with none
// costs nothing.
func (l *arrivalList[T]) compact() {
	if l.buried == 0 {
		return
	}
	l.held = slices.DeleteFunc(l.held, func(record T) bool { return record.buried() })
	l.buried = 0
}

// sortHeld puts records, all held in the list, in the order the list keeps them: a lookup of
// each by its arrival number, so the cost follows the records given, not the list.
func (l *arrivalList[T]) sortHeld(records []T) {
	type placed struct {
		record T
		at     int
	}
	found := make([]placed, len(records))
	for i, record := range records {
		at, _ := slices.BinarySearchFunc(l.held, record.arrivalNumber(), func(held T, number int64) int {
			return cmp.Compare(held.arrivalNumber(), number)
		})
		for at < len(l.held) && l.held[at] != record {
			at++
		}
		found[i] = placed{record: record, at: at}
	}
	slices.SortFunc(found, func(a, b placed) int { return cmp.Compare(a.at, b.at) })
	for i := range found {
		records[i] = found[i].record
	}
}
