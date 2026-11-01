package cli

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

const askUsage = "ask <session[,session…]> <question>"

// ask puts a question to one or more sessions and prints one "name: answer" line per reply as it
// arrives. The first ask_wait sends the question; each next one carries the ask's seq and the last
// reply's seq, so it returns only newer replies. --timeout bounds the whole ask: each wait gets the
// whole seconds left. It exits 0 once every session has replied or the owner closed the ask, and
// 124 with the last result when the time runs out or a wait is cancelled.
func (c *cli) ask(ctx context.Context) int {
	to, ok := c.shift()
	if !ok {
		return c.die(askUsage, 2)
	}
	msg := c.rest()
	if msg == "" {
		return c.die(askUsage, 2)
	}
	timeout := 600.0
	if v, ok := c.opt.text("timeout"); ok {
		timeout = jsNumber(v)
	}
	deadline := time.Now().Add(time.Duration(timeout * float64(time.Second)))
	args := newBody().rawList("to", splitList(to)).textAlways("msg", msg).number("timeout", timeout)
	for {
		got, code := c.call(ctx, "ask_wait", args)
		if code != 0 {
			return code
		}
		var after *jsfmt.Value
		replies, _ := fieldOf(got.result, "replies")
		if replies != nil {
			for _, reply := range replies.Items() {
				c.say(replyLine(reply))
				if seq, ok := fieldOf(reply, "seq"); ok {
					after = seq
				}
			}
		}
		if flagged(got.result, "timeout") || flagged(got.result, "cancelled") {
			c.say(c.resultJSON(got))
			return 124
		}
		if flagged(got.result, "closed") {
			return 0
		}
		asked, present := fieldOf(got.result, "asked")
		if !present || after == nil {
			c.say(c.resultJSON(got))
			return 124
		}
		left := math.Ceil(time.Until(deadline).Seconds())
		if left <= 0 {
			c.say(c.resultJSON(got))
			return 124
		}
		args = newBody().raw("asked", asked).raw("after", after).number("timeout", left)
	}
}

// replyLine is one reply as "name: answer".
func replyLine(reply *jsfmt.Value) string {
	from, _ := strField(reply, "from")
	answer, _ := fieldOf(reply, "answer")
	switch {
	case answer == nil || answer.Kind() == jsfmt.KindNull:
		return fmt.Sprintf("%s: null", from)
	case answer.Kind() == jsfmt.KindString:
		return fmt.Sprintf("%s: %s", from, answer.Text())
	}
	return fmt.Sprintf("%s: %s", from, answer.EncodeJSON())
}

// flagged says whether an object's boolean field is true.
func flagged(value *jsfmt.Value, key string) bool {
	field, ok := fieldOf(value, key)
	return ok && field.Boolean()
}
