package cli

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTheFeedLeavesOutTheOwnersMessageToAnotherSession(t *testing.T) {
	events := []feedEvent{
		{Seq: 7, From: "owner", To: "lead", Topic: "msg", Msg: "Pick the second option"},
		{Seq: 8, From: "lead", To: "dotfiles", Topic: "msg", Msg: "done"},
	}
	t.Run("another session prints none of it and counts none of it", func(t *testing.T) {
		id := Identity{URL: "http://127.0.0.1:1", Channel: "h1", As: "dotfiles",
			Push: []string{}, Listen: []string{}, Detail: "digest"}
		got := channelPart(id, "h1", events, 6)
		if strings.Contains(got, "Pick the second option") {
			t.Fatalf("dotfiles's feed shows the owner's message to lead:\n%s", got)
		}
		if strings.Contains(got, "+1") || strings.Contains(got, "other event") {
			t.Fatalf("dotfiles's feed counts the owner's message:\n%s", got)
		}
		if !strings.Contains(got, "done") {
			t.Fatalf("dotfiles's feed lost lead's overheard message:\n%s", got)
		}
	})
	t.Run("its addressee prints it in full", func(t *testing.T) {
		id := Identity{URL: "http://127.0.0.1:1", Channel: "h1", As: "lead",
			Push: []string{}, Listen: []string{}, Detail: "digest"}
		got := channelPart(id, "h1", events, 6)
		if !strings.Contains(got, "Pick the second option") {
			t.Fatalf("lead's feed lost the owner's message:\n%s", got)
		}
	})
}

// huddle listen, the bash agent's follow, prints no owner's message to another member and keeps
// the one addressed to it.
func TestListenLeavesOutTheOwnersMessageToAnotherSession(t *testing.T) {
	s := startStreamServer(t, func(w http.ResponseWriter, r *http.Request) {
		sseWrites(w,
			"data: {\"type\":\"event\",\"data\":{\"seq\":1,\"from\":\"owner\",\"to\":\"lead\",\"topic\":\"msg\",\"msg\":\"Pick the second option\"}}\n\n",
			"data: {\"type\":\"event\",\"data\":{\"seq\":2,\"from\":\"owner\",\"to\":\"a\",\"topic\":\"msg\",\"msg\":\"yours\"}}\n\n",
			"data: {\"type\":\"event\",\"data\":{\"seq\":3,\"from\":\"lead\",\"to\":\"dotfiles\",\"topic\":\"msg\",\"msg\":\"done\"}}\n\n")
		<-r.Context().Done()
	}, quietTimeline)
	w := huddleWorld(t, s.server.URL, map[string]string{"HUDDLE_LISTEN_QUIET_MS": "300"})
	code, out, errOut := runListen(t, w, 1500*time.Millisecond)
	if code != 0 || errOut != "" {
		t.Fatalf("listen = %d %q %q", code, out, errOut)
	}
	if strings.Contains(out, "Pick the second option") {
		t.Errorf("listen printed the owner's message to lead:\n%s", out)
	}
	if !strings.Contains(out, "yours") || !strings.Contains(out, "done") {
		t.Errorf("listen printed %q, want the owner's message to a and lead's overheard one", out)
	}
}
