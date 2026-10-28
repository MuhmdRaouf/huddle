package store

import (
	"fmt"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// BenchmarkSessionChurn measures the price of one session's arrival at the full store:
// 200 sessions x 1,000 requests (the record cap), then each iteration upserts one new
// session, which drops the stalest one.
func BenchmarkSessionChurn(b *testing.B) {
	store := newTestStore()
	for i := 0; i < shared.MaxSessions; i++ {
		for j := 0; j < 1_000; j++ {
			store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
				r.ID = fmt.Sprintf("req-%03d-%04d", i, j)
				r.SessionID = fmt.Sprintf("s%03d", i)
				r.Ts = ts0 + int64(i)*1_000 + int64(j)
			}), nil)
		}
	}
	if got := len(store.requests); got != shared.MaxSessions*1_000 {
		b.Fatalf("warm-up stored %d requests, want %d", got, shared.MaxSessions*1_000)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.UpsertSession(SessionUpsert{ID: fmt.Sprintf("new-%d", i)})
	}
}
