// What a Claude session has not seen yet in its channels (its own, plus "listen" in huddle.json
// or HUDDLE_LISTEN), as text for its context. The listen hook calls it after every tool call
// and on every prompt; the session-start hook marks "now" so a new session starts clean.
//
// It shows the others' messages, asks and replies in full, also those between other sessions,
// and folds the rest (task updates, knowledge entries, custom topics such as build.ready) into
// one count line, so a busy channel costs a line per tool call, not thirty ("listen_detail":
// "all" shows every event in full). Joins and leaves (session.*), this session's own subagents
// (<me>.<role>) and an owner's message to another session stay out. It remembers, per Claude
// session, the last seq shown in each channel: <Huddle home>/seen/<session id>.json. Never
// blocks long: 1.5 s (or the caller's budget), silent when down.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	channelpkg "github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// feedLimit is how many events one look pulls per channel.
const feedLimit = 30

// FeedOptions shape one feed call.
type FeedOptions struct {
	// Start marks "now" for a fresh session: nothing older is shown, and sessions idle for a
	// week are forgotten.
	Start bool
	// MS is the caller's budget; 0 takes the 1.5 s ceiling.
	MS int
}

// feedEvent is one event of a channel timeline.
type feedEvent struct {
	Seq        int64  `json:"seq"`
	From       string `json:"from"`
	To         string `json:"to"`
	Topic      string `json:"topic"`
	Msg        string `json:"msg"`
	NeedsReply bool   `json:"needs_reply"`
}

// timeline reads a channel's newest events after a seq, oldest first; nil when the server does
// not answer.
func timeline(ctx context.Context, w *World, server, channel string, after int64, limit int, ms int) []feedEvent {
	callURL := fmt.Sprintf("%s/api/c/%s/timeline?after=%d&limit=%d", server, url.PathEscape(channel), after, limit)
	resp, err := HFetch(ctx, w, http.MethodGet, callURL, nil, nil, time.Duration(ms)*time.Millisecond, "")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	var events []feedEvent
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return nil
	}
	return events
}

// Feed answers what this session has not seen yet in its channels, as text for its context;
// empty when there is nothing to show or Huddle is down.
func Feed(ctx context.Context, w *World, id Identity, home, session string, o FeedOptions) string {
	ms := o.MS
	if ms == 0 {
		ms = 1500
	}
	ms = max(1, min(1500, ms)) // the caller's budget, never more than 1.5 s
	sid := cleanKey(session)
	if id.Channel == "" || id.As == "" || sid == "" {
		return ""
	}
	dir := filepath.Join(home, "seen")
	file := filepath.Join(dir, sid+".json")
	seen := map[string]int64{}
	if !o.Start {
		if raw, err := os.ReadFile(file); err == nil {
			if value, err := jsfmt.DecodeValue(raw); err == nil && value.Kind() == jsfmt.KindObject {
				for i, key := range value.Keys() {
					if field := value.Fields()[i]; field.Kind() == jsfmt.KindNumber {
						seen[key] = int64(numberValue(field))
					}
				}
			}
		}
	}
	channels := []string{id.Channel}
	for _, extra := range id.Listen {
		if extra != id.Channel {
			channels = append(channels, extra)
		}
	}
	parts := make([]string, 0, len(channels))
	for _, channel := range channels {
		from, known := seen[channel]
		if !known { // first look: start from the newest event
			last := timeline(ctx, w, id.URL, channel, 0, 1, ms)
			if last != nil {
				seen[channel] = newestSeq(last)
			}
			parts = append(parts, "")
			continue
		}
		events := timeline(ctx, w, id.URL, channel, from, feedLimit, ms)
		if len(events) == 0 {
			parts = append(parts, "")
			continue
		}
		seen[channel] = events[len(events)-1].Seq
		text := channelPart(id, channel, events, from)
		parts = append(parts, text)
	}
	writeSeen(w, dir, file, seen, channels, o.Start)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return strings.Join(out, "\n\n")
}

// newestSeq is the newest event's seq, 0 when there is none.
func newestSeq(events []feedEvent) int64 {
	if len(events) == 0 {
		return 0
	}
	return events[len(events)-1].Seq
}

