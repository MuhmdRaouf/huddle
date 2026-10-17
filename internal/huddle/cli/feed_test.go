package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeEvent is one timeline event as the server writes it.
type fakeEvent struct {
	Seq        int64  `json:"seq"`
	From       string `json:"from"`
	To         string `json:"to,omitempty"`
	Topic      string `json:"topic"`
	Msg        string `json:"msg,omitempty"`
	NeedsReply bool   `json:"needs_reply,omitempty"`
}

// feedServer answers the timeline endpoint over a set of canned events, the same way the server
// does: the newest limit events after a seq, oldest first.
func feedServer(t *testing.T) (url string, set func(channel string, events []fakeEvent)) {
	t.Helper()
	var mu sync.Mutex
	channels := map[string][]fakeEvent{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/c/") || !strings.HasSuffix(r.URL.Path, "/timeline") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		channel := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/c/"), "/")[0]
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		limit, _ := strconv.ParseInt(r.URL.Query().Get("limit"), 10, 64)
		mu.Lock()
		events := append([]fakeEvent{}, channels[channel]...)
		mu.Unlock()
		kept := []fakeEvent{}
		for i := len(events) - 1; i >= 0 && int64(len(kept)) < limit; i-- {
			if events[i].Seq > after {
				kept = append(kept, events[i])
			}
		}
		for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
			kept[i], kept[j] = kept[j], kept[i]
		}
		_ = json.NewEncoder(w).Encode(kept)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func(channel string, events []fakeEvent) {
		mu.Lock()
		defer mu.Unlock()
		channels[channel] = events
	}
}

func feedWorld(t *testing.T, url string) (*World, *Identity) {
	t.Helper()
	w := world(t, t.TempDir(), map[string]string{"HUDDLE_TOKEN": "root"})
	id := &Identity{URL: url, Channel: "h1", As: "b", Push: []string{}, Listen: []string{}, Detail: "digest"}
	return w, id
}

func TestFeedBringsNewMessagesOfTheChannelAndItsListenChannelsOnce(t *testing.T) {
	ctx := context.Background()
	url, set := feedServer(t)
	w, id := feedWorld(t, url)
	id.Listen = []string{"h2"}
	home := t.TempDir()
	// the first look marks now: nothing to show
	set("h1", []fakeEvent{{Seq: 7, From: "a", Topic: "msg", Msg: "earlier"}})
	if got := Feed(ctx, w, *id, home, "s-1", FeedOptions{}); got != "" {
		t.Fatalf("the first look = %q, want nothing", got)
	}
	// the others' messages in full, asks with their answer tail, overheard ones marked
	set("h1", []fakeEvent{
		{Seq: 7, From: "a", Topic: "msg", Msg: "earlier"},
		{Seq: 8, From: "a", To: "c", Topic: "msg", Msg: "between a and c"},
		{Seq: 9, From: "a", To: "b", Topic: "ask", Msg: "can you check?", NeedsReply: true},
		{Seq: 10, From: "a", Topic: "build.done", Msg: "its own topic"},
		{Seq: 11, From: "d", Topic: "session.joined"},
		{Seq: 12, From: "b", Topic: "msg", Msg: "mine own"},
		{Seq: 13, From: "b.explore", Topic: "msg", Msg: "my own subagent"},
	})
	set("h2", []fakeEvent{{Seq: 2, From: "a", Topic: "msg", Msg: "over in h2"}})
	got := Feed(ctx, w, *id, home, "s-1", FeedOptions{})
	for _, want := range []string{
		`Huddle, new in "h1" (you are b):`,
		"#8 a → c (msg): between a and c [overheard: between other sessions]",
		"#9 a → b (ask): can you check? [answer: reply seq=9]",
		"+1 other event (events after=9 for them)",
		`Huddle, new in "h2" (you are b):`,
		"#2 a → all (msg): over in h2",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the feed misses %q:\n%s", want, got)
		}
	}
	for _, not := range []string{"earlier", "its own topic", "session.joined", "mine own", "my own subagent"} {
		if strings.Contains(got, not) {
			t.Errorf("the feed shows %q:\n%s", not, got)
		}
	}
	// shown once: nothing new since
	if again := Feed(ctx, w, *id, home, "s-1", FeedOptions{}); again != "" {
		t.Errorf("shown twice:\n%s", again)
	}
	// task updates and knowledge fold into their counts
	set("h1", []fakeEvent{
		{Seq: 12, From: "b", Topic: "msg", Msg: "mine own"},
		{Seq: 13, From: "b.explore", Topic: "msg", Msg: "my own subagent"},
		{Seq: 14, From: "a", Topic: "task.doing", Msg: "build it"},
		{Seq: 15, From: "a", Topic: "task.done", Msg: "built"},
		{Seq: 16, From: "a", Topic: "kb.added", Msg: "a fact"},
	})
	counts := Feed(ctx, w, *id, home, "s-1", FeedOptions{})
	if !regexp.MustCompile(`\+2 task updates, \+1 knowledge entry \(events after=13 for them\)`).MatchString(counts) {
		t.Errorf("the count line:\n%s", counts)
	}
}

