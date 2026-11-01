package store

import "slices"

// arrived is a stored record with an arrival number.
type arrived interface {
	arrivalNumber() int64
}

func (r *storedRequest) arrivalNumber() int64 { return r.seq }

func (t *storedTool) arrivalNumber() int64 { return t.seq }

// arrivalList keeps the held records in arrival order, so a reader gets the order without
// copying the map or sorting. A live record arrives with the highest number yet and is
// appended; a reloaded one carries the position its file wrote it at and may land below
// the tail, which settle puts right once the load has placed its batch.
type arrivalList[T arrived] struct {
	held   []T
	sorted bool
}

func newArrivalList[T arrived]() arrivalList[T] {
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
	return l.held
}

// keepOnly drops every record keep rejects, compacting the list in place: one pass over
// the list, however many records go.
func (l *arrivalList[T]) keepOnly(keep func(T) bool) {
	l.held = slices.DeleteFunc(l.held, func(record T) bool { return !keep(record) })
}
