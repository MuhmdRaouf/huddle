package insights

import (
	"fmt"
	"math"
	"strconv"
	"time"
)

// The points lane_failing fires at.
const (
	laneFailingWindow   = 24 * time.Hour // how far back a request still counts
	laneFailingRequests = 20             // the requests a lane needs in the window before one tip
	laneFailingShare    = 5              // the percent failed above which a lane is failing
)

// laneTally is one lane's requests in the window: every one of them, the failed ones and the
// failures by status.
type laneTally struct {
	total    int
	failures int
	statuses map[int]int
}

// laneFailingTips names a lane that keeps failing: a provider that served 20 or more requests
// in the last 24 h of which more than 5% failed. The commonest status among the failures names
// the cause, and the id carries the day, so one dismissal holds for the day.
func laneFailingTips(in Input) []Tip {
	windowStart := in.Now.Add(-laneFailingWindow)
	byProvider := map[string]*laneTally{}
	for _, req := range in.Requests {
		if req.At.Before(windowStart) || req.At.After(in.Now) {
			continue
		}
		tally := byProvider[req.Provider]
		if tally == nil {
			tally = &laneTally{statuses: map[int]int{}}
			byProvider[req.Provider] = tally
		}
		tally.total++
		// the upstream never answered, or answered with a client or server error
		if req.Status == 0 || req.Status >= 400 {
			tally.failures++
			tally.statuses[req.Status]++
		}
	}
	var tips []Tip
	for provider, tally := range byProvider {
		if tally.total < laneFailingRequests || tally.failures*100 <= laneFailingShare*tally.total {
			continue
		}
		tips = append(tips, Tip{
			ID:   fmt.Sprintf("%s:%s:%s", RuleLaneFailing, provider, in.Now.UTC().Format("2006-01-02")),
			Rule: RuleLaneFailing,
			Sentence: fmt.Sprintf("The %s lane failed %d%% of %d requests in 24 h, mostly %s.",
				provider, percentOf(tally.failures, tally.total), tally.total, statusName(commonestStatus(tally.statuses))),
			Count: tally.failures,
		})
	}
	return tips
}

// percentOf is a share of a total as a percent, rounded to the nearest whole number.
func percentOf(share, total int) int {
	return int(math.Round(float64(share) * 100 / float64(total)))
}

// commonestStatus is the status most of a lane's failures share; on a tie the larger status wins.
func commonestStatus(statuses map[int]int) int {
	best, bestCount := 0, 0
	for status, count := range statuses {
		if count > bestCount || (count == bestCount && status > best) {
			best, bestCount = status, count
		}
	}
	return best
}

// statusName is how a status reads in a sentence: "no answer" when the upstream gave none.
func statusName(status int) string {
	if status == 0 {
		return "no answer"
	}
	return strconv.Itoa(status)
}
