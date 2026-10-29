package store

import "github.com/MuhmdRaouf/huddle/internal/radar/shared"

// StoredOf answers the record the store holds for the call this record landed in: the record
// itself, the twin it was folded into (through the alias a folded copy leaves), or the call its
// request id names; false when the store holds none.
func (s *Store) StoredOf(record shared.RequestRecord) (shared.RequestRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id := record.ID
	if held, ok := s.aliases.get(record.ID); ok {
		id = held
	}
	if held, ok := s.requests[id]; ok {
		return held.record, true
	}
	// a read-only look at the request-id slot: slotLocked deletes a dead slot, which needs
	// the write lock
	if record.RequestID != "" {
		if slot, ok := s.calls[record.RequestID]; ok {
			if held, heldOk := s.requests[slot.id]; heldOk {
				return held.record, true
			}
		}
	}
	return shared.RequestRecord{}, false
}
