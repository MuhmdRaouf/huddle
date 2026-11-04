package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// groupCopies is c's message to a and b as the timeline holds it: one copy each, tied by group 8.
func groupCopies() []feedEvent {
	data := json.RawMessage(`{"to":["a","b"],"group":8}`)
	return []feedEvent{
		{Seq: 8, From: "c", To: "a", Topic: "msg", Msg: "stand by", Data: data},
		{Seq: 9, From: "c", To: "b", Topic: "msg", Msg: "stand by", Data: data},
	}
}

func TestTheFeedShowsAGroupMessageOnceToASessionItWasNotAddressedTo(t *testing.T) {
	id := Identity{URL: "http://127.0.0.1:1", Channel: "h1", As: "d", Push: []string{}, Listen: []string{}, Detail: "digest"}
	got := channelPart(id, "h1", groupCopies(), 7)
	if n := strings.Count(got, "stand by"); n != 1 {
		t.Errorf("the feed of d shows the message %d times, want 1:\n%s", n, got)
	}
	if !strings.Contains(got, "#8 c → a (msg)") {
		t.Errorf("the feed of d misses the first copy:\n%s", got)
	}
	id.As = "b"
	if got := channelPart(id, "h1", groupCopies(), 7); !strings.Contains(got, "#9 c → b (msg): stand by") {
		t.Errorf("the feed of b misses its own copy:\n%s", got)
	}
}

func TestListenPrintsAGroupMessageOnceToASessionItWasNotAddressedTo(t *testing.T) {
	data := `"data":{"to":["x","y"],"group":1}`
	s := startStreamServer(t, func(w http.ResponseWriter, r *http.Request) {
		sseWrites(w,
			`data: {"type":"event","data":{"seq":1,"from":"c","to":"x","topic":"msg","msg":"stand by",`+data+"}}\n\n",
			`data: {"type":"event","data":{"seq":2,"from":"c","to":"y","topic":"msg","msg":"stand by",`+data+"}}\n\n")
		<-r.Context().Done()
	}, quietTimeline)
	w := huddleWorld(t, s.server.URL, map[string]string{"HUDDLE_LISTEN_QUIET_MS": "300"})
	code, out, errOut := runListen(t, w, 1500*time.Millisecond)
	if code != 0 || errOut != "" {
		t.Fatalf("listen = %d %q %q", code, out, errOut)
	}
	if n := strings.Count(out, "stand by"); n != 1 {
		t.Errorf("listen printed the message %d times, want 1:\n%s", n, out)
	}
}

func TestTheBridgePushesAGroupMessageOnceToASessionItWasNotAddressedTo(t *testing.T) {
	var out strings.Builder
	b := &mcpBridge{stdout: &out, channelName: "c", me: "d", pushed: map[int64]bool{}}
	for _, ev := range groupCopies() {
		raw, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		event, err := jsfmt.DecodeValue(raw)
		if err != nil {
			t.Fatal(err)
		}
		b.deliver(event, true)
	}
	if n := strings.Count(out.String(), "stand by"); n != 1 {
		t.Errorf("the bridge pushed the message %d times, want 1:\n%s", n, out.String())
	}
}
