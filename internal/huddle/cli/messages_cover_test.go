package cli

// TakeMessages, the listen hook's second delivery route: what the owner sent waits for the
// session at one bounded POST, oldest first — and every way the answer can fail stays silent.

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// TestTakeMessagesBringsTheSessionsUnreadMessages pins the happy path: one POST to the
// session's messages route, the messages as they came, and a call with no deadline still
// bounded by the caller's count.
func TestTakeMessagesBringsTheSessionsUnreadMessages(t *testing.T) {
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		if call.Method == http.MethodPost && strings.HasSuffix(call.Path, "/api/sessions/s-9/messages") {
			return 200, `{"messages": ["first", "second"]}`
		}
		return 404, `{}`
	})
	w := world(t, t.TempDir(), map[string]string{"HUDDLE_TOKEN": "root"})
	got := TakeMessages(context.Background(), w, Identity{URL: fake.server.URL}, "s-9", 0)
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Errorf("TakeMessages = %v", got)
	}
}

// TestTakeMessagesStaysSilentWhenNothingAnswers pins the quiet paths: no url, no session, an
// unreachable huddle, a refused call and an answer that is not JSON all answer nothing.
func TestTakeMessagesStaysSilentWhenNothingAnswers(t *testing.T) {
	ctx := context.Background()
	w := world(t, t.TempDir(), map[string]string{"HUDDLE_TOKEN": "root"})
	if got := TakeMessages(ctx, w, Identity{}, "s-9", 500); got != nil {
		t.Errorf("no url = %v", got)
	}
	if got := TakeMessages(ctx, w, Identity{URL: "http://127.0.0.1:1"}, "", 500); got != nil {
		t.Errorf("no session = %v", got)
	}
	if got := TakeMessages(ctx, w, Identity{URL: "http://127.0.0.1:1"}, "s-9", 500); got != nil {
		t.Errorf("unreachable = %v", got)
	}
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		if call.Path == "/api/sessions/s-9/messages" {
			return http.StatusForbidden, `{"error": "no such session"}`
		}
		return 404, `{}`
	})
	if got := TakeMessages(ctx, w, Identity{URL: fake.server.URL}, "s-9", 1500); got != nil {
		t.Errorf("refused = %v", got)
	}
	odd := startFakeHuddle(t, func(call fakeCall) (int, string) {
		return 200, "not json at all"
	})
	if got := TakeMessages(ctx, w, Identity{URL: odd.server.URL}, "s-9", 1500); got != nil {
		t.Errorf("a garbage answer = %v", got)
	}
}
