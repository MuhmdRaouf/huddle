package server

// The mute and forget routes: the owner posts one rule to /api/insights/mute and its tips leave
// every insights answer for 30 days, and posts /api/insights/forget to delete insights.json, the
// only learning Insights keeps. Both take the owner's credential and the POST method alone.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// seedAllowedAsks records allowed asks of one go-test command, each raised one hour further back
// and answered two minutes later, so always_allowed has a group to fire on.
func seedAllowedAsks(t *testing.T, srv *childServer, count int) {
	t.Helper()
	reg, err := hub.Open(context.Background(), home.Home{Root: srv.home}.HubDB())
	if err != nil {
		t.Fatalf("open the registry: %v", err)
	}
	defer reg.Close()
	now := time.Now()
	for i := 0; i < count; i++ {
		raised := now.Add(-time.Duration(count+1-i) * time.Hour)
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
}

// seedLongWait records one Bash ask raised 45 minutes ago and withdrawn just now, the way a
// terminal answering its own prompt first leaves one: a decided item whose wait lets
// longest_wait fire. The registry refuses a decision on an ask older than its ten-minute life,
// so the withdrawal is the one way history holds a wait this long.
func seedLongWait(t *testing.T, srv *childServer) {
	t.Helper()
	reg, err := hub.Open(context.Background(), home.Home{Root: srv.home}.HubDB())
	if err != nil {
		t.Fatalf("open the registry: %v", err)
	}
	defer reg.Close()
	if _, err := reg.CreatePermissionItem(context.Background(), "s-seed", "Bash",
		[]byte(`{"command":"go build ./..."}`), time.Now().Add(-45*time.Minute)); err != nil {
		t.Fatalf("create the ask: %v", err)
	}
	if _, err := reg.WithdrawPermissions(context.Background(), "s-seed", jsscalar.ISOTime(time.Now())); err != nil {
		t.Fatalf("withdraw the ask: %v", err)
	}
}

// rulesOf answers the rule of each tip an insights answer carries.
func rulesOf(t *testing.T, out string) []string {
	t.Helper()
	raw, _ := bodyMap(t, out)["tips"].([]any)
	rules := make([]string, 0, len(raw))
	for _, tip := range raw {
		rule, _ := tip.(map[string]any)["rule"].(string)
		rules = append(rules, rule)
	}
	return rules
}

func TestTheInsightsMuteRouteHidesOneRulesTips(t *testing.T) {
	srv := startServer(t, nil)
	seedAllowedAsks(t, srv, 5)
	seedLongWait(t, srv)

	if code, _, out := srv.get(t, "/api/insights", nil); code != 200 {
		t.Fatalf("insights = %d %s", code, out)
	} else if got := rulesOf(t, out); len(got) != 2 {
		t.Fatalf("tips = %v, want the always_allowed and longest_wait tips before the mute", got)
	}

	if code, out := srv.post(t, "/api/insights/mute", `{"rule":"always_allowed"}`, nil); code != 200 {
		t.Fatalf("mute = %d %s, want 200", code, out)
	} else if out != `{"muted":["always_allowed"]}` {
		t.Errorf("mute answers %s, want the rule named", out)
	}

	if code, _, out := srv.get(t, "/api/insights", nil); code != 200 {
		t.Fatalf("insights = %d %s", code, out)
	} else if got := rulesOf(t, out); len(got) != 1 || got[0] != "longest_wait" {
		t.Errorf("tips = %v, want the longest_wait tip alone after the mute", got)
	}
}

func TestTheInsightsMuteRouteRecordsTheMute(t *testing.T) {
	srv := startServer(t, nil)

	if code, out := srv.post(t, "/api/insights/mute", `{"rule":"always_allowed"}`, nil); code != 200 {
		t.Fatalf("mute = %d %s, want 200", code, out)
	}
	info, err := os.Stat(filepath.Join(srv.home, "insights.json"))
	if err != nil {
		t.Fatalf("the switches' file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", info.Mode().Perm())
	}
	if _, _, out := srv.get(t, "/api/insights/settings", nil); out != `{"tips_for_me":true,"tips_to_sessions":false}` {
		t.Errorf("settings = %s, want the switches untouched by the mute", out)
	}
}

func TestTheInsightsMuteRouteRefusesABadRule(t *testing.T) {
	srv := startServer(t, nil)
	for _, body := range []string{`{"rule":3}`, `{"rule":"Bad Rule"}`, `{}`} {
		if code, out := srv.post(t, "/api/insights/mute", body, nil); code != 400 {
			t.Fatalf("mute %s = %d %s, want 400", body, code, out)
		} else if msg, _ := bodyMap(t, out)["error"].(string); !strings.Contains(msg, "rule: the rule to mute, like same_command") {
			t.Errorf("mute %s names %q, want the rule named", body, msg)
		}
	}
}

func TestTheInsightsForgetRouteDeletesTheLearning(t *testing.T) {
	srv := startServer(t, nil)
	seedAllowedAsks(t, srv, 5)

	if code, out := srv.post(t, "/api/insights/mute", `{"rule":"always_allowed"}`, nil); code != 200 {
		t.Fatalf("mute = %d %s, want 200", code, out)
	}
	if _, _, out := srv.get(t, "/api/insights", nil); out != `{"tips":[]}` {
		t.Fatalf("insights answers %s muted, want {\"tips\":[]}", out)
	}

	if code, out := srv.post(t, "/api/insights/forget", "", nil); code != 200 {
		t.Fatalf("forget = %d %s, want 200", code, out)
	} else if out != `{"tips_for_me":true,"tips_to_sessions":false}` {
		t.Errorf("forget answers %s, want the defaults named", out)
	}
	if _, err := os.Stat(filepath.Join(srv.home, "insights.json")); !os.IsNotExist(err) {
		t.Errorf("the learning's file = %v, want it gone", err)
	}
	if _, _, out := srv.get(t, "/api/insights", nil); out == `{"tips":[]}` {
		t.Errorf("insights answers %s after the forget, want the tip back", out)
	}

	if code, out := srv.post(t, "/api/insights/forget", "", nil); code != 200 {
		t.Fatalf("a second forget = %d %s, want 200", code, out)
	} else if out != `{"tips_for_me":true,"tips_to_sessions":false}` {
		t.Errorf("a second forget answers %s, want the defaults named", out)
	}
}

func TestTheInsightsMuteAndForgetRoutesNeedTheOwner(t *testing.T) {
	srv := startServer(t, nil)

	without := map[string]string{"x-huddle-token": ""}
	if code, _ := srv.post(t, "/api/insights/mute", `{"rule":"always_allowed"}`, without); code != 401 {
		t.Errorf("an uncredentialed mute = %d, want 401", code)
	}
	if code, _ := srv.post(t, "/api/insights/forget", "", without); code != 401 {
		t.Errorf("an uncredentialed forget = %d, want 401", code)
	}
	if code, _, _ := srv.get(t, "/api/insights/mute", nil); code != 404 {
		t.Errorf("a GET on the mute route = %d, want 404: the routes take a POST alone", code)
	}
	if code, _, _ := srv.get(t, "/api/insights/forget", nil); code != 404 {
		t.Errorf("a GET on the forget route = %d, want 404: the routes take a POST alone", code)
	}
}
