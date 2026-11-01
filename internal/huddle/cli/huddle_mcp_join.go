// The bridge finishes the join the SessionStart hook did not: a session whose channel does not
// list it as joined gets the same join call the hook sends, tried again with a growing pause
// until one lands or ten minutes pass. It runs beside the bridge's replies and never holds one.
// Once that join has ended, the bridge keeps the session on the list: the hub can lose the
// member while the live stream stays up, so the list is read on every reconnect and every
// bridgeMemberCheck, and a session the hub holds no row for joins again with the same call. A
// session that left stays out. A session that started with no channel is watched too: when its
// project's file comes to name one (the hub's "Add to channel" writes it), the bridge joins.
package cli

import (
	"context"
	"encoding/json"
	"errors"
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
// While the live stream is up the channel's list is read every bridgeMemberCheck.
const (
	bridgeJoinSettle = 3500 * time.Millisecond
	bridgeJoinFirst  = time.Second
	bridgeJoinCap    = 30 * time.Second
	bridgeJoinGiveUp = 10 * time.Minute
	bridgeJoinSlack  = time.Minute

	bridgeMemberCheck = 5 * time.Minute
	bridgeJoinReadMS  = 1500
	bridgeJoinCallMS  = 10000

	// bridgeChannelCheck is the pause between looks at the project's file while the session
	// names no channel.
	bridgeChannelCheck = 30 * time.Second
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
	defer close(b.joinDone)
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

// watchForChannel looks at the project's file every channelCheckEvery for a session that started
// in no channel, and joins the first channel and name it finds. Each look reads one file and
// calls no hub, so a session that never gets a channel costs nothing.
func (b *mcpBridge) watchForChannel(ctx context.Context) {
	ticker := time.NewTicker(channelCheckEvery(b.w))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		id := Identify(ctx, b.w, b.sessionID())
		if id.Channel == "" || id.As == "" {
			continue
		}
		b.memberMu.Lock()
		joined := b.sendJoin(ctx, id, false)
		b.memberMu.Unlock()
		if joined {
			return
		}
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
	return b.sendJoin(ctx, id, false)
}

// sendJoin sends the join call the SessionStart hook sends and, when it lands, brings the
// session's listen hook and live stream in. A rejoin leaves the hook's cursor where it is:
// what the session has not been shown stays unseen, and the live stream's catch-up delivers it.
func (b *mcpBridge) sendJoin(ctx context.Context, id Identity, rejoin bool) bool {
	session := b.sessionID()
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
	if !rejoin {
		_ = Feed(ctx, b.w, id, Home(ctx, b.w), session, FeedOptions{Start: true, MS: bridgeJoinReadMS})
	}
	b.refresh(ctx)
	if b.isInitialized() {
		b.startPush(ctx)
	}
	return true
}

// memberRow is what the channel's list shows of one session.
type memberRow struct {
	read  bool      // the list was read
	found bool      // the list holds a row under the session's name
	left  bool      // that row is in state left
	seen  time.Time // when that row was last seen; zero when the row gives no readable time
}

// listedAsJoined says whether the channel lists id.As as in: not left, and seen since just
// before this bridge started. Any failure to read says no, and the join that follows is harmless
// for a member that is in (it keeps its role and context).
func (b *mcpBridge) listedAsJoined(ctx context.Context, id Identity) bool {
	row := b.readMember(ctx, id)
	return row.read && row.found && !row.left && row.seen.After(b.startedAt.Add(-bridgeJoinSlack))
}

// readMember reads the channel's members and answers the row of id.As.
func (b *mcpBridge) readMember(ctx context.Context, id Identity) memberRow {
	callURL := id.URL + "/api/c/" + url.PathEscape(id.Channel) + "/sessions"
	resp, err := HFetch(ctx, b.w, http.MethodGet, callURL, nil, nil, bridgeJoinReadMS*time.Millisecond, b.sessionID())
	if err != nil {
		return memberRow{}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return memberRow{}
	}
	var list struct {
		Sessions []struct {
			Name     string `json:"name"`
			State    string `json:"state"`
			LastSeen string `json:"last_seen"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return memberRow{}
	}
	for _, member := range list.Sessions {
		if member.Name != id.As {
			continue
		}
		seen, _ := time.Parse(time.RFC3339Nano, jsscalar.Trim(member.LastSeen))
		return memberRow{read: true, found: true, left: member.State == "left", seen: seen}
	}
	return memberRow{read: true}
}

// watchMembership checks the list every memberEvery while the live stream is up. A stream that
// is down is checked by the reconnect that follows it.
func (b *mcpBridge) watchMembership(ctx context.Context) {
	ticker := time.NewTicker(b.memberEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.mu.Lock()
			up := b.streamUp
			b.mu.Unlock()
			if up {
				b.keepListed(ctx)
			}
		}
	}
}

// keepListed joins this session again when the hub holds no row for it, which is how a hub that
// lost its member looks. A row in state left is a leave someone asked for and stays; a row that
// is in is the hub's own member, however long ago it was seen; and a session that left through
// this bridge stays out for the rest of the run. It waits for keepJoined to end, so the two never
// send joins side by side, and one check runs at a time. A channel the hub says does not exist is
// not joined (a join would make one), and nor is a session the hub holds no credential for (a
// join would be refused).
func (b *mcpBridge) keepListed(ctx context.Context) {
	select {
	case <-b.joinDone:
	default:
		return
	}
	b.memberMu.Lock()
	defer b.memberMu.Unlock()
	b.mu.Lock()
	standDown := b.channelGone || b.credentialLost || b.leftThroughBridge
	b.mu.Unlock()
	if standDown || ctx.Err() != nil {
		return
	}
	id := Identify(ctx, b.w, b.sessionID())
	if id.Channel == "" || id.As == "" || id.URL == "" {
		return
	}
	if row := b.readMember(ctx, id); !row.read || row.found {
		return
	}
	b.logf("join: the channel holds no row for this session; joining again")
	b.sendJoin(ctx, id, true)
}

// noteRefusal reads what the live stream's last try came to. A 404 means the channel does not
// exist and a 401 that the hub holds no credential for this session; each is said once per run
// of the same answer, and ends with the next answer that differs. A stream that connected ends
// both.
func (b *mcpBridge) noteRefusal(err error, connected bool) {
	var refused *liveStatusError
	b.mu.Lock()
	gone, lost := false, false
	if !connected && errors.As(err, &refused) {
		gone, lost = refused.Code == http.StatusNotFound, refused.Code == http.StatusUnauthorized
	} else if !connected {
		gone, lost = b.channelGone, b.credentialLost // no answer from the hub: nothing new
	}
	sayGone, sayLost := gone && !b.channelGone, lost && !b.credentialLost
	b.channelGone, b.credentialLost = gone, lost
	channelName := b.channelName
	b.mu.Unlock()
	if sayGone {
		b.logf("the channel %s does not exist on the hub, so this session does not join it; /huddle:setup creates one", channelName)
	}
	if sayLost {
		b.logf("the hub holds no credential for this session; run /huddle:join with the join line /huddle:invite shows in a session that is in")
	}
}

// memberCheckEvery is the pause between membership checks: bridgeMemberCheck, unless
// HUDDLE_TEST_MEMBER_CHECK_MS names a shorter one in milliseconds. That variable exists for the
// tests, which cannot wait five minutes.
func memberCheckEvery(w *World) time.Duration {
	if ms := int(jsNumber(w.getenv("HUDDLE_TEST_MEMBER_CHECK_MS"))); ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return bridgeMemberCheck
}

// channelCheckEvery is the pause between looks at the project's file: bridgeChannelCheck, unless
// HUDDLE_TEST_MEMBER_CHECK_MS names a shorter one in milliseconds, the variable that shortens the
// membership check for the tests.
func channelCheckEvery(w *World) time.Duration {
	if ms := int(jsNumber(w.getenv("HUDDLE_TEST_MEMBER_CHECK_MS"))); ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return bridgeChannelCheck
}
