package server

// The owner's writes over Insights: the mute, the forget and the settings' PUT take the owner's
// credential alone, like the permission item's decision. A member's credential reads the tips
// and the switches and answers 403 on every write, with insights.json left as it was.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// insightsMemberPUT PUTs a JSON body at the path with the member's credential and answers the
// status and body.
func insightsMemberPUT(t *testing.T, srv *childServer, member, path, body string) (int, string) {
	t.Helper()
	code, _, out, err := srv.request(context.Background(), http.MethodPut, path, body, map[string]string{
		"content-type":   "application/json",
		"x-huddle-token": member,
	})
	if err != nil {
		t.Fatalf("PUT %s: %v", path, err)
	}
	return code, out
}

func TestOnlyTheOwnerWritesInsights(t *testing.T) {
	srv := startServer(t, nil)
	member := permitMember(t, srv)
	headers := map[string]string{"x-huddle-token": member}
	learning := filepath.Join(srv.home, "insights.json")

	// the member's mute is refused before anything is written: the learning's file stays absent
	if code, out := srv.post(t, "/api/insights/mute", `{"rule":"always_allowed"}`, headers); code != 403 {
		t.Fatalf("a member's mute = %d %s, want 403", code, out)
	} else if msg, _ := bodyMap(t, out)["error"].(string); !strings.Contains(msg, "only the owner changes Insights") {
		t.Errorf("a member's mute names %q, want the owner named", msg)
	}
	if _, err := os.Stat(learning); !os.IsNotExist(err) {
		t.Fatalf("the member's mute left the learning's file: %v", err)
	}

	// the member's forget is refused and the file keeps its bytes
	seeded := []byte(`{"tips_for_me":true,"tips_to_sessions":false,"note":"kept"}`)
	if err := os.WriteFile(learning, seeded, 0o600); err != nil {
		t.Fatalf("seed the learning: %v", err)
	}
	if code, out := srv.post(t, "/api/insights/forget", "", headers); code != 403 {
		t.Fatalf("a member's forget = %d %s, want 403", code, out)
	} else if msg, _ := bodyMap(t, out)["error"].(string); !strings.Contains(msg, "only the owner changes Insights") {
		t.Errorf("a member's forget names %q, want the owner named", msg)
	}
	kept, err := os.ReadFile(learning)
	if err != nil {
		t.Fatalf("read the learning: %v", err)
	}
	if string(kept) != string(seeded) {
		t.Fatalf("the learning's file = %s, want the bytes it held", kept)
	}

	// the member's settings PUT is refused and the switches answer as before
	if code, out := insightsMemberPUT(t, srv, member, "/api/insights/settings", `{"tips_for_me":false}`); code != 403 {
		t.Fatalf("a member's settings PUT = %d %s, want 403", code, out)
	} else if msg, _ := bodyMap(t, out)["error"].(string); !strings.Contains(msg, "only the owner changes Insights") {
		t.Errorf("a member's settings PUT names %q, want the owner named", msg)
	}
	if _, _, out := srv.get(t, "/api/insights/settings", nil); out != `{"tips_for_me":true,"tips_to_sessions":false}` {
		t.Fatalf("the switches answer %s after the member's PUT, want the file's values", out)
	}

	// the reads stay open to every credential
	if code, _, _ := srv.get(t, "/api/insights", headers); code != 200 {
		t.Errorf("a member's insights read = %d, want 200", code)
	}
	if code, _, _ := srv.get(t, "/api/insights/settings", headers); code != 200 {
		t.Errorf("a member's settings read = %d, want 200", code)
	}

	// the owner's writes stand
	if code, out := srv.post(t, "/api/insights/mute", `{"rule":"always_allowed"}`, nil); code != 200 {
		t.Errorf("the owner's mute = %d %s, want 200", code, out)
	}
	if code, out := putJSON(t, srv, "/api/insights/settings", `{"tips_to_sessions":true}`); code != 200 {
		t.Errorf("the owner's settings PUT = %d %s, want 200", code, out)
	}
	if code, out := srv.post(t, "/api/insights/forget", "", nil); code != 200 {
		t.Errorf("the owner's forget = %d %s, want 200", code, out)
	}
}
