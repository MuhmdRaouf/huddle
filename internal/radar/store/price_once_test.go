package store

import (
	"fmt"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/radar/cost"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// pricedFixture is a store whose pricer counts its calls: sessions of requests over models the
// catalog lists, models only its family prices, and a model nothing prices.
func pricedFixture(t *testing.T, requests int) (*Store, *int) {
	t.Helper()
	s := newTestStore()
	calls := 0
	s.price = func(record shared.RequestRecord) cost.RequestPrice {
		calls++
		return pricedNow(record)
	}
	addPricedRequests(s, 0, requests)
	return s, &calls
}

// addPricedRequests adds requests from..to-1, five sessions deep, rotating through the models.
func addPricedRequests(s *Store, from, to int) {
	models := []string{"claude-opus-5-5", "glm-5.3", "claude-opus-9-9", "mystery-model"}
	for i := from; i < to; i++ {
		s.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID = fmt.Sprintf("req-%04d", i)
			r.SessionID = fmt.Sprintf("s%d", i%5)
			r.Model = models[i%len(models)]
			r.Upstream = "https://api.z.ai/api/anthropic"
			r.Ts = ts0 + int64(i)
			r.StopReason = str("end_turn")
			r.Tokens = shared.Tokens{Input: int64(100 + i), Output: int64(10 + i%7)}
		}), nil)
	}
}

// readEveryView builds each view that reads a request's price.
func readEveryView(s *Store) {
	s.SessionList()
	s.Summary()
	s.Models(nil)
	s.Models(&ModelsScope{Sessions: []string{"s1", "s2"}})
	for i := 0; i < 5; i++ {
		s.SessionDetail(fmt.Sprintf("s%d", i))
	}
}

func TestARequestIsPricedOnceNotOncePerView(t *testing.T) {
	const requests = 60
	s, calls := pricedFixture(t, requests)
	if *calls != requests {
		t.Fatalf("%d requests added, the pricer ran %d times, want once each", requests, *calls)
	}

	*calls = 0
	readEveryView(s)
	if *calls != 0 {
		t.Errorf("building every view priced %d requests, want 0: the price is held from when the request landed", *calls)
	}

	*calls = 0
	addPricedRequests(s, requests, requests+3)
	readEveryView(s)
	if *calls != 3 {
		t.Errorf("3 requests added then every view built: the pricer ran %d times, want 3", *calls)
	}
}

// A request a later source refiles is priced again, once, and the views read the new price.
func TestARefiledRequestIsPricedAgain(t *testing.T) {
	s, calls := pricedFixture(t, 4)
	before := s.Summary().CostUsd

	*calls = 0
	s.AddRequest(makeRequest(func(r *shared.RequestRecord) {
		r.ID = "req-0000"
		r.SessionID = "s0"
		r.Model = "claude-opus-5-5"
		r.Ts = ts0
		r.StopReason = str("end_turn")
		r.Tokens = shared.Tokens{Input: 5_000_000, Output: 10}
	}), nil)
	after := s.Summary().CostUsd

	if *calls != 1 {
		t.Errorf("one request refiled: the pricer ran %d times, want 1", *calls)
	}
	if before == nil || after == nil || *after <= *before {
		t.Errorf("summary cost before %v and after the larger request %v, want it to grow", before, after)
	}
}

// The held price is what RequestCost answers for the record.
func TestAHeldPriceEqualsWhatRequestCostAnswers(t *testing.T) {
	cases := map[string]func(*shared.RequestRecord){
		"catalog model":     func(r *shared.RequestRecord) { r.Model = "claude-opus-5-5"; r.Upstream = "https://api.anthropic.com" },
		"router upstream":   func(r *shared.RequestRecord) { r.Model = "glm-5.3"; r.Upstream = "https://api.z.ai/api/anthropic" },
		"over the tier":     func(r *shared.RequestRecord) { r.Model = "claude-haiku-5-5"; r.Tokens.Input = 150_000 },
		"fast mode, US geo": func(r *shared.RequestRecord) { r.Model = "claude-opus-5-5"; r.Speed = str("fast"); r.Geo = str("us") },
		"one hour writes": func(r *shared.RequestRecord) {
			r.Model = "claude-opus-5-5"
			r.Tokens.CacheWrite = 2_000
			r.CacheWrite1h = i64(1_500)
		},
		"family path":   func(r *shared.RequestRecord) { r.Model = "claude-opus-9-9" },
		"unknown model": func(r *shared.RequestRecord) { r.Model = "mystery-model" },
		"still streaming": func(r *shared.RequestRecord) {
			r.StopReason = nil
			r.Tokens.Output = 0
		},
	}
	for name, shape := range cases {
		t.Run(name, func(t *testing.T) {
			s := newTestStore()
			record := makeRequest(func(r *shared.RequestRecord) {
				r.ID, r.SessionID, r.Ts = "req-1", "s1", ts0
				r.StopReason = str("end_turn")
				r.Tokens = shared.Tokens{Input: 1_000, Output: 500}
				shape(r)
			})
			s.AddRequest(record, nil)

			s.mu.Lock()
			held := s.requests["req-1"].price
			s.mu.Unlock()
			want := cost.RequestCost(pricedOf(&record))
			if (want == nil) != !held.Priced {
				t.Fatalf("RequestCost priced it: %v, the held price says priced: %v", want != nil, held.Priced)
			}
			if want != nil && held.USD != want.USD {
				t.Errorf("held price %v, RequestCost %v", held.USD, want.USD)
			}
			if held.Family != cost.CostFamilyOf(record.Model) {
				t.Errorf("held family %q, CostFamilyOf %q", held.Family, cost.CostFamilyOf(record.Model))
			}
		})
	}
}
