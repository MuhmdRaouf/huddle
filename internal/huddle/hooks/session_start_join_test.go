package hooks

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// joinHub is a fake Huddle that answers the channel join op the way the real one does and
// counts the joins it saw. hang decides, per join number (1-based), how long that join is held
// before it is answered (0 answers at once); a join the client gave up on ends with it.
type joinHub struct {
	*httptest.Server
	mu    sync.Mutex
	joins int
}

func newJoinHub(t *testing.T, hang func(n int) time.Duration, status int, errText string) *joinHub {
	t.Helper()
	hub := &joinHub{}
	hub.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/op/join"):
			hub.mu.Lock()
			hub.joins++
			n := hub.joins
			hub.mu.Unlock()
			select {
			case <-time.After(hang(n)):
			case <-r.Context().Done():
				return
			}
			if status != 200 {
				w.WriteHeader(status)
				fmt.Fprintf(w, `{"error":%q}`, errText)
				return
			}
			fmt.Fprint(w, `{"result":{},"text":"joined"}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/timeline"):
			fmt.Fprint(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(hub.Close)
	return hub
}

func (h *joinHub) joinCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.joins
}

// joinFailureLine is the one line a session start leaves when it did not join.
func joinFailureLine(reason string) string {
	return "huddle: could not join homelab as proxbeam: " + reason +
		"; the session's Huddle bridge keeps trying, and /huddle:join gets you in by hand"
}

var joinEnv = func(url string) map[string]string {
	return map[string]string{"HUDDLE_URL": url, "HUDDLE_CHANNEL": "homelab", "HUDDLE_AS": "proxbeam"}
}

// TestSessionStartRetriesAJoinWithoutAutostart: the first join hangs past its try, the second is
// answered; with autostart off the hook still ends joined inside its 3 s budget.
func TestSessionStartRetriesAJoinWithoutAutostart(t *testing.T) {
	hub := newJoinHub(t, func(n int) time.Duration {
		if n == 1 {
			return 5 * time.Second
		}
		return 0
	}, 200, "")
	stdout, elapsed, _ := runHook(t, "session-start", `{"source":"startup","session_id":"s-retry"}`, joinEnv(hub.URL))
	if elapsed > 3500*time.Millisecond {
		t.Errorf("session-start took %s, want it inside its 3 s budget", elapsed)
	}
	if got := hub.joinCount(); got < 2 {
		t.Errorf("the hub saw %d joins, want a retry after the first one hung", got)
	}
	ctx := hookContext(t, stdout)
	if !strings.Contains(ctx, `You are in Huddle channel "homelab" as "proxbeam"`) {
		t.Errorf("context = %q, want the joined picture", ctx)
	}
	if strings.Contains(ctx, "could not join") {
		t.Errorf("context = %q, want no failure line after a join that landed", ctx)
	}
}

// TestSessionStartSaysWhyItCouldNotJoin: a hub that never answers costs the session start its
// 3 s budget at most and leaves exactly one line naming the reason.
func TestSessionStartSaysWhyItCouldNotJoin(t *testing.T) {
	stdout, elapsed, _ := runHook(t, "session-start", `{"source":"startup","session_id":"s-mute"}`, joinEnv(muteURL(t)))
	if elapsed > 3500*time.Millisecond {
		t.Errorf("session-start took %s, want it inside its 3 s budget", elapsed)
	}
	ctx := hookContext(t, stdout)
	if want := joinFailureLine("the hub did not answer in 3 s"); ctx != want {
		t.Errorf("context = %q, want exactly %q", ctx, want)
	}
}

// TestSessionStartNamesEachJoinFailure: every path that does not join leaves one line, with its
// own reason.
func TestSessionStartNamesEachJoinFailure(t *testing.T) {
	t.Run("a refused connection", func(t *testing.T) {
		stdout, elapsed, _ := runHook(t, "session-start", `{"source":"startup","session_id":"s-down"}`, joinEnv(downURL(t)))
		if elapsed > 3500*time.Millisecond {
			t.Errorf("session-start took %s, want it inside its 3 s budget", elapsed)
		}
		if ctx, want := hookContext(t, stdout), joinFailureLine("the hub did not answer in 3 s"); ctx != want {
			t.Errorf("context = %q, want exactly %q", ctx, want)
		}
	})
	t.Run("no hub address yet", func(t *testing.T) {
		env := joinEnv("")
		stdout, _, _ := runHook(t, "session-start", `{"source":"startup","session_id":"s-noaddr"}`, env)
		if ctx, want := hookContext(t, stdout), joinFailureLine("no hub address yet"); ctx != want {
			t.Errorf("context = %q, want exactly %q", ctx, want)
		}
	})
	t.Run("a refusal with its status and text", func(t *testing.T) {
		hub := newJoinHub(t, func(int) time.Duration { return 0 }, 403, "channel homelab admits lead")
		stdout, _, _ := runHook(t, "session-start", `{"source":"startup","session_id":"s-403"}`, joinEnv(hub.URL))
		if ctx, want := hookContext(t, stdout), joinFailureLine("403 channel homelab admits lead"); ctx != want {
			t.Errorf("context = %q, want exactly %q", ctx, want)
		}
		if got := hub.joinCount(); got != 1 {
			t.Errorf("the hub saw %d joins, want one: a refusal is an answer, not a try to repeat", got)
		}
	})
}
