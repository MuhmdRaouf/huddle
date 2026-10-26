package radartab

// One turn's output tokens: the main agent's calls since the turn started, summed through the
// tab's own request list.

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"time"
)

// OutputTokensSince answers the output tokens one session's main agent has spent since a
// moment: the sum over radar's request records for that session and agent, the records marked
// as a run's total left out — a totals record repeats a run's total, and counting it would
// count the run twice. False when radar's request list does not answer or its body does not
// decode; (0, true) when radar holds no call of the session since then. The read asks for
// radar's largest page, so a turn of more than 1000 calls under-counts: the request list
// answers 1000 records at most.
func (t *Tab) OutputTokensSince(ctx context.Context, session string, since time.Time) (int64, bool) {
	path := "/api/requests?" + url.Values{
		"session": {session},
		"agent":   {"main"},
		"since":   {strconv.FormatInt(since.UnixMilli(), 10)},
		"limit":   {"1000"},
	}.Encode()
	body, ok := t.RadarAnswer(ctx, path)
	if !ok {
		return 0, false
	}
	var answer struct {
		Requests []struct {
			Tokens struct {
				Output int64 `json:"output"`
			} `json:"tokens"`
			Totals bool `json:"totals"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return 0, false
	}
	var total int64
	for _, request := range answer.Requests {
		if request.Totals {
			continue
		}
		total += request.Tokens.Output
	}
	return total, true
}