func TestFeedDetailsAndOlderHintsAndMarks(t *testing.T) {
	ctx := context.Background()
	url, set := feedServer(t)
	w, id := feedWorld(t, url)
	home := t.TempDir()
	var events []fakeEvent
	for i := int64(1); i <= 31; i++ {
		events = append(events, fakeEvent{Seq: i, From: "a", Topic: "msg", Msg: fmt.Sprintf("m%d", i)})
	}
	set("h1", events)
	Feed(ctx, w, *id, home, "s-1", FeedOptions{}) // the first look marks the newest
	// a window that holds more than the limit: the rest is pointed at, not shown
	for i := int64(32); i <= 62; i++ {
		events = append(events, fakeEvent{Seq: i, From: "a", Topic: "msg", Msg: fmt.Sprintf("m%d", i)})
	}
	set("h1", events)
	older := Feed(ctx, w, *id, home, "s-1", FeedOptions{})
	if !strings.Contains(older, "(older ones: events after=31)") {
		t.Errorf("the older hint:\n%s", older)
	}
	if !strings.Contains(older, "#33 a") || strings.Contains(older, "#32 a") || strings.Contains(older, "#31 a") {
		t.Errorf("the wrong window shown:\n%s", older)
	}
	// every event in full with listen detail all, also the custom topics
	all := *id
	all.Detail = "all"
	set("h1", append(events, fakeEvent{Seq: 63, From: "a", Topic: "build.done", Msg: "its own topic"}))
	full := Feed(ctx, w, all, home, "s-1", FeedOptions{})
	if !strings.Contains(full, "#63 a → all (build.done): its own topic") {
		t.Errorf("detail all:\n%s", full)
	}
	if strings.Contains(full, "+1 other event") {
		t.Errorf("detail all still folds:\n%s", full)
	}
}

func TestFeedStartsCleanAndForgetsIdleSessions(t *testing.T) {
	ctx := context.Background()
	url, set := feedServer(t)
	w, id := feedWorld(t, url)
	home := t.TempDir()
	set("h1", []fakeEvent{{Seq: 3, From: "a", Topic: "msg", Msg: "while away"}})
	if got := Feed(ctx, w, *id, home, "s-1", FeedOptions{}); got != "" {
		t.Fatalf("the first look = %q", got)
	}
	// start: a fresh session begins from now whatever was shown in this file's name
	seen := filepath.Join(home, "seen", "s-1.json")
	if _, err := os.Stat(seen); err != nil {
		t.Fatalf("no seen file: %v", err)
	}
	if got := Feed(ctx, w, *id, home, "s-1", FeedOptions{Start: true}); got != "" {
		t.Fatalf("start = %q, want nothing", got)
	}
	set("h1", []fakeEvent{
		{Seq: 3, From: "a", Topic: "msg", Msg: "while away"},
		{Seq: 4, From: "a", Topic: "msg", Msg: "new now"},
	})
	if got := Feed(ctx, w, *id, home, "s-1", FeedOptions{}); !strings.Contains(got, "new now") || strings.Contains(got, "while away") {
		t.Fatalf("after start = %q", got)
	}
	// a session idle for a week is forgotten, its file and all
	idle := filepath.Join(home, "seen", "idle-x.json")
	writeFile(t, idle, `{"h1":9}`)
	past := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(idle, past, past); err != nil {
		t.Fatal(err)
	}
	Feed(ctx, w, *id, home, "fresh", FeedOptions{Start: true})
	if _, err := os.Stat(idle); !os.IsNotExist(err) {
		t.Error("an idle session's file was kept")
	}
}

func TestFeedStaysSilentWithoutAChannelASessionOrAWindow(t *testing.T) {
	ctx := context.Background()
	url, _ := feedServer(t)
	w, id := feedWorld(t, url)
	home := t.TempDir()
	noChannel := *id
	noChannel.Channel = ""
	if got := Feed(ctx, w, noChannel, home, "s-1", FeedOptions{}); got != "" {
		t.Errorf("no channel = %q", got)
	}
	noAs := *id
	noAs.As = ""
	if got := Feed(ctx, w, noAs, home, "s-1", FeedOptions{}); got != "" {
		t.Errorf("no session name = %q", got)
	}
	if got := Feed(ctx, w, *id, home, "s 1!", FeedOptions{}); got != "" {
		t.Errorf("no session id = %q", got)
	}
	// a server that never answers: one empty answer, quickly
	down := world(t, t.TempDir(), nil)
	if got := Feed(ctx, down, Identity{URL: "http://127.0.0.1:1", Channel: "c", As: "a", Push: []string{}, Listen: []string{}}, home, "s-1", FeedOptions{MS: 50}); got != "" {
		t.Errorf("unreachable = %q", got)
	}
}
