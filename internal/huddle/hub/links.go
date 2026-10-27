// Links: a session tied to a channel at a level, without membership of the channel. The
// server fans the channel's broadcast posts out to the linked sessions through the owner-message
// queue, so a link on a satellite is delivered by the satellite's own poll, with no code of its
// own.
package hub

import (
	"context"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
)

// The three levels a link hears its channel at: every broadcast post, only the digests,
// or nothing until the session is briefed itself.
const (
	LevelOverhear  = "overhear"
	LevelDigest    = "digest"
	LevelOnRequest = "on-request"
)

// ValidLevel answers whether l names one of the three levels.
func ValidLevel(l string) bool {
	switch l {
	case LevelOverhear, LevelDigest, LevelOnRequest:
		return true
	}
	return false
}

// Link is one row of the links table: a session's tie to a channel at a level.
type Link struct {
	Channel   string `json:"channel"`
	SessionID string `json:"session_id"`
	Level     string `json:"level"`
	LinkedAt  string `json:"linked_at"`
}

// LinkRow is one link with the time of its session's newest digest, empty for a session that
// has digested nothing yet — the one fact the links list adds to the tie itself.
type LinkRow struct {
	Link
	DigestAt string `json:"digest_at,omitempty"`
}

// LinkSessions ties each session to the channel at the one level, in one transaction: a session
// already linked keeps its row with its level and time moved to this link's.
func (d *DB) LinkSessions(ctx context.Context, channel string, sessions []string, level, at string) error {
	return d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		for _, session := range sessions {
			if err := q.Run(ctx,
				"INSERT INTO links (channel, session_id, level, linked_at) VALUES (?, ?, ?, ?) "+
					"ON CONFLICT(channel, session_id) DO UPDATE SET level = excluded.level, linked_at = excluded.linked_at",
				channel, session, level, at); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetLinkLevel moves one link's level and answers whether a link was there to move.
func (d *DB) SetLinkLevel(ctx context.Context, channel, session, level string) (bool, error) {
	var moved bool
	err := d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		if err := q.Run(ctx,
			"UPDATE links SET level = ? WHERE channel = ? AND session_id = ?", level, channel, session); err != nil {
			return err
		}
		return countChanged(ctx, q, &moved)
	})
	return moved, err
}

// Unlink ends one link at once (no further sharing) and answers whether a link was
// there to end.
func (d *DB) Unlink(ctx context.Context, channel, session string) (bool, error) {
	var gone bool
	err := d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		if err := q.Run(ctx,
			"DELETE FROM links WHERE channel = ? AND session_id = ?", channel, session); err != nil {
			return err
		}
		return countChanged(ctx, q, &gone)
	})
	return gone, err
}

// countChanged reads SQLite's changes() inside the statement's transaction: the statement's own
// count of the rows it met, so the answer never races a second writer.
func countChanged(ctx context.Context, q store.Q, into *bool) error {
	row, err := q.Get(ctx, "SELECT changes() AS n")
	if err != nil {
		return err
	}
	if row == nil {
		return nil
	}
	if n, ok := row["n"].(int64); ok {
		*into = n > 0
	}
	return nil
}

// AllLinks answers every link of every channel, ordered by channel then session, each joined
// with the time of its session's newest digest in one query — the list the Settings section
// draws needs no read per channel.
func (d *DB) AllLinks(ctx context.Context) ([]LinkRow, error) {
	rows, err := d.st.All(ctx,
		"SELECT l.channel, l.session_id, l.level, l.linked_at, d.at AS digest_at "+
			"FROM links l LEFT JOIN digests d ON d.session_id = l.session_id "+
			"ORDER BY l.channel, l.session_id")
	if err != nil {
		return nil, err
	}
	out := make([]LinkRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, LinkRow{
			Link: Link{
				Channel:   textOfRow(row, "channel"),
				SessionID: textOfRow(row, "session_id"),
				Level:     textOfRow(row, "level"),
				LinkedAt:  textOfRow(row, "linked_at"),
			},
			DigestAt: textOfRow(row, "digest_at"),
		})
	}
	return out, nil
}

// LinksOf answers the channel's links ordered by session, empty when it holds none.
func (d *DB) LinksOf(ctx context.Context, channel string) ([]Link, error) {
	rows, err := d.st.All(ctx,
		"SELECT channel, session_id, level, linked_at FROM links WHERE channel = ? ORDER BY session_id", channel)
	if err != nil {
		return nil, err
	}
	return linksOfRows(rows), nil
}

// LinksOfSession answers every channel the session is linked to, ordered by channel: a session
// may sit in more than one channel's link list, and its digest reaches each.
func (d *DB) LinksOfSession(ctx context.Context, session string) ([]Link, error) {
	rows, err := d.st.All(ctx,
		"SELECT channel, session_id, level, linked_at FROM links WHERE session_id = ? ORDER BY channel", session)
	if err != nil {
		return nil, err
	}
	return linksOfRows(rows), nil
}

// linksOfRows shapes link rows whole.
func linksOfRows(rows []store.Row) []Link {
	out := make([]Link, 0, len(rows))
	for _, row := range rows {
		out = append(out, Link{
			Channel:   textOfRow(row, "channel"),
			SessionID: textOfRow(row, "session_id"),
			Level:     textOfRow(row, "level"),
			LinkedAt:  textOfRow(row, "linked_at"),
		})
	}
	return out
}
