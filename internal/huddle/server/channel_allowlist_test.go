package server

// The allowlist over HTTP: a channel created with members refuses a stranger's join with 403,
// and one created with an empty members array admits every signed-in session.

import (
	"net/http"
	"strings"
	"testing"
)

// TestCreateChannelWithMembersRefusesAStranger pins the create-and-join path: POST /api/channels
// stores the members it is given, an op/join off the list is refused with the members named, and
// a channel created with members [] admits anyone.
func TestCreateChannelWithMembersRefusesAStranger(t *testing.T) {
	srv := startServer(t, nil)

	if code, out := srv.post(t, "/api/channels", `{"name": "gated", "members": ["gatekeeper"]}`, nil); code != http.StatusOK {
		t.Fatalf("create the gated channel = %d %s", code, out)
	}
	code, out := srv.op(t, "gated", "join", "stranger", `{}`)
	if code != http.StatusForbidden {
		t.Fatalf("a stranger's join = %d %s, want 403", code, out)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "channel gated admits gatekeeper (the owner adds members)") {
		t.Fatalf("the refusal %s does not name the members", out)
	}

	if code, out := srv.post(t, "/api/channels", `{"name": "openroad", "members": []}`, nil); code != http.StatusOK {
		t.Fatalf("create the open channel = %d %s", code, out)
	}
	if code, out := srv.op(t, "openroad", "join", "anyone", `{}`); code != http.StatusOK {
		t.Fatalf("a join with members [] = %d %s, want 200", code, out)
	}
}
