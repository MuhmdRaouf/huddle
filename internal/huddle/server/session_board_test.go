package server

// The board a session is pinned to: POST and GET /api/registry/sessions/<id>/board. The owner
// pins any session; a session pins itself with its own member credential; the registry's session
// row carries the pin.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// memberCredential joins the server as a member called name and answers its credential.
func memberCredential(t *testing.T, srv *childServer, name string) string {
	t.Helper()
	_, out := srv.post(t, "/api/tokens", `{}`, nil)
	var invite struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(out), &invite); err != nil || invite.Token == "" {
		t.Fatalf("make an invite: %s (%v)", out, err)
	}
	_, out = srv.post(t, "/api/join", `{"token":"`+invite.Token+`","name":"`+name+`"}`, map[string]string{"x-huddle-token": ""})
	var joined struct {
		Credential string `json:"credential"`
	}
	if err := json.Unmarshal([]byte(out), &joined); err != nil || joined.Credential == "" {
		t.Fatalf("join as %s: %s (%v)", name, out, err)
	}
	return joined.Credential
}

// reportMember lands a report for id that names the channel member it acts as.
func reportMember(t *testing.T, srv *childServer, id, member string) {
	t.Helper()
	body := fmt.Sprintf(`{"session_id":%q,"member":%q,"last_event":"SessionStart","last_event_at":%q}`,
		id, member, isoAgo(2*time.Second))
	if code, out := srv.post(t, "/api/registry/report", body, nil); code != 200 {
		t.Fatalf("report %s: %d %s", id, code, out)
	}
}

func boardOf(t *testing.T, out string) string {
	t.Helper()
	board, _ := bodyMap(t, out)["board"].(string)
	return board
}

func TestTheOwnerPinsASessionAndTheRowCarriesIt(t *testing.T) {
	srv := startServer(t, nil)
	reportAt(t, srv, "sess-pin", t.TempDir(), "")
	code, out := srv.post(t, "/api/registry/sessions/sess-pin/board", `{"board":"login"}`, nil)
	if code != 200 || boardOf(t, out) != "login" {
		t.Fatalf("pin = %d %s, want 200 and the board", code, out)
	}
	code, _, out = srv.get(t, "/api/registry/sessions/sess-pin/board", nil)
	if code != 200 || boardOf(t, out) != "login" {
		t.Errorf("read the pin = %d %s, want login", code, out)
	}
	code, _, out = srv.get(t, "/api/registry/sessions/sess-pin", nil)
	if code != 200 || !strings.Contains(out, `"board":"login"`) {
		t.Errorf("the session row = %d %s, want it to carry board login", code, out)
	}
	if code, out = srv.post(t, "/api/registry/sessions/sess-pin/board", `{"board":""}`, nil); code != 200 || boardOf(t, out) != "" {
		t.Errorf("clear the pin = %d %s, want 200 and no board", code, out)
	}
}

func TestPinningAnUnknownSessionAnswers404(t *testing.T) {
	srv := startServer(t, nil)
	code, out := srv.post(t, "/api/registry/sessions/nobody/board", `{"board":"login"}`, nil)
	if code != http.StatusNotFound {
		t.Errorf("pin of an unknown session = %d %s, want 404", code, out)
	}
	if code, _, _ = srv.get(t, "/api/registry/sessions/nobody/board", nil); code != http.StatusNotFound {
		t.Errorf("read of an unknown session's pin = %d, want 404", code)
	}
}

func TestASessionPinsItselfAndNoOtherMember(t *testing.T) {
	srv := startServer(t, nil)
	reportMember(t, srv, "sess-mine", "worker")
	reportMember(t, srv, "sess-theirs", "other")
	cred := memberCredential(t, srv, "worker")
	as := map[string]string{"x-huddle-token": cred}

	if code, out := srv.post(t, "/api/registry/sessions/sess-mine/board", `{"board":"login"}`, as); code != 200 {
		t.Errorf("a member pinning its own session = %d %s, want 200", code, out)
	}
	code, out := srv.post(t, "/api/registry/sessions/sess-theirs/board", `{"board":"login"}`, as)
	if code != http.StatusForbidden || !strings.Contains(errorOf(t, out), "own session") {
		t.Errorf("a member pinning another session = %d %s, want 403 naming its own session", code, out)
	}
}

func TestAPinThatIsNoBoardNameIsRefused(t *testing.T) {
	srv := startServer(t, nil)
	reportAt(t, srv, "sess-pin", t.TempDir(), "")
	for _, bad := range []string{strings.Repeat("a", 65), "two\nlines"} {
		raw, _ := json.Marshal(map[string]string{"board": bad})
		if code, out := srv.post(t, "/api/registry/sessions/sess-pin/board", string(raw), nil); code != http.StatusBadRequest {
			t.Errorf("pin %q = %d %s, want 400", bad, code, out)
		}
	}
}
