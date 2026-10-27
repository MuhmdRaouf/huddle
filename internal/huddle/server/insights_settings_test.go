package server

// The Insights switches' route: the owner reads and records them at /api/insights/settings, a
// PUT names either or both, and tips for me off empties /api/insights before it reads anything.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// putJSON PUTs a JSON body at the path with the owner's credential and answers the status and body.
func putJSON(t *testing.T, srv *childServer, path, body string) (int, string) {
	t.Helper()
	code, _, out, err := srv.request(context.Background(), http.MethodPut, path, body, map[string]string{
		"content-type":   "application/json",
		"x-huddle-token": srv.token,
	})
	if err != nil {
		t.Fatalf("PUT %s: %v", path, err)
	}
	return code, out
}

func TestTheInsightsSettingsRouteAnswersTheDefaults(t *testing.T) {
	srv := startServer(t, nil)
	code, _, out := srv.get(t, "/api/insights/settings", nil)
	if code != 200 {
		t.Fatalf("settings = %d %s", code, out)
	}
	if out != `{"tips_for_me":true,"tips_to_sessions":false}` {
		t.Errorf("settings = %s, want the defaults named", out)
	}

	without := map[string]string{"x-huddle-token": ""}
	if code, _, _ = srv.get(t, "/api/insights/settings", without); code != 401 {
		t.Errorf("an uncredentialed read = %d, want 401", code)
	}
}

func TestTheInsightsSettingsRouteRecordsOneSwitch(t *testing.T) {
	srv := startServer(t, nil)

	code, out := putJSON(t, srv, "/api/insights/settings", `{"tips_to_sessions":true}`)
	if code != 200 {
		t.Fatalf("PUT = %d %s", code, out)
	}
	if out != `{"tips_for_me":true,"tips_to_sessions":true}` {
		t.Errorf("PUT answers %s, want the merge over the file's values", out)
	}
	if _, _, out = srv.get(t, "/api/insights/settings", nil); out != `{"tips_for_me":true,"tips_to_sessions":true}` {
		t.Errorf("a second read answers %s, want the switch kept", out)
	}

	info, err := os.Stat(filepath.Join(srv.home, "insights.json"))
	if err != nil {
		t.Fatalf("the switches' file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestTheInsightsSettingsRouteRefusesAFieldThatIsNotABoolean(t *testing.T) {
	srv := startServer(t, nil)
	code, out := putJSON(t, srv, "/api/insights/settings", `{"tips_for_me":"yes"}`)
	if code != 400 {
		t.Fatalf(`PUT {"tips_for_me":"yes"} = %d %s, want 400`, code, out)
	}
	if msg, _ := bodyMap(t, out)["error"].(string); !strings.Contains(msg, "tips_for_me") {
		t.Errorf("error = %q, want the field named", msg)
	}
}

func TestTipsForMeOffHidesTheTips(t *testing.T) {
	srv := startServer(t, nil)

	// five allowed asks of one command: the one tip /api/insights names while tips for me is on
	reg, err := hub.Open(context.Background(), home.Home{Root: srv.home}.HubDB())
	if err != nil {
		t.Fatalf("open the registry: %v", err)
	}
	now := time.Now()
	for i := 0; i < 5; i++ {
		raised := now.Add(-time.Duration(5-i) * time.Hour)
		id, err := reg.CreatePermissionItem(context.Background(), "s-seed", "Bash",
			[]byte(`{"command":"go test ./internal/kanban/..."}`), raised)
		if err != nil {
			t.Fatalf("create the ask: %v", err)
		}
		decided := raised.Add(2 * time.Minute)
		if err := reg.TouchPoll(context.Background(), id, jsscalar.ISOTime(decided)); err != nil {
			t.Fatalf("poll the ask: %v", err)
		}
		if won, err := reg.DecidePermission(context.Background(), id, "allow", nil, "", jsscalar.ISOTime(decided), decided); err != nil || !won {
			t.Fatalf("decide the ask = (%v, %v), want the decision to win", won, err)
		}
	}
	reg.Close()

	if code, _, out := srv.get(t, "/api/insights", nil); code != 200 {
		t.Fatalf("insights = %d %s", code, out)
	} else if tips, _ := bodyMap(t, out)["tips"].([]any); len(tips) != 1 {
		t.Fatalf("tips = %v, want the one always_allowed tip before the switch turns", tips)
	}

	if code, out := putJSON(t, srv, "/api/insights/settings", `{"tips_for_me":false}`); code != 200 {
		t.Fatalf("PUT = %d %s", code, out)
	}
	if _, _, out := srv.get(t, "/api/insights", nil); out != `{"tips":[]}` {
		t.Errorf("insights answers %s with tips for me off, want {\"tips\":[]}", out)
	}
}