// mine answers whether an event's sender is this session or one of its subagents.
func mine(as, from string) bool {
	return from == as || strings.HasPrefix(from, as+".")
}

// channelPart renders one channel's new events.
func channelPart(id Identity, channel string, events []feedEvent, from int64) string {
	shown := make([]feedEvent, 0, len(events))
	for _, event := range events {
		if mine(id.As, event.From) || strings.HasPrefix(event.Topic, "session.") ||
			channelpkg.OwnerPrivate(event.From, event.To, id.As) {
			continue
		}
		shown = append(shown, event)
	}
	if len(shown) == 0 {
		return ""
	}
	other := ""
	if channel != id.Channel {
		other = fmt.Sprintf(" (channel %s)", channel)
	}
	// messages, asks and replies in full; everything the session must act on too
	talk := func(e feedEvent) bool {
		if id.Detail == "all" {
			return true
		}
		return e.Topic == "msg" || e.Topic == "ask" || e.Topic == "reply" || e.NeedsReply || e.To == id.As
	}
	lines := make([]string, 0, len(shown))
	folded := make([]feedEvent, 0, len(shown))
	for _, event := range shown {
		if talk(event) {
			lines = append(lines, eventLine(id, event, other))
		} else {
			folded = append(folded, event)
		}
	}
	if len(folded) > 0 {
		tasks, knowledge := 0, 0
		for _, event := range folded {
			switch {
			case strings.HasPrefix(event.Topic, "task."):
				tasks++
			case strings.HasPrefix(event.Topic, "kb."):
				knowledge++
			}
		}
		rest := len(folded) - tasks - knowledge
		counts := []string{}
		if tasks > 0 {
			counts = append(counts, fmt.Sprintf("%d task update%s", tasks, plural(tasks)))
		}
		if knowledge > 0 {
			counts = append(counts, fmt.Sprintf("%d knowledge entr%s", knowledge, pluralY(knowledge)))
		}
		if rest > 0 {
			counts = append(counts, fmt.Sprintf("%d other event%s", rest, plural(rest)))
		}
		lines = append(lines, fmt.Sprintf("+%s (events after=%d for them)", strings.Join(counts, ", +"), folded[0].Seq-1))
	}
	part := fmt.Sprintf("Huddle, new in %q (you are %s):\n%s", channel, id.As, strings.Join(lines, "\n"))
	if len(events) == feedLimit {
		part += fmt.Sprintf("\n(older ones: events after=%d)", from)
	}
	return part
}

// plural answers "" for one, "s" for more.
func plural(n int) string {
	if n > 1 {
		return "s"
	}
	return ""
}

// pluralY answers "y" for one, "ies" for more (one knowledge entry, two entries).
func pluralY(n int) string {
	if n > 1 {
		return "ies"
	}
	return "y"
}

// eventLine renders one event in full.
func eventLine(id Identity, e feedEvent, other string) string {
	to := e.To
	if to == "" {
		to = "all"
	}
	tail := ""
	switch {
	case e.To != "" && e.To != id.As:
		tail = " [overheard: between other sessions]"
	case e.NeedsReply:
		tail = fmt.Sprintf(" [answer: reply seq=%d%s]", e.Seq, other)
	}
	return fmt.Sprintf("#%d %s → %s (%s): %s%s", e.Seq, e.From, to, e.Topic,
		jsfmt.UTF16Slice(e.Msg, 0, 600), tail)
}

// writeSeen stores what each channel has shown, in channel order, and on a start forgets the
// sessions idle for a week. A failure to store never surfaces: the next call re-reads.
func writeSeen(w *World, dir, file string, seen map[string]int64, channels []string, start bool) {
	keys := make([]string, 0, len(channels))
	values := make([]*jsfmt.Value, 0, len(channels))
	for _, channel := range channels {
		if _, known := seen[channel]; known {
			keys = append(keys, channel)
			values = append(values, jsfmt.Number(float64(seen[channel])))
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, []byte(jsfmt.Object(keys, values).EncodeJSON()), 0o600); err == nil {
		_ = os.Rename(tmp, file)
	}
	if !start {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if w.now().Sub(info.ModTime()) > 7*24*time.Hour {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}
