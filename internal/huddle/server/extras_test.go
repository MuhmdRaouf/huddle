package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The owner's extras over HTTP: the digest route and tool, and Radar's own view of the channel
// (absent, its parts are missing without an error; running, its alerts and costs land).
func TestExtrasOverHTTP(t *testing.T) {
	srv := startServer(t, nil)
	root := map[string]string{"x-huddle-token": srv.token}
	for _, n := range []string{"api", "web"} {
		if code, out := srv.op(t, "shop", "join", n, fmt.Sprintf(`{"role": %q, "claude_session": "claude-%s"}`, n, n)); code != 200 {
			t.Fatalf("join %s = %d %s", n, code, out)
		}
	}

	t.Run("the digest over HTTP, the MCP tool and the CLI", func(t *testing.T) {
		// two approval requests, so the digest's totals carry them
		for _, command := range []string{"git push --force origin main", "git push -f origin next"} {
			code, out := srv.post(t, "/api/c/shop/x/approval?as=api", fmt.Sprintf(`{"command": %q}`, command), root)
			if code != 200 {
				t.Fatalf("approval %s = %d %s", command, code, out)
			}
		}
		_, _, digest := srv.get(t, "/api/c/shop/x/digest?since=1h", root)
		var parsed struct {
			Totals struct {
				Approvals float64 `json:"approvals"`
			} `json:"totals"`
			Sessions []struct {
				Name string `json:"name"`
			} `json:"sessions"`
		}
		if err := json.Unmarshal([]byte(digest), &parsed); err != nil {
			t.Fatalf("digest = %s", digest)
		}
		if parsed.Totals.Approvals < 2 {
			t.Fatalf("digest approvals = %v, want at least 2 (%s)", parsed.Totals.Approvals, digest)
		}
		found := false
		for _, s := range parsed.Sessions {
			if s.Name == "api" {
				found = true
			}
		}
		if !found {
			t.Fatalf("digest sessions = %s, want api among them", digest)
		}
		mcp := srv.rpc(t, "shop", "api", "tools/call", `{"name": "digest", "arguments": {"since": "2h"}}`, 1)
		result, _ := mcp["result"].(map[string]any)
		content, _ := result["content"].([]any)
		first, _ := content[0].(map[string]any)
		if !strings.Contains(fmt.Sprint(first["text"]), "digest: shop · since") {
			t.Fatalf("the digest tool = %v", mcp)
		}
		if code, _, _ := srv.get(t, "/api/c/shop/x/digest?since=bogus", root); code != 400 {
			t.Fatalf("a bogus window = %d, want 400", code)
		}
	})

	t.Run("Radar with nothing recorded: its parts are quiet, no error", func(t *testing.T) {
		// radar rides inside this server; an empty radar home means no rows yet, never an error
		code, _, body := srv.get(t, "/api/c/shop/x/radar", root)
		if code != 200 || strings.TrimSpace(body) != `{"available":true,"range":"day","alerts":[],"cost":{},"total":0}` {
			t.Fatalf("radar = %d %s, want the quiet available answer", code, body)
		}
		_, _, digest := srv.get(t, "/api/c/shop/x/digest", root)
		if !strings.Contains(digest, `"cost":{"available":true,"total":0,"range":"day"}`) {
			t.Fatalf("digest cost = %s, want the quiet zero", digest)
		}
	})

	t.Run("Radar's cost per session, read in-process, in the digest too", func(t *testing.T) {
		// a fresh server, so nothing was cached while radar was empty
		watching := startServer(t, nil)
		watchingRoot := map[string]string{"x-huddle-token": watching.token}
		if code, out := watching.op(t, "shop", "join", "api", `{"claude_session": "claude-api"}`); code != 200 {
			t.Fatalf("join = %d %s", code, out)
		}
		if code, out := watching.op(t, "shop", "join", "web", `{"claude_session": "claude-web"}`); code != 200 {
			t.Fatalf("join = %d %s", code, out)
		}
		// two routers' route lines in radar's spool: the tab's ingest prices them, and the
		// channel view folds the cost onto huddle names through the session links
		ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
		lines := fmt.Sprintf(`{"ts":%q,"event":"route","session_id":"claude-api","agent_id":"main","model":"glm-5.3","upstream":"https://api.z.ai/api/anthropic","plugin":"zai","route":"direct","status":200,"latency_ms":900,"usage":{"input_tokens":5000,"output_tokens":500}}
{"ts":%q,"event":"route","session_id":"claude-web","agent_id":"main","model":"glm-5.3","upstream":"https://api.z.ai/api/anthropic","plugin":"zai","route":"direct","status":200,"latency_ms":700,"usage":{"input_tokens":1000,"output_tokens":100}}
`, ts, ts)
		spool := filepath.Join(watching.radarHome, "spool", time.Now().UTC().Format("2006-01-02")+".jsonl")
		if err := os.MkdirAll(filepath.Dir(spool), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(spool, []byte(lines), 0o600); err != nil {
			t.Fatal(err)
		}
		radar, digest := "", ""
		// the ingest runs beside every other package's tests: under -race on a loaded runner it
		// has taken past 10 s, so the wait is bounded by time, not by a count of polls
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
			time.Sleep(100 * time.Millisecond)
			var code int
			code, _, radar = watching.get(t, "/api/c/shop/x/radar", watchingRoot)
			if code != 200 || !strings.Contains(radar, `"api":`) || !strings.Contains(radar, `"web":`) {
				continue
			}
			_, _, digest = watching.get(t, "/api/c/shop/x/digest?since=24h", watchingRoot)
			if strings.Contains(digest, `"total":0`) {
				continue // the digest read raced the ingest; try again
			}
			break
		}
		for _, want := range []string{`"available":true`, `"api":`, `"web":`, `"total":`} {
			if !strings.Contains(radar, want) {
				t.Fatalf("radar misses %s: %s", want, radar)
			}
		}
		if !strings.Contains(digest, `"cost":{"available":true,"total":`) {
			t.Fatalf("digest cost = %s", digest)
		}
		if !strings.Contains(digest, `"cost":0.`) && !regexp.MustCompile(`"cost":0\.[0-9]+`).MatchString(digest) {
			t.Fatalf("the digest's sessions carry no cost: %s", digest)
		}
	})
}
