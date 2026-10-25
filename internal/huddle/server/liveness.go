package server

import (
	"context"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
)

// registryLiveness answers a channel's liveness question from the registry: a member is alive
// while its registered session is working, idle or needs the owner; ended and stuck are not.
type registryLiveness struct{ db *hub.DB }

func (l registryLiveness) MemberAlive(ctx context.Context, channel, member string, now time.Time) (alive, known bool) {
	if l.db == nil {
		return false, false
	}
	state, ok := l.db.MemberState(ctx, channel, member, now)
	if !ok {
		return false, false
	}
	return state != hub.StateEnded && state != hub.StateStuck, true
}
