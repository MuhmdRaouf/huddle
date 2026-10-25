// The usage route: the long-range usage report over 7, 30 or 90 UTC days.
package server

import (
	"context"
	"net/url"
	"time"

	dayclock "github.com/MuhmdRaouf/huddle/internal/radar/clock"
	"github.com/MuhmdRaouf/huddle/internal/radar/cost"
	"github.com/MuhmdRaouf/huddle/internal/radar/history"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/usage"
)

// usageRoute answers one usage report: the window runs from the UTC midnight `days-1` days
// back to now, and the rows price the way the Costs view does. A read error is the caller's
// to answer for.
func usageRoute(h *history.History, query url.Values, now func() time.Time) (ApiResponse, error) {
	days := int64(30)
	if query.Has("days") {
		asked, ok := parseInt10(query.Get("days"))
		if !ok || (asked != 7 && asked != 30 && asked != 90) {
			return errorOf(400, "days: one of 7, 30, 90"), nil
		}
		days = asked
	}
	at := now()
	from := dayclock.Midnight(at).AddDate(0, 0, -int(days-1)).UnixMilli()
	to := at.UnixMilli()
	rows, err := h.UsageRows(context.Background(), from, to)
	if err != nil {
		return ApiResponse{}, err
	}
	report := usage.Aggregate(rows, usage.Window{From: from, To: to}, usagePricer{})
	return jsonOf(200, report), nil
}

// usagePricer prices usage rows through the cost package — the same path the Costs view
// reads, so the two never disagree to the cent — and prices the saving a cache read made
// against the input rate.
type usagePricer struct{}

func (usagePricer) PriceOf(row usage.Row) (usage.RowPrice, bool) {
	price := cost.RequestCost(cost.PricedRequest{
		Model:    row.Model,
		Upstream: row.Upstream,
		Ts:       row.TS,
		Tokens: shared.Tokens{
			Input:      row.Input,
			Output:     row.Output,
			CacheRead:  row.CacheRead,
			CacheWrite: row.CacheWrite,
		},
		CacheWrite1h: &row.CacheWrite1h,
		Speed:        row.Speed,
		Geo:          row.Geo,
		ServiceTier:  row.ServiceTier,
	})
	if price == nil {
		return usage.RowPrice{}, false
	}
	out := usage.RowPrice{CostUSD: float64(price.USD)}
	if rates, ok := cost.RatesOf(row.Model, row.Upstream); ok {
		out.SavingsUSD = float64(row.CacheRead) / 1e6 * (rates.Input - rates.CacheRead)
	}
	return out, true
}
