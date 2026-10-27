package server

// The lane tip on the owner's insights (SPEC 9.4): a lane whose requests kept failing over the
// last day names itself on the owner's card alone — a session's answer carries no lane tip, and
// the tips-for-me switch off empties the owner's answer.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

func TestTheOwnersInsightsNameAFailingLane(t *testing.T) {
	bdd.Feature(t, "The owner's insights name a failing lane", func(f *bdd.F) {
		f.Scenario("50 kimi requests in a day, 6 of them failed", func(s *bdd.S) {
			var srv *childServer
			s.Given("a hub whose radar holds a day of kimi requests, 6 failed", func(t *testing.T) {
				srv = startServer(s.T, nil) // the rig outlives the steps: it starts under the scenario's own T
				spoolLane(t, srv, 44, "200", 0)
				spoolLane(t, srv, 5, "429", 1)
				spoolLane(t, srv, 1, "503", 2)
			})
			s.Then("the owner's insights carry the lane tip", func(t *testing.T) {
				var tip map[string]any
				var last string
				if !until(20*time.Second, func() bool {
					code, _, body := srv.get(t, "/api/insights", nil)
					if code != 200 {
						return false
					}
					last = body
					tip = laneTip(t, body)
					return tip != nil
				}) {
					t.Fatalf("the owner's insights carry no lane tip, last answer: %s", last)
				}
				day := time.Now().UTC().Format("2006-01-02")
				if tip["id"] != "lane_failing:Moonshot:"+day {
					t.Errorf("the tip id = %v, want lane_failing:Moonshot:%s", tip["id"], day)
				}
				if tip["rule"] != "lane_failing" {
					t.Errorf("the tip rule = %v, want lane_failing", tip["rule"])
				}
				want := "The Moonshot lane failed 12% of 50 requests in 24 h, mostly 429."
				if tip["sentence"] != want {
					t.Errorf("the tip sentence = %v, want %s", tip["sentence"], want)
				}
				if count, _ := tip["count"].(float64); int(count) != 6 {
					t.Errorf("the tip count = %v, want 6", tip["count"])
				}
			})
			s.And("a session's insights carry no lane tip", func(t *testing.T) {
				code, _, body := srv.get(t, "/api/insights?session=claude-lanes", nil)
				if code != 200 {
					t.Fatalf("the session's insights = %d %s, want 200", code, body)
				}
				if tip := laneTip(t, body); tip != nil {
					t.Errorf("the session's insights carry %v, want no lane tip", tip)
				}
			})
			s.And("with tips for me off, the owner's answer is empty", func(t *testing.T) {
				if code, out := putJSON(t, srv, "/api/insights/settings", `{"tips_for_me":false}`); code != 200 {
					t.Fatalf("the switch = %d %s, want 200", code, out)
				}
				code, _, body := srv.get(t, "/api/insights", nil)
				if code != 200 || strings.TrimSpace(body) != `{"tips":[]}` {
					t.Fatalf("the owner's insights = %d %s, want 200 {\"tips\":[]}", code, body)
				}
			})
		})
	})
}

// laneTip is the answer's lane_failing tip, nil when it carries none.
func laneTip(t *testing.T, body string) map[string]any {
	t.Helper()
	tips, _ := bodyMap(t, body)["tips"].([]any)
	for _, anyTip := range tips {
		if tip, ok := anyTip.(map[string]any); ok && tip["rule"] == "lane_failing" {
			return tip
		}
	}
	return nil
}

// spoolLane appends count router route lines for kimi model calls with the given status to the
// child's radar spool, the spool's own shape, so the tab the server runs ingests them as
// requests a lane served. The minute staggers the groups inside the last hour, inside the day.
func spoolLane(t *testing.T, srv *childServer, count int, status string, minute int) {
	t.Helper()
	path := filepath.Join(srv.radarHome, "spool", time.Now().UTC().Format("2006-01-02")+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("make the spool dir: %v", err)
	}
	var lines strings.Builder
	for i := 0; i < count; i++ {
		ts := time.Now().Add(-time.Duration(minute)*time.Minute - time.Duration(i)*time.Second)
		fmt.Fprintf(&lines, `{"ts":%q,"event":"route","session_id":"claude-lanes","agent_id":"main",`+
			`"model":"kimi-k2-coding","upstream":"https://api.moonshot.ai","plugin":"kimi","route":"direct",`+
			`"status":%s,"latency_ms":900,"usage":{"input_tokens":50,"output_tokens":20}}`+"\n",
			ts.UTC().Format("2006-01-02T15:04:05.000Z"), status)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open the spool file: %v", err)
	}
	defer file.Close()
	if _, err := file.WriteString(lines.String()); err != nil {
		t.Fatalf("write the spool file: %v", err)
	}
}
