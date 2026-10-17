// The digests table (SPEC 8): one row per session, holding the newest digest its Stop hook
// built. A digest replaces its row whole — it is a summary of a session's whole run so far, so
// blending two would read like neither — and DropPendingDigests keeps the "delivered once" rule
// on the peer side: the newest digest of a sender replaces the one still waiting.
package hub

import (
	"context"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
)

// saveDigestSQL upserts one session's digest; the report's own transaction runs it, so a
// digest lands exactly when its Stop does.
const saveDigestSQL = `
INSERT INTO digests (session_id, text, at) VALUES (?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET text = excluded.text, at = excluded.at`

// SaveDigest records one session's newest digest.
func (d *DB) SaveDigest(ctx context.Context, session, text, at string) error {
	return d.st.Run(ctx, saveDigestSQL, session, text, at)
}

// DigestOf answers a session's stored digest and the Stop that carried it; ok is false for a
// session that has digested nothing yet.
func (d *DB) DigestOf(ctx context.Context, session string) (text, at string, ok bool, err error) {
	row, err := d.st.Get(ctx, "SELECT text, at FROM digests WHERE session_id = ?", session)
	if err != nil || row == nil {
		return "", "", false, err
	}
	return textOfRow(row, "text"), textOfRow(row, "at"), true, nil
}

// DropPendingDigests removes the digest still waiting to reach to from from: the next one
// replaces it, so a digest-level peer holds at most the newest of each sender (SPEC 8).
func (d *DB) DropPendingDigests(ctx context.Context, from, to string) error {
	return d.st.Run(ctx,
		"DELETE FROM messages WHERE state = 'pending' AND from_kind = ? AND from_name = ? AND session_id = ?",
		FromDigest, from, to)
}

// saveDigestInTx is saveDigestSQL on a transaction's handle.
func saveDigestInTx(ctx context.Context, q store.Q, session, text, at string) error {
	return q.Run(ctx, saveDigestSQL, session, text, at)
}
