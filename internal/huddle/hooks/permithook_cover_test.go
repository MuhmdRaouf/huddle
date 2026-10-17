package hooks

// The permit hook's refusals: a create the huddle will not take, an item that vanishes between
// the create and the poll, a poll that answers garbage, and a denial that arrives without words.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// permitFake is one fake huddle for the permit hook: what it answers to the create and to the
// wait, switchable between runs.
type permitFake struct {
	create int64  // the status the POST /api/needs answers
	wait   int64  // the status the GET wait answers
	body   string // the wait answer's body
	server *httptest.Server
}

func startPermitFake(t *testing.T, create int64, wait int64, body string) *permitFake {
	t.Helper()
	fake := &permitFake{create: create, wait: wait, body: body}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			w.WriteHeader(int(atomic.LoadInt64(&fake.create)))
			fmt.Fprint(w, `{"id":7}`)
		default:
			w.WriteHeader(int(atomic.LoadInt64(&fake.wait)))
			fmt.Fprint(w, fake.body)
		}
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func TestThePermitHookStaysSilentWhenTheCreateIsRefused(t *testing.T) {
	fake := startPermitFake(t, 401, 200, `{"state":"open"}`)
	stdout, _, _ := runHook(t, "permit", permitInputJSON,
		map[string]string{"HUDDLE_URL": fake.server.URL})
	if stdout != "" {
		t.Errorf("the hook printed %q after a refused create, want silence", stdout)
	}
}

func TestThePermitHookStaysSilentWhenTheItemVanishesBeforeItsPoll(t *testing.T) {
	fake := startPermitFake(t, 200, 404, `{"state":"gone"}`)
	stdout, _, _ := runHook(t, "permit", permitInputJSON,
		map[string]string{"HUDDLE_URL": fake.server.URL})
	if stdout != "" {
		t.Errorf("the hook printed %q after a vanished item, want silence", stdout)
	}
}

func TestThePermitHookStaysSilentWhenItsPollAnswersGarbage(t *testing.T) {
	fake := startPermitFake(t, 200, 200, `{not json`)
	stdout, _, _ := runHook(t, "permit", permitInputJSON,
		map[string]string{"HUDDLE_URL": fake.server.URL})
	if stdout != "" {
		t.Errorf("the hook printed %q on a garbage poll, want silence", stdout)
	}
}

func TestThePermitHookPrintsTheDefaultDenialWhenNoneIsGiven(t *testing.T) {
	s := startHuddle(t)
	input := `{"session_id":"s-rep","tool_name":"Bash","tool_input":{"command":"npm test"}}`
	out := make(chan string, 1)
	go func() {
		stdout, _, _ := runHook(t, "permit", input, map[string]string{"HUDDLE_URL": s.base})
		out <- stdout
	}()
	id := needIDOf(t, s)
	if code, body := post(t, s, "/api/needs/"+id+"/decide", s.token, `{"decision":"deny"}`); code != 200 {
		t.Fatalf("the app's deny = %d %s", code, body)
	}
	select {
	case stdout := <-out:
		want := `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"Huddle: the owner denied this permission"}}}` + "\n"
		if stdout != want {
			t.Fatalf("the hook printed %q, want the default denial", stdout)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the hook never answered its poll")
	}
}
