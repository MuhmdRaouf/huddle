package channel

import (
	"context"
	"fmt"
	"slices"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// AllRecipients is the recipient that reaches every member.
const AllRecipients = "all"

// resolveRecipients turns a send's "to" list into the session names to deliver to, in the order
// given and without repeats. An empty list or ["all"] is everyone and answers nil. "all" among
// names and the owner among several names are refused: the owner's messages are private to one
// member, and a list is a set of members.
func resolveRecipients(to []string) ([]string, error) {
	var names []string
	seen := map[string]bool{}
	for _, name := range to {
		if name == "" {
			return nil, bad("a recipient needs a name")
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	if seen[AllRecipients] {
		if len(names) > 1 {
			return nil, bad(`"all" cannot be combined with session names`)
		}
		return nil, nil
	}
	if seen[Owner] && len(names) > 1 {
		return nil, bad("the owner can only be the sole recipient")
	}
	return names, nil
}

// IsBroadcast answers whether the body of a send op addresses everyone: no "to", an empty list or
// "all". A body the send would refuse is no broadcast.
func IsBroadcast(args *jsfmt.Value) bool {
	names, err := resolveRecipients(argRecipients(args))
	return err == nil && len(names) == 0
}

// sendFanOut writes one event per recipient in one transaction. A single recipient, or everyone
// (names empty), is one plain event. Several recipients share data.group, the seq of the first
// event, and each carries data.to, the whole list; a send key becomes one key per recipient so a
// retry repeats every event. It answers the first event.
func (c *Channel) sendFanOut(ctx context.Context, from string, names []string, in evIn) (Ev, error) {
	if len(names) <= 1 {
		if len(names) == 1 {
			in.to = sp(names[0])
		}
		return c.append(ctx, from, in)
	}
	var first Ev
	err := c.serial(ctx, func(ctx context.Context) error {
		err := c.tx(ctx, func(ctx context.Context, q store.Q) error {
			tied := withField(in.data, "to", jstrs(names))
			for i, name := range names {
				e := in
				e.to = sp(name)
				e.data = tied
				if in.key != nil {
					e.key = sp(*in.key + "#" + name)
				}
				if i == 0 {
					e.ownGroup = true
				} else {
					e.data = withField(tied, "group", jnum(first.Seq))
				}
				ev, err := c.insert(ctx, q, from, e)
				if err != nil {
					return err
				}
				if i == 0 {
					first = ev
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		c.flush()
		return nil
	})
	return first, err
}

// FoldedCopy answers whether an event is a later copy of a message to several sessions that the
// reader was not addressed in. The group's first copy, whose seq is data.group, stands for the
// group for such a reader; a recipient hears every copy, as before.
func FoldedCopy(reader string, seq int64, data *jsfmt.Value) bool {
	group := argIntPtr(data, "group")
	if group == nil || *group == seq {
		return false
	}
	return !slices.Contains(strsOf(objField(data, "to")), reader)
}

// tieToGroup stores the event's own seq as its data.group and answers the event as stored.
func (c *Channel) tieToGroup(ctx context.Context, q store.Q, ev Ev) (Ev, error) {
	data := withField(ev.Data, "group", jnum(ev.Seq))
	r, err := q.Get(ctx, "UPDATE events SET data=? WHERE seq=? RETURNING *", data.EncodeJSON(), ev.Seq)
	if err != nil {
		return Ev{}, err
	}
	if r == nil {
		return Ev{}, fmt.Errorf("event %d vanished inside its transaction", ev.Seq)
	}
	return toEv(r), nil
}

// withField answers a copy of the object value with one more field; a nil base is an empty object.
func withField(base *jsfmt.Value, key string, v *jsfmt.Value) *jsfmt.Value {
	var keys []string
	var fields []*jsfmt.Value
	if base != nil {
		keys = append(keys, base.Keys()...)
		fields = append(fields, base.Fields()...)
	}
	return jsfmt.Object(append(keys, key), append(fields, v))
}
