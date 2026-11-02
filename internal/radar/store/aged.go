package store

import (
	"slices"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// waitingKind says which pairing queue a call stood in.
type waitingKind int

const (
	waitingNone waitingKind = iota
	waitingRoute
	waitingCopy
)

// agedCall is a call that arrived past the record window and was shed with its write, with
// the pairing state it had. A later sighting of the call (its twin, or the same source again)
// brings it back for the write, so the call reaches history once whatever its age.
type agedCall struct {
	record shared.RequestRecord
	// waiting is the queue the call stood in, and agent the agent a waiting route line named.
	waiting waitingKind
	agent   string
	// routed and copied are the sources the call's request-id slot held.
	routed, copied bool
}

// agedCalls is the set of those calls, found by id, request id and match-window slot. A
// watcher releases it when a full pass ends, so it holds what that pass reads.
type agedCalls struct {
	held      map[string]agedCall
	byRequest map[string]string
	bySlot    map[uint64][]string
}

func newAgedCalls() *agedCalls {
	return &agedCalls{
		held:      make(map[string]agedCall),
		byRequest: make(map[string]string),
		bySlot:    make(map[uint64][]string),
	}
}

// add sets a call aside.
func (a *agedCalls) add(call agedCall) {
	record := call.record
	if _, ok := a.held[record.ID]; !ok {
		slot := warmedCallKey(record.SessionID, record.Model, record.Ts)
		a.bySlot[slot] = append(a.bySlot[slot], record.ID)
	}
	a.held[record.ID] = call
	if record.RequestID != "" {
		a.byRequest[record.RequestID] = record.ID
	}
}

// take removes a call from the set and returns it.
func (a *agedCalls) take(id string) (agedCall, bool) {
	call, ok := a.held[id]
	if !ok {
		return agedCall{}, false
	}
	record := call.record
	delete(a.held, id)
	if a.byRequest[record.RequestID] == id {
		delete(a.byRequest, record.RequestID)
	}
	slot := warmedCallKey(record.SessionID, record.Model, record.Ts)
	if ids := slices.DeleteFunc(a.bySlot[slot], func(other string) bool { return other == id }); len(ids) > 0 {
		a.bySlot[slot] = ids
	} else {
		delete(a.bySlot, slot)
	}
	return call, true
}

// sightingsOf lists the set-aside calls a record can be another sighting of: the call its own
// id or alias names, the call sharing its request id, and every call of the same session and
// model within the match window of its time.
func (a *agedCalls) sightingsOf(record shared.RequestRecord, aliasOf string) []string {
	var ids []string
	note := func(id string) {
		if _, ok := a.held[id]; ok && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	note(record.ID)
	note(aliasOf)
	if record.RequestID != "" {
		note(a.byRequest[record.RequestID])
	}
	for _, at := range [3]int64{record.Ts - MatchMs, record.Ts, record.Ts + MatchMs} {
		for _, id := range a.bySlot[warmedCallKey(record.SessionID, record.Model, at)] {
			if other := a.held[id].record; other.Ts >= record.Ts-MatchMs && other.Ts <= record.Ts+MatchMs {
				note(id)
			}
		}
	}
	return ids
}

// holdAgedLocked sets aside the requests an age shed is about to drop, with the pairing state
// each has.
func (s *Store) holdAgedLocked(aged []evictionCandidate) {
	for _, pick := range aged {
		if pick.source != 0 {
			continue
		}
		held, ok := s.requests[pick.id]
		if !ok {
			continue
		}
		call := agedCall{record: held.record}
		key := pairKey(held.record)
		if routes := s.lonelyRoutes[key]; routes != nil {
			if agent, waiting := routes.Get(pick.id); waiting {
				call.waiting, call.agent = waitingRoute, agent
			}
		}
		if copies := s.lonelyCopies[key]; copies != nil && copies.Has(pick.id) {
			call.waiting = waitingCopy
		}
		if slot, ok := s.calls[held.record.RequestID]; ok && slot.id == pick.id {
			call.routed, call.copied = slot.routed, slot.copied
		}
		s.agedCalls.add(call)
	}
}

// restoreAgedLocked brings back, as held requests, the set-aside calls a record can be another
// sighting of, so the write pairs and folds it as it does a call inside the window. It lists
// the restored requests as pins: the age shed after the write sets them aside again.
func (s *Store) restoreAgedLocked(record shared.RequestRecord) []evictionPin {
	if len(s.agedCalls.held) == 0 {
		return nil
	}
	aliasOf, _ := s.aliases.get(record.ID)
	var pins []evictionPin
	for _, id := range s.agedCalls.sightingsOf(record, aliasOf) {
		call, _ := s.agedCalls.take(id)
		s.insertRequestLocked(call.record)
		key := pairKey(call.record)
		switch call.waiting {
		case waitingRoute:
			s.waitingRoutesLocked(key).Set(id, call.agent)
		case waitingCopy:
			s.waitingCopiesLocked(key).Add(id)
		}
		if call.record.RequestID != "" {
			s.noteCallLocked(call.record, call.routed, call.copied)
		}
		pins = append(pins, evictionPin{source: 0, id: id})
	}
	return pins
}

// ReleaseAgedCalls lets go of the calls set aside for a twin: a watcher calls it when a full pass
// has ended, which read every source, so a store at rest holds none.
func (s *Store) ReleaseAgedCalls() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agedCalls = newAgedCalls()
}
