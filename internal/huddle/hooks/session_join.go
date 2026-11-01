// The join half of the SessionStart hook: one call to the channel's join op, tried again within
// the hook's budget when the hub is slow or not up yet, and the one line the session reads when
// no try landed.
package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
)

// joinAnswer is one join's outcome: the HTTP status and the op's answer.
type joinAnswer struct {
	Status int
	Error  string
	Text   string
}

// joined says the hub took the join.
func (a *joinAnswer) joined() bool {
	return a.Status >= 200 && a.Status < 300 && a.Error == ""
}

// joinTry is one call's result: the hub's answer, or, when it gave none, why.
type joinTry struct {
	Answer *joinAnswer
	// Why says what stopped a try with no answer, in the words the failure line uses.
	Why string
	// Refused marks a connection nothing accepted: a hub still starting may accept it soon.
	Refused bool
}

// What a try that got no answer says about it.
var (
	hubSilent    = fmt.Sprintf("the hub did not answer in %d s", startBudgetMS/1000)
	hubNoAddress = "no hub address yet"
)

// sessionJoin is the join one session start makes.
type sessionJoin struct {
	w       *cli.World
	id      cli.Identity
	context string // the join mode (cli.ContextFor)
	session string // the Claude Code session id
}

// try makes one join call at rawURL and waits at most ms for the answer.
func (s sessionJoin) try(ctx context.Context, rawURL string, ms int) joinTry {
	if rawURL == "" {
		return joinTry{Why: hubNoAddress} // this project's server has never started: no port yet
	}
	callURL, body, err := cli.SessionJoinCall(s.id, rawURL, s.context, s.session)
	if err != nil {
		return joinTry{Why: err.Error()}
	}
	resp, err := cli.HFetch(ctx, s.w, http.MethodPost, callURL,
		map[string]string{"content-type": "application/json"}, body,
		time.Duration(ms)*time.Millisecond, s.session)
	if err != nil {
		return joinTry{Why: hubSilent, Refused: errors.Is(err, syscall.ECONNREFUSED)}
	}
	defer resp.Body.Close()
	var fields struct {
		Error string `json:"error"`
		Text  string `json:"text"`
	}
	decodeErr := json.NewDecoder(resp.Body).Decode(&fields)
	if decodeErr != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return joinTry{Why: fmt.Sprintf("%d an answer that is not Huddle's", resp.StatusCode)}
	}
	return joinTry{Answer: &joinAnswer{Status: resp.StatusCode, Error: fields.Error, Text: fields.Text}}
}

// retry tries the join at rawURL until the hub answers or the budget is gone: the first try
// waits firstMS, each next one startJoinTryMS, and a refused connection waits a moment before
// the next. left is what remains of the hook's budget in milliseconds.
func (s sessionJoin) retry(ctx context.Context, rawURL string, firstMS int, left func() int) joinTry {
	ms := firstMS
	var last joinTry
	for range startJoinTries {
		last = s.try(ctx, rawURL, min(ms, left()))
		if last.Answer != nil || last.Why == hubNoAddress || left() < startJoinMinMS {
			return last
		}
		if last.Refused {
			time.Sleep(time.Duration(min(startRefusedPauseMS, left())) * time.Millisecond)
		}
		ms = startJoinTryMS
	}
	return last
}

// failure says why a session start did not join: the one line the session reads, and the same
// words for hooks.log.
func (s sessionJoin) failure(reason string) string {
	return "huddle: could not join " + s.id.Channel + " as " + s.id.As + ": " + reason +
		"; the session's Huddle bridge keeps trying, and /huddle:join gets you in by hand"
}

// reasonOf is why a try that did not join did not: what the hub said, or why it said nothing.
func reasonOf(t joinTry) string {
	if t.Answer == nil {
		return t.Why
	}
	return strings.TrimSpace(fmt.Sprintf("%d %s", t.Answer.Status, t.Answer.Error))
}
