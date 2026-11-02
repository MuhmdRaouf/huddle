package store

import (
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// A paired record's tail keys take the room of the keys they hold and no more: the store
// holds one such list per call, and a list with room for eight more keys held a third of the
// catch-up's request records' size again.
func TestAPairedRecordsTailKeysHoldNoSpareRoom(t *testing.T) {
	transcript := makeRequest(func(r *shared.RequestRecord) { r.TailKeys = []string{} })
	route := makeRequest(func(r *shared.RequestRecord) {
		r.Status = str("200")
		r.Route = str("direct")
		r.Via = str("zai")
		r.Headers = &shared.Headers{{Name: "request-id", Value: "req-1"}}
	})
	merged := Combine(transcript, route, "req-1")
	want := []string{"status", "route", "via", "headers"}
	if len(merged.TailKeys) != len(want) {
		t.Fatalf("tail keys = %v, want %v", merged.TailKeys, want)
	}
	if got := cap(merged.TailKeys); got != len(want) {
		t.Errorf("%d tail keys sit in room for %d", len(want), got)
	}
}
