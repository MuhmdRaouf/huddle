// The blocks route: the 5-hour metering windows over 7 or 30 UTC days. Blocks keep UTC
// whatever the hub's zone, the calendar the subscription's own limits count in.
package server

import (
	"context"
	"net/url"
	"time"

	dayclock "github.com/MuhmdRaouf/huddle/internal/radar/clock"
	"github.com/MuhmdRaouf/huddle/internal/radar/history"
	"github.com/MuhmdRaouf/huddle/internal/radar/usage"
)

// blocksRoute answers the metering blocks: the window runs from the UTC midnight `days-1`
// days back to now, Claude-model requests alone unless the query asks for the routed
// provider models too. A read error is the caller's to answer for.
func blocksRoute(h *history.History, query url.Values, now func() time.Time) (ApiResponse, error) {
	days := int64(7)
	if query.Has("days") {
		asked, ok := parseInt10(query.Get("days"))
		if !ok || (asked != 7 && asked != 30) {
			return errorOf(400, "days: one of 7, 30"), nil
		}
		days = asked
	}
	models := usage.ClaudeModels
	if query.Get("providers") == "1" {
		models = usage.AllModels
	}
	at := now()
	from := dayclock.Midnight(at).AddDate(0, 0, -int(days-1)).UnixMilli()
	rows, err := h.UsageRows(context.Background(), from, at.UnixMilli())
	if err != nil {
		return ApiResponse{}, err
	}
	return jsonOf(200, usage.Blocks(rows, at, models, usagePricer{})), nil
}
