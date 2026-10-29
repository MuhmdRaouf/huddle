package store

import (
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// The read that answers where a record landed: the watcher asks it right after AddRequest,
// so an id-less transcript copy can file its facts under the request id the call it paired
// with carries.

func TestStoredOf(t *testing.T) {
	t.Run("answers an id-less copy alone as itself", func(t *testing.T) {
		st := newTestStore()
		copy := transcriptCopy(func(r *shared.RequestRecord) { r.ID = "msg_sub1" })
		st.AddRequest(copy, nil)
		stored, ok := st.StoredOf(copy)
		if !ok {
			t.Fatal("StoredOf answered false for a copy the store holds")
		}
		if stored.ID != "msg_sub1" || stored.RequestID != "" {
			t.Errorf("StoredOf = id %q request id %q, want the copy itself with no request id", stored.ID, stored.RequestID)
		}
	})

	t.Run("answers the folded copy the route's request id, the route line first", func(t *testing.T) {
		st := newTestStore()
		st.AddRequest(route(func(r *shared.RequestRecord) {
			r.AgentID = "a31cd3de580148dc3"
			r.Status = str("200")
			r.StopReason = nil
			r.RequestID = "req_x"
		}), nil)
		idless := transcriptCopy(func(r *shared.RequestRecord) {
			r.ID = "msg_sub1"
			r.Ts = routeT + 1_000
		})
		st.AddRequest(idless, nil)
		stored, ok := st.StoredOf(idless)
		if !ok {
			t.Fatal("StoredOf answered false for a copy the store folded into a route")
		}
		if stored.RequestID != "req_x" {
			t.Errorf("StoredOf request id = %q, want the route's req_x", stored.RequestID)
		}
	})

	t.Run("answers the route at the copy's id, the transcript first", func(t *testing.T) {
		st := newTestStore()
		idless := transcriptCopy(func(r *shared.RequestRecord) {
			r.ID = "msg_sub1"
			r.Ts = routeT + 1_000
		})
		st.AddRequest(idless, nil)
		routed := route(func(r *shared.RequestRecord) {
			r.AgentID = "a31cd3de580148dc3"
			r.Status = str("200")
			r.StopReason = nil
			r.RequestID = "req_x"
		})
		st.AddRequest(routed, nil)
		stored, ok := st.StoredOf(routed)
		if !ok {
			t.Fatal("StoredOf answered false for a route the store holds")
		}
		if stored.ID != "msg_sub1" || stored.RequestID != "req_x" {
			t.Errorf("StoredOf = id %q request id %q, want the copy's msg_sub1 carrying the route's req_x", stored.ID, stored.RequestID)
		}
	})

	t.Run("answers false for a record the store never saw", func(t *testing.T) {
		st := newTestStore()
		st.AddRequest(route(func(r *shared.RequestRecord) { r.RequestID = "req_x" }), nil)
		if _, ok := st.StoredOf(transcriptCopy(func(r *shared.RequestRecord) { r.ID = "msg_other" })); ok {
			t.Error("StoredOf answered true for a record the store never saw")
		}
	})
}
