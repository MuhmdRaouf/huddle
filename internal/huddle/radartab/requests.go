package radartab

// The requests the lanes served, read through the tab's own API for the insights route.

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"time"
)

// Request is one model request a lane served, as the tab's own history holds it.
type Request struct {
	Provider string
	Status   int // the answer's HTTP status
	At       time.Time
}

// RequestsSince reads the requests the lanes served since the given moment through the tab's
// own API, in process, oldest first. The limit is radar's own largest, so the whole window
// answers; a record with no provider, or with no status a router wrote, says nothing about a
// lane and stays out, and an unreadable answer answers none.
func (t *Tab) RequestsSince(ctx context.Context, since time.Time) []Request {
	body, ok := t.RadarAnswer(ctx, "/api/requests?"+url.Values{
		"since": {strconv.FormatInt(since.UnixMilli(), 10)},
		"limit": {"1000"},
	}.Encode())
	if !ok {
		return nil
	}
	var answer struct {
		Requests []struct {
			Provider string  `json:"provider"`
			Ts       int64   `json:"ts"`
			Status   *string `json:"status"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil
	}
	requests := make([]Request, 0, len(answer.Requests))
	for _, record := range answer.Requests {
		if record.Provider == "" || record.Status == nil {
			continue
		}
		status, err := strconv.Atoi(*record.Status)
		if err != nil {
			continue
		}
		requests = append(requests, Request{Provider: record.Provider, Status: status, At: time.UnixMilli(record.Ts)})
	}
	slices.SortStableFunc(requests, func(a, b Request) int { return a.At.Compare(b.At) })
	return requests
}
