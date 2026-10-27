package server

import (
	"context"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
)

// registryLiveness answers a channel's state question from the registry: the state of the
// session registered for that member, unknown when no session is.
type registryLiveness struct{ db *hub.DB }

func (l registryLiveness) MemberState(ctx context.Context, channel, member string, now time.Time) (string, bool) {
	if l.db == nil {
		return "", false
	}
	return l.db.MemberState(ctx, channel, member, now)
}
