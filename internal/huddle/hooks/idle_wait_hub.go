package hooks

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
)

const (
	// idleCatchUpLimit is how many events one events call asks for.
	idleCatchUpLimit = 100
	// idleCallMS bounds one plain call to the hub.
	idleCallMS = 5000
	// idleStreamSilence is how long a live stream may say nothing before the waiter drops it: the
	// hub pings every 15 s, so a longer silence is a hub that no longer answers.
	idleStreamSilence = 45 * time.Second
)

// idleStream is one waiter's view of its session's channel: the live stream, the events behind
// it, and the cursor between them.
type idleStream struct {
	w       *cli.World
	session string
	home    string
	cursor  int64
	// woken is the last seq a wake line carried.
	woken int64
}

// streamFrame is one server-sent event frame of the channel's live stream.
type streamFrame struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// take reads events past the cursor, moves the cursor over them and answers the ones that wake
// the session.
func (s *idleStream) take(ctx context.Context, id cli.Identity, events []wakeEvent) []wakeEvent {
	fresh := make([]wakeEvent, 0, len(events))
	for _, event := range events {
		if event.Seq > s.cursor {
			fresh = append(fresh, event)
			s.cursor = event.Seq
		}
	}
	fresh = s.withoutRaised(id, fresh)
	woken := wokenBy(id, fresh, func() bool { return s.pushLive(ctx, id) })
	if len(woken) > 0 {
		s.woken = woken[len(woken)-1].Seq
	}
	return woken
}

// withoutRaised drops the events the Stop hook already blocked a stop for (stop-raised.json, keyed
// by channel and member): the session has them. The file is read only when an event could wake
// the session.
func (s *idleStream) withoutRaised(id cli.Identity, events []wakeEvent) []wakeEvent {
	waking := false
	for _, event := range events {
		waking = waking || wakes(id.As, event)
	}
	if !waking {
		return events
	}
	raised := raisedSeqs(readRaised(stopRaised(s.home)), id.Channel+"/"+id.As)
	if len(raised) == 0 {
		return events
	}
	kept := make([]wakeEvent, 0, len(events))
	for _, event := range events {
		if !seenHas(raised, event.Seq) {
			kept = append(kept, event)
		}
	}
	return kept
}

// catchUp reads the channel's events after the cursor, page by page, and answers the ones that
// wake the session.
func (s *idleStream) catchUp(ctx context.Context, id cli.Identity) ([]wakeEvent, error) {
	for {
		page, err := s.eventsAfter(ctx, id, s.cursor)
		if err != nil {
			return nil, err
		}
		if woken := s.take(ctx, id, page); len(woken) > 0 {
			return woken, nil
		}
		if len(page) < idleCatchUpLimit {
			return nil, nil
		}
	}
}

// eventsAfter asks the hub for one page of the channel's events after a seq.
func (s *idleStream) eventsAfter(ctx context.Context, id cli.Identity, after int64) ([]wakeEvent, error) {
	body := fmt.Sprintf(`{"after":%d,"limit":%d}`, after, idleCatchUpLimit)
	callURL := fmt.Sprintf("%s/api/c/%s/op/events?as=%s", id.URL, url.PathEscape(id.Channel), url.QueryEscape(id.As))
	resp, err := cli.HFetch(ctx, s.w, http.MethodPost, callURL,
		map[string]string{"content-type": "application/json"}, []byte(body),
		idleCallMS*time.Millisecond, s.session)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("events: HTTP %d", resp.StatusCode)
	}
	var answer struct {
		Result []wakeEvent `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return nil, err
	}
	return answer.Result, nil
}

// pushLive says whether the hub delivers this session's events by channel push: its registry row
// names the now route while the session's bridge proves its pushes flow. Any failure answers no,
// so a repeated reminder is the worst outcome and a lost one is not.
func (s *idleStream) pushLive(ctx context.Context, id cli.Identity) bool {
	resp, err := cli.HFetch(ctx, s.w, http.MethodGet, id.URL+"/api/registry/sessions", nil, nil,
		idleCallMS*time.Millisecond, s.session)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}
	var answer struct {
		Sessions []struct {
			SessionID     string `json:"session_id"`
			DeliveryRoute string `json:"delivery_route"`
		} `json:"sessions"`
	}
	if json.NewDecoder(resp.Body).Decode(&answer) != nil {
		return false
	}
	for _, row := range answer.Sessions {
		if row.SessionID == s.session {
			return row.DeliveryRoute == "now"
		}
	}
	return false
}

// follow opens the channel's live stream and reads it until an event wakes the session, the
// stream breaks, or ctx ends. connected says the stream ever answered.
func (s *idleStream) follow(ctx context.Context, id cli.Identity, haveCursor bool) (woken []wakeEvent, connected bool, err error) {
	if id.URL == "" {
		return nil, false, errors.New("no hub url")
	}
	conn, cancel := context.WithCancel(ctx)
	defer cancel()
	silence := time.AfterFunc(idleStreamSilence, cancel)
	defer silence.Stop()
	liveURL := fmt.Sprintf("%s/api/c/%s/live?as=%s", id.URL, url.PathEscape(id.Channel), url.QueryEscape(id.As))
	resp, err := cli.HFetch(conn, s.w, http.MethodGet, liveURL,
		map[string]string{"accept": "text/event-stream"}, nil, 0, s.session)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false, fmt.Errorf("live stream: HTTP %d", resp.StatusCode)
	}
	reader := bufio.NewReader(resp.Body)
	data := ""
	for {
		line, err := reader.ReadString('\n')
		silence.Reset(idleStreamSilence)
		if err != nil {
			return nil, connected, errors.New("live stream ended")
		}
		line = strings.TrimRight(line, "\r\n")
		if rest, ok := strings.CutPrefix(line, "data: "); ok {
			data = rest
			continue
		}
		if line != "" || data == "" {
			continue
		}
		frame := streamFrame{}
		if json.Unmarshal([]byte(data), &frame) == nil {
			woken, hello, err := s.frame(ctx, id, frame, haveCursor)
			if hello {
				connected = true
				haveCursor = true
			}
			if err != nil || len(woken) > 0 {
				return woken, connected, err
			}
		}
		data = ""
	}
}

// frame handles one frame. The stream's hello is the moment to catch up on what the stream does
// not replay; a session the feed never saw starts at the hello's last seq.
func (s *idleStream) frame(ctx context.Context, id cli.Identity, f streamFrame, haveCursor bool) (woken []wakeEvent, hello bool, err error) {
	switch f.Type {
	case "hello":
		var data struct {
			Last int64 `json:"last"`
		}
		if json.Unmarshal(f.Data, &data) != nil {
			return nil, false, errors.New("bad hello")
		}
		if !haveCursor {
			s.cursor = data.Last
		}
		woken, err = s.catchUp(ctx, id)
		return woken, true, err
	case "event":
		var event wakeEvent
		if json.Unmarshal(f.Data, &event) != nil {
			return nil, false, nil
		}
		return s.take(ctx, id, []wakeEvent{event}), false, nil
	}
	return nil, false, nil
}
