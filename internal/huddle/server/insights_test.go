package server

// The insights route: today's tips read out of the registry's own history, the owner's
// credential only, and no tips where there is nothing to name.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

func TestTheInsightsRouteNamesAnAlwaysAllowedPrompt(t *testing.T) {
	srv := startServer(t, nil)

	// five Bash asks of one command over the last 7 days, each allowed two minutes after it
	// was raised: a wait the always_allowed rule names
	reg, err := hub.Open(context.Background(), home.Home{Root: srv.home}.HubDB())
	if err != nil {
		t.Fatalf("open the registry: %v", err)
	}
	defer reg.Close()
	now := time.Now()
	for i := 0; i < 5; i++ {
		raised := now.Add(-time.Duration(5-i) * time.Hour)
		id, err := reg.CreatePermissionItem(context.Background(), "s-seed", "Bash",
			[]byte(`{"command":"go test ./internal/kanban/..."}`), raised)
		if err != nil {
			t.Fatalf("create the ask: %v", err)
		}
		decided := raised.Add(2 * time.Minute)
		if err := reg.TouchPoll(context.Background(), id, jsfmt.ISOTime(decided)); err != nil {
			t.Fatalf("poll the ask: %v", err)
		}
		if won, err := reg.DecidePermission(context.Background(), id, "allow", "", jsfmt.ISOTime(decided), decided); err != nil || !won {
			t.Fatalf("decide the ask = (%v, %v), want the decision to win", won, err)
		}
	}

	code, _, out := srv.get(t, "/api/insights", nil)
	if code != 200 {
		t.Fatalf("insights = %d %s", code, out)
	}
	tips, _ := bodyMap(t, out)["tips"].([]any)
	if len(tips) != 1 {
		t.Fatalf("tips = %v, want the one always_allowed tip", tips)
	}
	tip, _ := tips[0].(map[string]any)
	sentence, _ := tip["sentence"].(string)
	if tip["rule"] != "always_allowed" || tip["count"] != float64(5) {
		t.Errorf("the tip = %v, want always_allowed over five asks", tip)
	}
	// the engine names a prompt by the rule its commands group under, the first two words
	if !strings.Contains(sentence, "Bash(go test:*)") || !strings.Contains(sentence, "5 times in 7 days") {
		t.Errorf("sentence = %q, want the go test rule named with its count", sentence)
	}

	// without a credential the route is refused the way /api/needs refuses it
	without := map[string]string{"x-huddle-token": ""}
	needsCode, _, _ := srv.get(t, "/api/needs", without)
	insightsCode, _, insightsBody := srv.get(t, "/api/insights", without)
	if insightsCode != 401 || needsCode != 401 {
		t.Errorf("an uncredentialed read = needs %d, insights %d %s, want both 401", needsCode, insightsCode, insightsBody)
	}
}

func TestTheInsightsRouteAnswersAnEmptyHubWithNoTips(t *testing.T) {
	srv := startServer(t, nil)
	code, _, out := srv.get(t, "/api/insights", nil)
	if code != 200 {
		t.Fatalf("insights = %d %s", code, out)
	}
	if out != `{"tips":[]}` {
		t.Errorf("an empty hub answers %s, want {\"tips\":[]}", out)
	}
}
