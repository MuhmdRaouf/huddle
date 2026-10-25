package usage

import (
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/clock"
)

// todaySessions caps the Overview's list of today's costliest sessions.
const todaySessions = 10

// TodayReport is the current UTC day as the Overview graphs it: the requests in each of its
// 24 hours, its costliest sessions, and the honest flags about prices.
type TodayReport struct {
	Date           string    `json:"date"`
	HourlyRequests []int64   `json:"hourlyRequests"`
	Sessions       []Session `json:"sessions"`
	Approximate    bool      `json:"approximate"`
	Unpriced       int       `json:"unpriced"`
}

// Today sums the rows of the UTC day `at` falls on: a request count per UTC hour and the ten
// costliest sessions, ordered like each project's top sessions. Rows outside the day are
// left out; a row no price covers counts at no cost and turns Approximate on.
func Today(rows []Row, at time.Time, prices Pricer) TodayReport {
	midnight := clock.Midnight(at)
	from := midnight.UnixMilli()
	to := midnight.AddDate(0, 0, 1).UnixMilli()
	report := TodayReport{
		Date:           clock.DayKey(midnight),
		HourlyRequests: make([]int64, 24),
	}
	sessions := map[string]*sessionAcc{}
	for _, row := range rows {
		if row.TS < from || row.TS >= to {
			continue
		}
		price, priced := priceRow(row, prices)
		if !priced {
			report.Unpriced++
			report.Approximate = true
			price = RowPrice{}
		}
		report.HourlyRequests[(row.TS-from)/time.Hour.Milliseconds()]++
		sessionAt(sessions, row).add(row, price)
	}
	report.Sessions = make([]Session, 0, len(sessions))
	for _, acc := range sessions {
		report.Sessions = append(report.Sessions, acc.session)
	}
	sortSessions(report.Sessions)
	if len(report.Sessions) > todaySessions {
		report.Sessions = report.Sessions[:todaySessions]
	}
	return report
}
