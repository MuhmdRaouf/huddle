package hooks

import (
	"fmt"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// wakeLineMax caps one event's text in a wake line, in UTF-16 units like the feed's.
const wakeLineMax = 600

// wakeEvent is one channel event as the hub lists and streams it; To is empty for an event
// meant for the whole channel.
type wakeEvent struct {
	Seq        int64  `json:"seq"`
	From       string `json:"from"`
	To         string `json:"to"`
	Topic      string `json:"topic"`
	Msg        string `json:"msg"`
	NeedsReply bool   `json:"needs_reply"`
}

// directTopic says whether a topic wakes a session when it is sent to the session by name.
func directTopic(topic string) bool {
	switch topic {
	case "msg", "ask", "reply", "task.ready", "turn.pass":
		return true
	}
	return false
}

// wakes says whether an event is meant for the session named me: an ask or a handover that
// needs its reply, a direct message, a reply, a ready task or a passed turn addressed to it, and
// a message to the whole channel. Its own words, its subagents' words, joins and leaves, task
// updates, knowledge and traffic between two other sessions never wake it.
func wakes(me string, e wakeEvent) bool {
	if e.From == me || strings.HasPrefix(e.From, me+".") || strings.HasPrefix(e.Topic, "session.") {
		return false
	}
	switch e.To {
	case me:
		return e.NeedsReply || directTopic(e.Topic)
	case "":
		return e.NeedsReply || e.Topic == "msg"
	}
	return false
}

// wokenBy answers the events that wake the session, in order. A session that has not joined a
// channel is woken by nothing. pushLive is asked at most once, and only when an event would wake
// the session: it says whether the session's channel push already delivers its events.
func wokenBy(id cli.Identity, events []wakeEvent, pushLive func() bool) []wakeEvent {
	if id.Channel == "" || id.As == "" {
		return nil
	}
	var woken []wakeEvent
	for _, event := range events {
		if wakes(id.As, event) {
			woken = append(woken, event)
		}
	}
	if len(woken) > 0 && pushLive() {
		return nil
	}
	return woken
}

// wakeLines answers one reminder line per event that wakes the session.
func wakeLines(id cli.Identity, events []wakeEvent, pushLive func() bool) []string {
	woken := wokenBy(id, events, pushLive)
	lines := make([]string, 0, len(woken))
	for _, event := range woken {
		lines = append(lines, wakeLine(event))
	}
	return lines
}

// wakeLine renders one event on one line, with how to answer it when it asks for a reply.
func wakeLine(e wakeEvent) string {
	text := jsscalar.UTF16Slice(strings.Join(strings.Fields(e.Msg), " "), 0, wakeLineMax)
	switch {
	case e.Topic == "task.ready":
		return "task ready: " + text
	case e.Topic == "turn.pass":
		return fmt.Sprintf("%s passed you the turn: %s", e.From, text)
	case e.Topic == "reply":
		return fmt.Sprintf("%s replied: %s", e.From, text)
	case e.NeedsReply:
		return fmt.Sprintf(`%s asks: %s (reply with: huddle reply %d "<text>")`, e.From, text, e.Seq)
	case e.To == "":
		return fmt.Sprintf("%s says to the channel: %s", e.From, text)
	case e.Topic == "msg":
		return fmt.Sprintf("%s says: %s", e.From, text)
	}
	return fmt.Sprintf("%s (%s): %s", e.From, e.Topic, text)
}

// idleKeepalive is the line a waiter ends its cycle with: it wakes the session once so the turn's
// Stop starts the next waiter, and tells it plainly that nothing is asked.
func idleKeepalive(channel string) string {
	return fmt.Sprintf("huddle: still listening in %s; no new messages, no reply needed.", channel)
}
