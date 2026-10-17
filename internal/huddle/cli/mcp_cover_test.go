package cli

// The bridge's quiet edges: the topic globs it pushes by, the errors that mean a deadline, the
// waits, the answer shapes it prints, and the push ack's silent paths.

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// TestTheBridgePushesOnlyTopicsItsGlobsName pins the topic filter: push off pushes nothing, a
// glob pushes what it names, and an unnamed topic stays with the session.
func TestTheBridgePushesOnlyTopicsItsGlobsName(t *testing.T) {
	off := &mcpBridge{pushOff: true}
	if off.matches("msg") {
		t.Error("push off still matched")
	}
	globs := &mcpBridge{pushGlobs: []*regexp.Regexp{
		regexp.MustCompile("^(?:" + mcpGlobExpr("msg.*") + ")$"),
		regexp.MustCompile("^(?:" + mcpGlobExpr("ask") + ")$"),
	}}
	if !globs.matches("msg.turn") || !globs.matches("ask") {
		t.Error("a named topic did not match")
	}
	if globs.matches("control.ask") {
		t.Error("an unnamed topic matched")
	}
	if mcpGlobExpr("a.b*c(d)[e]") != `a\.b.*c\(d\)\[e\]` {
		t.Errorf("mcpGlobExpr = %q", mcpGlobExpr("a.b*c(d)[e]"))
	}
}

// TestErrorsThatMeanADeadline pins the timeout reader: a url error over a deadline and a bare
// context deadline both read as deadlines, a plain error does not.
func TestErrorsThatMeanADeadline(t *testing.T) {
	if !mcpIsTimeout(&url.Error{Op: "Post", URL: "http://127.0.0.1:1", Err: context.DeadlineExceeded}) {
		t.Error("a url error over a deadline")
	}
	if !mcpIsTimeout(context.DeadlineExceeded) {
		t.Error("a bare deadline")
	}
	if mcpIsTimeout(errors.New("connection refused")) {
		t.Error("a plain error")
	}
}

// TestMcpSleepEndsWhenTheSessionDoes pins the bridge's wait: an ended session says so at once,
// a live one lets the timer fire.
func TestMcpSleepEndsWhenTheSessionDoes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if mcpSleep(ctx, time.Hour) {
		t.Error("an ended session slept on")
	}
	if !mcpSleep(context.Background(), time.Millisecond) {
		t.Error("a live session ended the wait early")
	}
}

// TestMcpStringOfPrintsEveryShape pins the name printer: a string as it is, a number as
// JavaScript prints it, a boolean as its word, anything else as its JSON.
func TestMcpStringOfPrintsEveryShape(t *testing.T) {
	if got := mcpStringOf(nil); got != "" {
		t.Errorf("mcpStringOf(nil) = %q", got)
	}
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{`"phone"`, "phone"},
		{`5`, "5"},
		{`5.5`, "5.5"},
		{`true`, "true"},
		{`{"a":1}`, `{"a":1}`},
	} {
		value, err := jsfmt.DecodeValue([]byte(tc.raw))
		if err != nil {
			t.Fatal(err)
		}
		if got := mcpStringOf(value); got != tc.want {
			t.Errorf("mcpStringOf(%s) = %q, want %q", tc.raw, got, tc.want)
		}
	}
	if got := mcpNumberOf(nil); got != 0 {
		t.Errorf("mcpNumberOf(nil) = %v", got)
	}
}

// TestOpListAnswersTheResultListOrSaysWhy pins the bridge's operation calls: the result's list
// when there is one, and the three ways there is none.
func TestOpListAnswersTheResultListOrSaysWhy(t *testing.T) {
	w := world(t, t.TempDir(), map[string]string{"HUDDLE_TOKEN": "root"})
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		switch {
		case strings.HasSuffix(call.Path, "/op/inbox"):
			return 200, `{"result": [{"seq": 1}, {"seq": 2}]}`
		case strings.HasSuffix(call.Path, "/op/events"):
			return 200, "not json at all"
		case strings.HasSuffix(call.Path, "/op/map"):
			return 200, `{"result": {"not": "a list"}}`
		default:
			return 404, `{}`
		}
	})
	b := &mcpBridge{w: w, url: fake.server.URL, channelName: "c", me: "a", callMS: 1000}
	items, err := b.opList(context.Background(), "inbox", jsfmt.Object(nil, nil), 1000)
	if err != nil || len(items) != 2 {
		t.Errorf("opList(inbox) = (%d items, %v)", len(items), err)
	}
	if _, err := b.opList(context.Background(), "events", jsfmt.Object(nil, nil), 1000); err == nil {
		t.Error("opList over garbage answered no error")
	}
	if _, err := b.opList(context.Background(), "map", jsfmt.Object(nil, nil), 1000); err == nil || !strings.Contains(err.Error(), "no result list") {
		t.Errorf("opList over an object result = %v", err)
	}
	gone := &mcpBridge{w: w, url: "http://127.0.0.1:1", channelName: "c", me: "a", callMS: 500}
	if _, err := gone.opList(context.Background(), "inbox", jsfmt.Object(nil, nil), 500); err == nil {
		t.Error("opList over an unreachable huddle answered no error")
	}
}

// TestTheBridgeAcksQuietlyOrNotAtAll pins the push ack's silent paths: a bridge with no
// identity acks nothing, and a registry that does not answer costs one log line.
func TestTheBridgeAcksQuietlyOrNotAtAll(t *testing.T) {
	anonymous := &mcpBridge{w: world(t, t.TempDir(), nil), stderr: &strings.Builder{}}
	anonymous.ack(context.Background())
	if anonymous.stderr.(*strings.Builder).Len() != 0 {
		t.Error("a bridge with no identity logged an ack")
	}
	var mu sync.Mutex
	acked := false
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		if call.Path == "/api/registry/push-ack" {
			mu.Lock()
			acked = true
			mu.Unlock()
			return 200, `{"ok": true}`
		}
		return 404, `{}`
	})
	loud := &mcpBridge{w: world(t, t.TempDir(), nil), url: fake.server.URL, channelName: "c", me: "a", stderr: &strings.Builder{}}
	loud.ack(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if !acked {
		t.Error("the ack never reached the registry")
	}
	gone := &mcpBridge{w: world(t, t.TempDir(), nil), url: "http://127.0.0.1:1", channelName: "c", me: "a", stderr: &strings.Builder{}}
	gone.ack(context.Background())
	if !strings.Contains(gone.stderr.(*strings.Builder).String(), "push ack") {
		t.Errorf("a failed ack said nothing: %q", gone.stderr.(*strings.Builder).String())
	}
}
