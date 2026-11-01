package channel

import (
	"context"
	"fmt"
)

// askEvents selects the events of one ask by its group seq: the ask's first event, which is its
// own group, and the events tied to it by data.group. An ask to one session has no data.group and
// is its own group.
const askEvents = `topic = 'ask' AND (seq = ? OR json_extract(data, '$.group') = ?)`

// askClosed holds for an ask event e whose group the owner closed: an ask.closed event answers
// the group's seq.
const askClosed = `EXISTS (SELECT 1 FROM events x WHERE x.topic = 'ask.closed'
    AND x.reply_to = COALESCE(json_extract(e.data, '$.group'), e.seq))`

// AskView is the tally of one ask: who was asked, who replied, who is still waited for. An ask is
// closed when nobody is waited for, or when the owner closed it early.
type AskView struct {
	Group   int64    `json:"group"`
	From    string   `json:"from"`
	To      []string `json:"to"`
	Replied []string `json:"replied"`
	Waiting []string `json:"waiting"`
	Closed  bool     `json:"closed"`
}

// AskReply is one session's answer to an ask.
type AskReply struct {
	From   string `json:"from"`
	Seq    int64  `json:"seq"`
	Answer string `json:"answer"`
}

// AskProgress is what WaitReplies found: the replies that arrived after the cursor and the tally
// behind them. Kind is "event" when it returned on a reply or a close, else "timeout" or
// "cancelled".
type AskProgress struct {
	Kind    string
	Replies []AskReply
	View    AskView
}

// AskStatus is the tally of the ask that seq belongs to: its group's seq, or any event of it.
func (c *Channel) AskStatus(ctx context.Context, seq int64) (AskView, error) {
	view, _, err := c.loadAsk(ctx, seq)
	return view, err
}

// CloseAsk is the owner ending an ask early: the sessions that have not replied stop seeing it in
// their inbox, and a late reply is refused.
func (c *Channel) CloseAsk(ctx context.Context, by string, seq int64) (AskView, error) {
	var out AskView
	err := c.serial(ctx, func(ctx context.Context) error {
		if by != Owner {
			return httpErr(403, "only the owner closes an ask")
		}
		view, _, err := c.loadAsk(ctx, seq)
		if err != nil {
			return err
		}
		if view.Closed {
			return httpErr(409, fmt.Sprintf("ask %d is already closed", view.Group))
		}
		if _, err := c.append(ctx, by, evIn{
			topic: "ask.closed", msg: sp(fmt.Sprintf("closed ask %d", view.Group)), replyTo: sp2(view.Group),
		}); err != nil {
			return err
		}
		view.Closed = true
		out = view
		return nil
	})
	return out, err
}

// WaitReplies blocks until a reply to the ask arrives with a seq above after, or the ask is
// closed, or the wait times out. It answers every reply above the cursor.
func (c *Channel) WaitReplies(ctx context.Context, name string, seq, after int64, timeoutS int) (AskProgress, error) {
	if _, err := c.need(ctx, name); err != nil {
		return AskProgress{}, err
	}
	bctx := context.Background()
	var found AskProgress
	check := func() (*WaitHit, error) {
		view, replies, err := c.loadAsk(bctx, seq)
		if err != nil {
			return nil, err
		}
		fresh := []AskReply{}
		for _, r := range replies {
			if r.Seq > after {
				fresh = append(fresh, r)
			}
		}
		if len(fresh) == 0 && !view.Closed {
			return nil, nil
		}
		found = AskProgress{Kind: "event", Replies: fresh, View: view}
		return &WaitHit{Kind: "event"}, nil
	}
	h, err := c.sleep(ctx, name, check, timeoutS,
		func() PresenceInput {
			return PresenceInput{State: sp("waiting"), Task: sp(fmt.Sprintf("waiting for replies to #%d", seq))}
		},
		func(h WaitHit) error {
			if s, _ := c.Session(bctx, name); s != nil && s.State == "waiting" {
				task := fmt.Sprintf("reply to #%d received", seq)
				if h.Kind == "timeout" {
					task = "no reply yet"
				}
				if _, err := c.Presence(bctx, name, PresenceInput{State: sp("working"), Task: sp(task)}); err != nil {
					return err
				}
			}
			return nil
		}, nil)
	if err != nil {
		return AskProgress{}, err
	}
	if h.Kind != "event" {
		view, _, err := c.loadAsk(bctx, seq)
		if err != nil {
			return AskProgress{}, err
		}
		return AskProgress{Kind: h.Kind, Replies: []AskReply{}, View: view}, nil
	}
	return found, nil
}

// loadAsk reads the ask that seq belongs to, with its replies in the order they arrived.
func (c *Channel) loadAsk(ctx context.Context, seq int64) (AskView, []AskReply, error) {
	ev, err := c.Event(ctx, seq)
	if err != nil {
		return AskView{}, nil, err
	}
	if ev == nil {
		return AskView{}, nil, httpErr(404, fmt.Sprintf("no event %d", seq))
	}
	if ev.Topic != "ask" {
		return AskView{}, nil, bad(fmt.Sprintf("event %d is not an ask", seq))
	}
	group := seq
	if g := argIntPtr(ev.Data, "group"); g != nil {
		group = *g
	}
	rows, err := c.dbAll(ctx, "SELECT * FROM events WHERE "+askEvents+" ORDER BY seq", group, group)
	if err != nil {
		return AskView{}, nil, err
	}
	if len(rows) == 0 {
		return AskView{}, nil, httpErr(404, fmt.Sprintf("no ask %d", group))
	}
	first := toEv(rows[0])
	to, err := c.askedSessions(ctx, first)
	if err != nil {
		return AskView{}, nil, err
	}
	replyRows, err := c.dbAll(ctx,
		"SELECT * FROM events WHERE topic = 'reply' AND reply_to IN (SELECT seq FROM events WHERE "+askEvents+") ORDER BY seq",
		group, group)
	if err != nil {
		return AskView{}, nil, err
	}
	view := AskView{Group: group, From: first.From, To: to, Replied: []string{}, Waiting: []string{}}
	replies := []AskReply{}
	seen := map[string]bool{}
	for _, r := range replyRows {
		e := toEv(r)
		if !seen[e.From] {
			seen[e.From] = true
			view.Replied = append(view.Replied, e.From)
			replies = append(replies, AskReply{From: e.From, Seq: e.Seq, Answer: deref(e.Msg)})
		}
	}
	for _, name := range to {
		if !seen[name] {
			view.Waiting = append(view.Waiting, name)
		}
	}
	closed, err := c.dbGet(ctx, "SELECT 1 FROM events WHERE topic = 'ask.closed' AND reply_to = ?", group)
	if err != nil {
		return AskView{}, nil, err
	}
	view.Closed = closed != nil || (len(to) > 0 && len(view.Waiting) == 0)
	return view, replies, nil
}

// askedSessions is who an ask's first event waits for: its recipient list, its one recipient, or
// every other session still in the channel for an ask to everyone.
func (c *Channel) askedSessions(ctx context.Context, first Ev) ([]string, error) {
	if to := strsOf(objField(first.Data, "to")); len(to) > 0 {
		return to, nil
	}
	if first.To != nil {
		return []string{*first.To}, nil
	}
	rows, err := c.dbAll(ctx, "SELECT name FROM sessions WHERE name != ? AND state != 'left' ORDER BY joined_at, name", first.From)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = rStr(r, "name")
	}
	return names, nil
}
