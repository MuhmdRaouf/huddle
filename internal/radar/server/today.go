// The today route: the current day's requests per clock hour in the hub's zone and its costliest sessions, the
// Overview's two graphs of today.
package server

import (
	"context"
	"time"

	dayclock "github.com/MuhmdRaouf/huddle/internal/radar/clock"
	"github.com/MuhmdRaouf/huddle/internal/radar/history"
	"github.com/MuhmdRaouf/huddle/internal/radar/usage"
)

// todayRoute answers the day from zone's midnight (nil is UTC) to now, priced the way the
// usage report is. A read error is the caller's to answer for.
func todayRoute(h *history.History, now func() time.Time, zone *time.Location) (ApiResponse, error) {
	at := now()
	rows, err := h.UsageRows(context.Background(), dayclock.DayOf(at, zone).UnixMilli(), at.UnixMilli())
	if err != nil {
		return ApiResponse{}, err
	}
	return jsonOf(200, usage.Today(rows, at, zone, usagePricer{})), nil
}
