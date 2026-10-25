// The today route: the current UTC day's requests per hour and its costliest sessions, the
// Overview's two graphs of today.
package server

import (
	"context"
	"time"

	dayclock "github.com/MuhmdRaouf/huddle/internal/radar/clock"
	"github.com/MuhmdRaouf/huddle/internal/radar/history"
	"github.com/MuhmdRaouf/huddle/internal/radar/usage"
)

// todayRoute answers the day from the UTC midnight to now, priced the way the usage report
// is. A read error is the caller's to answer for.
func todayRoute(h *history.History, now func() time.Time) (ApiResponse, error) {
	at := now()
	rows, err := h.UsageRows(context.Background(), dayclock.Midnight(at).UnixMilli(), at.UnixMilli())
	if err != nil {
		return ApiResponse{}, err
	}
	return jsonOf(200, usage.Today(rows, at, usagePricer{})), nil
}
