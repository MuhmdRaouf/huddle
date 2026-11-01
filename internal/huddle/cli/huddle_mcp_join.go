// The bridge finishes the join the SessionStart hook did not: a session whose channel does not
// list it as joined gets the same join call the hook sends, tried again with a growing pause
// until one lands or ten minutes pass. It runs beside the bridge's replies and never holds one.
package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// How the bridge paces its join: it waits bridgeJoinSettle after it starts (the hook's join,
// which runs beside it, has its 3 s budget to land) or for the session's first tool call,
// whichever comes first; the pause between tries starts at bridgeJoinFirst, doubles up to
// bridgeJoinCap, and the bridge stops after bridgeJoinGiveUp. A member row counts as this
// session's when it is not left and was seen within bridgeJoinSlack before the bridge started.
const (
	bridgeJoinSettle = 3500 * time.Millisecond
	bridgeJoinFirst  = time.Second
	bridgeJoinCap    = 30 * time.Second
	bridgeJoinGiveUp = 10 * time.Minute
	bridgeJoinSlack  = time.Minute
	bridgeJoinReadMS = 1500
	bridgeJoinCallMS = 10000
)

// wakeJoin tells the join loop a tool call arrived: it checks now instead of waiting out the
// settle time. A wake already waiting stands for this one.
func (b *mcpBridge) wakeJoin() {
	select {
	case b.joinWake <- struct{}{}:
	default:
	}
}

// keepJoined joins this session to its channel when the channel does not list it, until a join
// lands, the session ends or bridgeJoinGiveUp passes.
func (b *mcpBridge) keepJoined(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-b.joinWake:
	case <-time.After(bridgeJoinSettle):
	}
	giveUp := time.NewTimer(bridgeJoinGiveUp)
	defer giveUp.Stop()
	pause := bridgeJoinFirst
	for {
		if b.joinOnce(ctx) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-giveUp.C:
			b.logf("join: gave up after %s; /huddle:join gets this session in", bridgeJoinGiveUp)
			return
		case <-time.After(pause):
		}
		pause = min(pause*2, bridgeJoinCap)
	}
}

// joinOnce makes one check and, when the session is not in, one join. It answers true when
// there is nothing more to do: the session is in, or it names no channel to join.
func (b *mcpBridge) joinOnce(ctx context.Context) bool {
	session := b.sessionID()
	id := Identify(ctx, b.w, session)
	if id.Channel == "" || id.As == "" {
		return true
	}
	if id.URL == "" {
		return false // the project's hub has no port yet: a later try may find one
	}
	// a join call that was sent and not confirmed may have landed without its link to the
	// Claude session, so the list no longer vouches for the session: the join goes again
	if !b.joinSent && b.listedAsJoined(ctx, id) {
		return true
	}
	callURL, body, err := SessionJoinCall(id, id.URL, ContextFor("", id.Context), session)
	if err != nil {
		b.logf("join: %v", err)
		return false
	}
	b.joinSent = true
	resp, err := HFetch(ctx, b.w, http.MethodPost, callURL, map[string]string{"content-type": "application/json"},
		body, bridgeJoinCallMS*time.Millisecond, session)
	if err != nil {
		b.logf("join: %v", err)
		return false
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b.logf("join: the hub answered %d", resp.StatusCode)
		return false
	}
	// from here on, the listen hook brings each new message into this session, as after the
	// hook's own join
	_ = Feed(ctx, b.w, id, Home(ctx, b.w), session, FeedOptions{Start: true, MS: bridgeJoinReadMS})
	b.refresh(ctx)
	if b.isInitialized() {
		b.startPush(ctx)
	}
	return true
}

// listedAsJoined reads the channel's members and says whether id.As is one that is in: not left,
// and seen since just before this bridge started. Any failure to read says no, and the join
// that follows is harmless for a member that is in (it keeps its role and context).
func (b *mcpBridge) listedAsJoined(ctx context.Context, id Identity) bool {
	callURL := id.URL + "/api/c/" + url.PathEscape(id.Channel) + "/sessions"
	resp, err := HFetch(ctx, b.w, http.MethodGet, callURL, nil, nil, bridgeJoinReadMS*time.Millisecond, b.sessionID())
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}
	var list struct {
		Sessions []struct {
			Name     string `json:"name"`
			State    string `json:"state"`
			LastSeen string `json:"last_seen"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return false
	}
	for _, member := range list.Sessions {
		if member.Name != id.As || member.State == "left" {
			continue
		}
		seen, err := time.Parse(time.RFC3339Nano, jsscalar.Trim(member.LastSeen))
		return err == nil && seen.After(b.startedAt.Add(-bridgeJoinSlack))
	}
	return false
}
