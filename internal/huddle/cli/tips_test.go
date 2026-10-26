package cli

// SessionTips, the tips hook's one fetch: the tips the hub holds about one session, one bounded
// GET — and every way the answer can fail stays silent.

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestSessionTipsBringsTheSessionsTips(t *testing.T) {
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		if call.Method == http.MethodGet && call.Path == "/api/insights" && call.Query == "session=s-9" {
			return 200, `{"tips": [{"id": "t-1", "sentence": "one narrower run is faster"}, {"id": "t-2", "sentence": "an allow rule would skip the prompt"}]}`
		}
		return 404, `{}`
	})
	w := world(t, t.TempDir(), map[string]string{"HUDDLE_TOKEN": "root"})
	got := SessionTips(context.Background(), w, Identity{URL: fake.server.URL}, "s-9", 0)
	if len(got) != 2 || got[0].ID != "t-1" || got[0].Sentence != "one narrower run is faster" || got[1].ID != "t-2" {
		t.Errorf("SessionTips = %+v, want both tips", got)
	}
	if calls := fake.asked(); len(calls) != 1 || calls[0].Method != http.MethodGet || calls[0].Query != "session=s-9" {
		t.Errorf("the fake saw %+v, want one GET at session=s-9", calls)
	}
}

func TestSessionTipsStaysSilentWhenNothingAnswers(t *testing.T) {
	ctx := context.Background()
	w := world(t, t.TempDir(), map[string]string{"HUDDLE_TOKEN": "root"})
	if got := SessionTips(ctx, w, Identity{}, "s-9", 500); got != nil {
		t.Errorf("no url = %v, want none", got)
	}
	if got := SessionTips(ctx, w, Identity{URL: "http://127.0.0.1:1"}, "", 500); got != nil {
		t.Errorf("no session = %v, want none", got)
	}
	if got := SessionTips(ctx, w, Identity{URL: "http://127.0.0.1:1"}, "s-9", 500); got != nil {
		t.Errorf("an unreachable hub = %v, want none", got)
	}
	refusing := startFakeHuddle(t, func(call fakeCall) (int, string) {
		if call.Path == "/api/insights" {
			return http.StatusForbidden, `{"error": "no"}`
		}
		return 404, `{}`
	})
	if got := SessionTips(ctx, w, Identity{URL: refusing.server.URL}, "s-9", 1500); got != nil {
		t.Errorf("a refused ask = %v, want none", got)
	}
	garbled := startFakeHuddle(t, func(call fakeCall) (int, string) {
		return 200, "not json at all"
	})
	if got := SessionTips(ctx, w, Identity{URL: garbled.server.URL}, "s-9", 1500); got != nil {
		t.Errorf("a garbage answer = %v, want none", got)
	}
	empty := startFakeHuddle(t, func(call fakeCall) (int, string) {
		if strings.HasSuffix(call.Path, "/api/insights") {
			return 200, `{"tips": []}`
		}
		return 404, `{}`
	})
	if got := SessionTips(ctx, w, Identity{URL: empty.server.URL}, "s-9", 1500); len(got) != 0 {
		t.Errorf("no tips = %v, want none", got)
	}
}
