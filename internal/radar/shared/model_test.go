package shared

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

func TestAddTokens(t *testing.T) {
	t.Run("sums the four counters", func(t *testing.T) {
		a := Tokens{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4}
		b := Tokens{Input: 10, Output: 20, CacheRead: 30, CacheWrite: 40}
		want := Tokens{Input: 11, Output: 22, CacheRead: 33, CacheWrite: 44}
		if got := AddTokens(a, b); got != want {
			t.Errorf("AddTokens(%+v, %+v) = %+v, want %+v", a, b, got, want)
		}
	})

	t.Run("zero tokens change nothing", func(t *testing.T) {
		a := Tokens{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4}
		if got := AddTokens(a, ZeroTokens); got != a {
			t.Errorf("AddTokens(a, ZeroTokens) = %+v, want %+v", got, a)
		}
	})
}

func TestTotalTokens(t *testing.T) {
	t.Run("sums the four counters", func(t *testing.T) {
		if got := TotalTokens(Tokens{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4}); got != 10 {
			t.Errorf("TotalTokens() = %d, want 10", got)
		}
		if got := TotalTokens(ZeroTokens); got != 0 {
			t.Errorf("TotalTokens(ZeroTokens) = %d, want 0", got)
		}
	})
}

func TestPromptTitle(t *testing.T) {
	t.Run("folds whitespace to single spaces and trims", func(t *testing.T) {
		cases := []struct{ in, want string }{
			{"  write   the\n\ttests  ", "write the tests"},
			{"one line", "one line"},
			{"", ""},
		}
		for _, tt := range cases {
			if got := PromptTitle(tt.in); got != tt.want {
				t.Errorf("PromptTitle(%q) = %q, want %q", tt.in, got, tt.want)
			}
		}
	})

	t.Run("cuts at 80 characters and keeps short text whole", func(t *testing.T) {
		short := strings.Repeat("a", 80)
		if got := PromptTitle(short); got != short {
			t.Errorf("PromptTitle(80 chars) changed the text: %q", got)
		}
		long := strings.Repeat("a", 100)
		if got := PromptTitle(long); got != short {
			t.Errorf("PromptTitle(100 chars) = %d chars, want 80", len(got))
		}
	})

	t.Run("counts UTF-16 code units, not bytes or runes", func(t *testing.T) {
		// 80 é characters are 80 UTF-16 units but 160 bytes; the cut lands after 80.
		text := strings.Repeat("é", 90)
		want := strings.Repeat("é", 80)
		if got := PromptTitle(text); got != want {
			t.Errorf("PromptTitle(é×90) = %q, want é×80", got)
		}
	})
}

func TestIsInFlight(t *testing.T) {
	t.Run("is true only with no stop reason and no output yet", func(t *testing.T) {
		streaming := &RequestRecord{Tokens: Tokens{}}
		if !IsInFlight(streaming) {
			t.Error("IsInFlight(no stop reason, 0 output) = false, want true")
		}
		done := &RequestRecord{StopReason: ptr("end_turn"), Tokens: Tokens{}}
		if IsInFlight(done) {
			t.Error("IsInFlight(stop reason set) = true, want false")
		}
		answered := &RequestRecord{Tokens: Tokens{Output: 5}}
		if IsInFlight(answered) {
			t.Error("IsInFlight(output > 0) = true, want false")
		}
	})
}

func TestModelConstants(t *testing.T) {
	t.Run("carry the values the dashboard reads", func(t *testing.T) {
		if NoticeEvent != "Notice" {
			t.Errorf("NoticeEvent = %q, want Notice", NoticeEvent)
		}
		if OutsideSession != "outside-a-session" {
			t.Errorf("OutsideSession = %q, want outside-a-session", OutsideSession)
		}
		if UnattachedName != "Unattached" {
			t.Errorf("UnattachedName = %q, want Unattached", UnattachedName)
		}
		if ActivityBuckets != 48 {
			t.Errorf("ActivityBuckets = %d, want 48", ActivityBuckets)
		}
		if ActivityLiveMs != 900_000 {
			t.Errorf("ActivityLiveMs = %d, want 900000", ActivityLiveMs)
		}
		if ActivityLiveBucketMs != 18_750 {
			t.Errorf("ActivityLiveBucketMs = %d, want 18750", ActivityLiveBucketMs)
		}
		if MaxSessions != 200 || MaxRecords != 200_000 {
			t.Errorf("caps = %d/%d, want 200/200000", MaxSessions, MaxRecords)
		}
		want := []RouterEventKind{
			"fallback", "refusal", "rate_limited", "budget_stop", "restart",
			"upstream_failing", "upstream_recovered",
		}
		if !reflect.DeepEqual(RouterEventKinds, want) {
			t.Errorf("RouterEventKinds = %v, want %v", RouterEventKinds, want)
		}
	})
}

func TestRequestRecordJSON(t *testing.T) {
	t.Run("serializes with the wire's keys in order, optionals absent", func(t *testing.T) {
		b, err := json.Marshal(RequestRecord{
			ID: "r1", SessionID: "s1", AgentID: "a1", Model: "glm-5.3", Upstream: "https://x",
			Ts: 5, Tokens: Tokens{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4},
			Provider: "Z.ai",
		})
		if err != nil {
			t.Fatal(err)
		}
		want := `{"id":"r1","sessionId":"s1","agentId":"a1","model":"glm-5.3","upstream":"https://x",` +
			`"ts":5,"latencyMs":null,` +
			`"tokens":{"input":1,"output":2,"cacheRead":3,"cacheWrite":4},` +
			`"stopReason":null,"provider":"Z.ai"}`
		if string(b) != want {
			t.Errorf("json = %s, want %s", b, want)
		}
	})

	t.Run("carries the streaming latencies only when a route line measured them", func(t *testing.T) {
		b, err := json.Marshal(RequestRecord{
			ID: "r1", SessionID: "s1", AgentID: "a1", Model: "glm-5.3", Upstream: "u",
			Ts: 1, LatencyMs: ptr(int64(1_200)), TtfbMs: ptr(int64(180)), TtftMs: ptr(int64(420)),
			Tokens: Tokens{Input: 1}, Provider: "p",
		})
		if err != nil {
			t.Fatal(err)
		}
		want := `{"id":"r1","sessionId":"s1","agentId":"a1","model":"glm-5.3","upstream":"u",` +
			`"ts":1,"latencyMs":1200,"ttfbMs":180,"ttftMs":420,` +
			`"tokens":{"input":1,"output":0,"cacheRead":0,"cacheWrite":0},` +
			`"stopReason":null,"provider":"p"}`
		if string(b) != want {
			t.Errorf("json = %s, want %s", b, want)
		}
	})

	t.Run("carries the optionals only when set, thinking inside tokens", func(t *testing.T) {
		thinking := int64(7)
		b, err := json.Marshal(RequestRecord{
			ID: "r1", SessionID: "s1", AgentID: "a1", Model: "m", Upstream: "u", Ts: 1,
			LatencyMs: ptr(int64(9)), Tokens: Tokens{Input: 1, Thinking: &thinking},
			StopReason: ptr("end_turn"), Provider: "p", Route: ptr("provider"),
			Via: ptr("zai"), Totals: true, Error: ptr("boom"),
			ParentAgentID: ptr("parent"), CacheWrite1h: ptr(int64(2)), Speed: ptr("fast"),
			Geo: ptr("us"), ServiceTier: ptr("priority"), What: ptr("→ text"),
			PromptHash: ptr("h"), Headers: &Headers{{Name: "y", Value: "2"}, {Name: "x", Value: "1"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		want := `{"id":"r1","sessionId":"s1","agentId":"a1","model":"m","upstream":"u","ts":1,` +
			`"latencyMs":9,` +
			`"tokens":{"input":1,"output":0,"cacheRead":0,"cacheWrite":0,"thinking":7},` +
			`"stopReason":"end_turn","provider":"p","route":"provider","via":"zai","what":"→ text",` +
			`"error":"boom","parentAgentId":"parent","promptHash":"h","headers":{"y":"2","x":"1"},` +
			`"totals":true,"cacheWrite1h":2,"speed":"fast","geo":"us","serviceTier":"priority"}`
		if string(b) != want {
			t.Errorf("json = %s, want %s", b, want)
		}
	})

	t.Run("prints a harness tag and a harness-reported cost, claude printing neither", func(t *testing.T) {
		cost := 0.0123
		tagged, err := json.Marshal(RequestRecord{
			ID: "r1", SessionID: "s1", AgentID: "main", Model: "gpt-5-codex", Ts: 1,
			Tokens: Tokens{Input: 1}, Provider: "other",
			Harness: HarnessCodex, CostUSD: &cost,
		})
		if err != nil {
			t.Fatal(err)
		}
		want := `{"id":"r1","sessionId":"s1","agentId":"main","model":"gpt-5-codex","upstream":"",` +
			`"ts":1,"latencyMs":null,` +
			`"tokens":{"input":1,"output":0,"cacheRead":0,"cacheWrite":0},` +
			`"stopReason":null,"provider":"other","harness":"codex","costUsd":0.0123}`
		if string(tagged) != want {
			t.Errorf("json = %s, want %s", tagged, want)
		}
		plain, err := json.Marshal(RequestRecord{
			ID: "r1", SessionID: "s1", AgentID: "main", Model: "m", Ts: 1,
			Tokens: Tokens{Input: 1}, Provider: "p", Harness: HarnessClaude,
		})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(plain), "harness") {
			t.Errorf("json = %s, want no harness key on a claude record", plain)
		}
	})

	t.Run("prints a route line's own extras before the model and its route after provider", func(t *testing.T) {
		b, err := json.Marshal(RequestRecord{
			ID: "r1", SessionID: "s1", AgentID: "a1", Model: "m", Upstream: "u", Ts: 1,
			Tokens: Tokens{Input: 1}, StopReason: ptr("429"), Provider: "p",
			Error: ptr("boom"), PromptHash: ptr("h"), Route: ptr("direct"), Via: ptr("zai"),
			EarlyKeys: []string{"error", "promptHash"},
			TailKeys:  []string{"route", "via"},
		})
		if err != nil {
			t.Fatal(err)
		}
		want := `{"id":"r1","sessionId":"s1","agentId":"a1","error":"boom","promptHash":"h",` +
			`"model":"m","upstream":"u","ts":1,"latencyMs":null,` +
			`"tokens":{"input":1,"output":0,"cacheRead":0,"cacheWrite":0},` +
			`"stopReason":"429","provider":"p","route":"direct","via":"zai"}`
		if string(b) != want {
			t.Errorf("json = %s, want %s", b, want)
		}
	})

	t.Run("prints a merged record's tail in the order it landed, billing in its slot", func(t *testing.T) {
		oneHour := int64(2)
		b, err := json.Marshal(RequestRecord{
			ID: "r1", SessionID: "s1", AgentID: "a1", Model: "m", Upstream: "u", Ts: 1,
			Tokens: Tokens{Input: 1}, StopReason: ptr("end_turn"), Provider: "p",
			CacheWrite1h: &oneHour, What: ptr("→ text"), Via: ptr("127.0.0.1:17787"),
			Route: ptr("direct"), PromptHash: ptr("h"),
			TailKeys: []string{"what", "via", "route", "promptHash"},
		})
		if err != nil {
			t.Fatal(err)
		}
		want := `{"id":"r1","sessionId":"s1","agentId":"a1","model":"m","upstream":"u","ts":1,` +
			`"latencyMs":null,` +
			`"tokens":{"input":1,"output":0,"cacheRead":0,"cacheWrite":0},` +
			`"cacheWrite1h":2,"stopReason":"end_turn","provider":"p",` +
			`"what":"→ text","via":"127.0.0.1:17787","route":"direct","promptHash":"h"}`
		if string(b) != want {
			t.Errorf("json = %s, want %s", b, want)
		}
	})

	t.Run("serializes cost as JavaScript prints it, null when unpriced", func(t *testing.T) {
		type row struct {
			CostUsd *USD `json:"costUsd"`
		}
		b, err := json.Marshal(row{CostUsd: ptr(USD(0.35))})
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != `{"costUsd":0.35}` {
			t.Errorf("json = %s, want {\"costUsd\":0.35}", b)
		}
		b, err = json.Marshal(row{CostUsd: ptr(USD(2.5e-7))})
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != `{"costUsd":2.5e-7}` {
			t.Errorf("json = %s, want {\"costUsd\":2.5e-7}", b)
		}
		b, err = json.Marshal(row{})
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != `{"costUsd":null}` {
			t.Errorf("json = %s, want {\"costUsd\":null}", b)
		}
	})
}

func TestHeaders(t *testing.T) {
	t.Run("re-encodes a headers object with its keys in arrival order", func(t *testing.T) {
		raw := `{"retry-after":"2","request-id":"req_1","content-type":"application/json",` +
			`"x-ratelimit-remaining-requests":"99"}`
		var headers Headers
		if err := json.Unmarshal([]byte(raw), &headers); err != nil {
			t.Fatal(err)
		}
		encoded, err := headers.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != raw {
			t.Errorf("encoded %s, want the object as it arrived: %s", encoded, raw)
		}
	})

	t.Run("folds a repeated name the way a parse does: first position, last value", func(t *testing.T) {
		var headers Headers
		raw := `{"retry-after":"1","request-id":"req_1","retry-after":"2"}`
		if err := json.Unmarshal([]byte(raw), &headers); err != nil {
			t.Fatal(err)
		}
		encoded, err := headers.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if want := `{"retry-after":"2","request-id":"req_1"}`; string(encoded) != want {
			t.Errorf("encoded %s, want %s", encoded, want)
		}
		if value, ok := headers.Get("retry-after"); !ok || value != "2" {
			t.Errorf("Get(retry-after) = %q %v, want 2", value, ok)
		}
	})

	t.Run("drops the values that are not strings", func(t *testing.T) {
		var headers Headers
		if err := json.Unmarshal([]byte(`{"request-id":"req_1","retry-after":2,"x-note":null}`), &headers); err != nil {
			t.Fatal(err)
		}
		encoded, err := headers.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if want := `{"request-id":"req_1"}`; string(encoded) != want {
			t.Errorf("encoded %s, want %s", encoded, want)
		}
	})

	t.Run("keeps an empty object and reads null as none", func(t *testing.T) {
		var empty Headers
		if err := json.Unmarshal([]byte(`{}`), &empty); err != nil {
			t.Fatal(err)
		}
		if empty == nil || len(empty) != 0 {
			t.Errorf("empty object = %v, want a present empty", empty)
		}
		encoded, err := empty.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != `{}` {
			t.Errorf("encoded %s, want {}", encoded)
		}
		var none Headers
		if err := json.Unmarshal([]byte(`null`), &none); err != nil {
			t.Fatal(err)
		}
		if none != nil {
			t.Errorf("null = %v, want none", none)
		}
	})

	t.Run("writes the strings the way JSON.stringify does", func(t *testing.T) {
		raw := `{"x-note":"a<b>&cé"}`
		var headers Headers
		if err := json.Unmarshal([]byte(raw), &headers); err != nil {
			t.Fatal(err)
		}
		encoded, err := headers.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != raw {
			t.Errorf("encoded %s, want %s", encoded, raw)
		}
	})
}

func TestSpoolLineUnmarshal(t *testing.T) {
	t.Run("fills the known fields and parks the unknown ones in Extra", func(t *testing.T) {
		raw := `{"ts":"2026-10-14T12:00:00.000Z","event":"route","session_id":"s1","model":"glm-5.3",` +
			`"model_env":{"ANTHROPIC_MODEL":"glm-5.3"},"pid":42,"latency_ms":150,` +
			`"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":3,"thoughts":9},` +
			`"headers":{"retry-after":"2"},"future_field":"rides along","model_extra":null}`
		var line SpoolLine
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatal(err)
		}
		if line.Ts != "2026-10-14T12:00:00.000Z" || line.Event != "route" {
			t.Errorf("ts/event = %q/%q", line.Ts, line.Event)
		}
		if *line.SessionID != "s1" || *line.Model != "glm-5.3" {
			t.Errorf("session_id/model = %v/%v", line.SessionID, line.Model)
		}
		if *line.PID != 42 || *line.LatencyMs != 150 {
			t.Errorf("pid/latency_ms = %v/%v", line.PID, line.LatencyMs)
		}
		if line.ModelEnv["ANTHROPIC_MODEL"] != "glm-5.3" {
			t.Errorf("model_env = %v", line.ModelEnv)
		}
		if value, ok := line.Headers.Get("retry-after"); !ok || value != "2" {
			t.Errorf("headers = %v, want retry-after 2", line.Headers)
		}
		if line.Usage == nil || *line.Usage.InputTokens != 10 || *line.Usage.OutputTokens != 5 ||
			*line.Usage.CacheReadInputTokens != 3 || line.Usage.CacheCreationInputTokens != nil {
			t.Errorf("usage = %+v", line.Usage)
		}
		if string(line.Usage.Extra["thoughts"]) != "9" {
			t.Errorf("usage extra = %v", line.Usage.Extra)
		}
		if len(line.Extra) != 2 || string(line.Extra["future_field"]) != `"rides along"` ||
			string(line.Extra["model_extra"]) != "null" {
			t.Errorf("extra = %v, want future_field and model_extra", line.Extra)
		}
	})

	t.Run("reads a router health line whose model is null", func(t *testing.T) {
		var line SpoolLine
		if err := json.Unmarshal([]byte(`{"ts":"t","event":"router.event","model":null}`), &line); err != nil {
			t.Fatal(err)
		}
		if line.Model != nil {
			t.Errorf("model = %v, want nil", *line.Model)
		}
	})

	t.Run("rejects broken JSON", func(t *testing.T) {
		var line SpoolLine
		if err := json.Unmarshal([]byte(`{"ts":`), &line); err == nil {
			t.Error("Unmarshal(broken json) succeeded, want an error")
		}
	})
}

// The want bytes are JSON.stringify(JSON.parse(line)), byte for byte; regenerate them with:
//
//	bun -e 'for (const l of require("fs").readFileSync(0,"utf8").split("\n")) if (l.trim()) console.log(JSON.stringify(JSON.parse(l)))' < lines.jsonl
//
// The payload marshals through domain.MarshalCompactJSON, the one encoder whose bytes
// JSON.stringify matches: encoding/json's own escaping rewrites a MarshalJSON method's
// output (`<`, `>`, `&`, U+2028, U+2029) even with HTML escaping off.
func TestSpoolLineMarshal(t *testing.T) {
	t.Run("re-encodes a read line exactly as JSON.stringify(JSON.parse(line))", func(t *testing.T) {
		cases := []struct{ name, line, want string }{
			{
				"a SessionStart hook line",
				`{"ts":"2026-10-14T08:00:00.000Z","event":"SessionStart","session_id":"9d1e",` +
					`"transcript_path":"/Users/raouf/.claude/projects/p/9d1e.jsonl","cwd":"/Users/raouf/work/radar",` +
					`"agent_id":"main","pid":4242,"ppid":4241,"base_url":"http://127.0.0.1:17787",` +
					`"model_env":{"ANTHROPIC_MODEL":"glm-5.3"},"cc_version":"2.1.5"}`,
				`{"ts":"2026-10-14T08:00:00.000Z","event":"SessionStart","session_id":"9d1e",` +
					`"transcript_path":"/Users/raouf/.claude/projects/p/9d1e.jsonl","cwd":"/Users/raouf/work/radar",` +
					`"agent_id":"main","pid":4242,"ppid":4241,"base_url":"http://127.0.0.1:17787",` +
					`"model_env":{"ANTHROPIC_MODEL":"glm-5.3"},"cc_version":"2.1.5"}`,
			},
			{
				"a PreToolUse hook line with a tool input",
				`{"ts":"2026-10-14T08:00:01.000Z","event":"PreToolUse","session_id":"9d1e","agent_id":"main",` +
					`"tool_name":"Bash","tool_use_id":"toolu_01A",` +
					`"tool_input":{"command":"git status --short","timeout":120000},"pid":4242,"ppid":4241}`,
				`{"ts":"2026-10-14T08:00:01.000Z","event":"PreToolUse","session_id":"9d1e","agent_id":"main",` +
					`"tool_name":"Bash","tool_use_id":"toolu_01A",` +
					`"tool_input":{"command":"git status --short","timeout":120000},"pid":4242,"ppid":4241}`,
			},
			{
				"a PostToolUse hook line with a tool response array",
				`{"ts":"2026-10-14T08:00:02.000Z","event":"PostToolUse","session_id":"9d1e","agent_id":"main",` +
					`"tool_name":"Read","tool_use_id":"toolu_01B",` +
					`"tool_response":[{"type":"text","text":"package main"}],"pid":4242,"ppid":4241}`,
				`{"ts":"2026-10-14T08:00:02.000Z","event":"PostToolUse","session_id":"9d1e","agent_id":"main",` +
					`"tool_name":"Read","tool_use_id":"toolu_01B",` +
					`"tool_response":[{"type":"text","text":"package main"}],"pid":4242,"ppid":4241}`,
			},
			{
				"a prompt with unicode from every plane the dashboard shows",
				`{"ts":"2026-10-14T08:00:03.000Z","event":"UserPromptSubmit","session_id":"9d1e",` +
					`"prompt":"日本語のプロンプト ✨ café crème 🚀","pid":4242,"ppid":4241}`,
				`{"ts":"2026-10-14T08:00:03.000Z","event":"UserPromptSubmit","session_id":"9d1e",` +
					`"prompt":"日本語のプロンプト ✨ café crème 🚀","pid":4242,"ppid":4241}`,
			},
			{
				"a notification whose message holds controls and quotes",
				`{"ts":"2026-10-14T08:00:04.000Z","event":"Notification","session_id":"9d1e",` +
					`"message":"line one\nline two\tand a vertical\u000b cut \"quoted\" \\done","pid":4242,"ppid":4241}`,
				`{"ts":"2026-10-14T08:00:04.000Z","event":"Notification","session_id":"9d1e",` +
					`"message":"line one\nline two\tand a vertical\u000b cut \"quoted\" \\done","pid":4242,"ppid":4241}`,
			},
			{
				"a route line with a numeric status and a usage block",
				`{"ts":"2026-10-14T08:00:05.000Z","event":"route","plugin":"zai","model":"glm-5.3",` +
					`"upstream":"https://api.z.ai/api/anthropic","route":"provider","status":200,"latency_ms":1840,` +
					`"session_id":"9d1e","agent_id":"main","parent_agent_id":"main",` +
					`"usage":{"input_tokens":1234,"output_tokens":567,"cache_creation_input_tokens":89,` +
					`"cache_read_input_tokens":1011,"service_tier":"priority"},"prompt_hash":"a1b2c3",` +
					`"headers":{"retry-after":"2","x-request-id":"req-9"}}`,
				`{"ts":"2026-10-14T08:00:05.000Z","event":"route","plugin":"zai","model":"glm-5.3",` +
					`"upstream":"https://api.z.ai/api/anthropic","route":"provider","status":200,"latency_ms":1840,` +
					`"session_id":"9d1e","agent_id":"main","parent_agent_id":"main",` +
					`"usage":{"input_tokens":1234,"output_tokens":567,"cache_creation_input_tokens":89,` +
					`"cache_read_input_tokens":1011,"service_tier":"priority"},"prompt_hash":"a1b2c3",` +
					`"headers":{"retry-after":"2","x-request-id":"req-9"}}`,
			},
			{
				"a failed route line with a numeric error status",
				`{"ts":"2026-10-14T08:00:06.000Z","event":"route","plugin":"kimi","model":"kimi-k2",` +
					`"upstream":"https://api.moonshot.cn/anthropic","route":"provider","status":429,` +
					`"error":"rate limited: retry after 2s","latency_ms":97,"session_id":"9d1e","agent_id":"w1"}`,
				`{"ts":"2026-10-14T08:00:06.000Z","event":"route","plugin":"kimi","model":"kimi-k2",` +
					`"upstream":"https://api.moonshot.cn/anthropic","route":"provider","status":429,` +
					`"error":"rate limited: retry after 2s","latency_ms":97,"session_id":"9d1e","agent_id":"w1"}`,
			},
			{
				"a router health line with epoch milliseconds and a null model",
				`{"kind":"router.event","plugin":"zai","event":"refusal","reason":"no key set","model":null,` +
					`"ts":1767225600000}`,
				`{"kind":"router.event","plugin":"zai","event":"refusal","reason":"no key set","model":null,` +
					`"ts":1767225600000}`,
			},
			{
				"a capture line",
				`{"ts":"2026-10-14T08:00:07.000Z","event":"capture","plugin":"zai","prompt_hash":"a1b2c3",` +
					`"gz":"H4sIAAAAAAAAA0tJLEkAHzkYBQAA//8="}`,
				`{"ts":"2026-10-14T08:00:07.000Z","event":"capture","plugin":"zai","prompt_hash":"a1b2c3",` +
					`"gz":"H4sIAAAAAAAAA0tJLEkAHzkYBQAA//8="}`,
			},
			{
				"a line with big numbers and float forms",
				`{"ts":"2026-10-14T08:00:08.000Z","event":"route","plugin":"zai","model":"glm-5.3",` +
					`"upstream":"https://api.z.ai/api/anthropic","route":"provider","status":200,"latency_ms":150,` +
					`"usage":{"input_tokens":12.0,"output_tokens":1e3},"weight":1e21,` +
					`"ratio":0.30000000000000004,"request_id":9007199254740993}`,
				`{"ts":"2026-10-14T08:00:08.000Z","event":"route","plugin":"zai","model":"glm-5.3",` +
					`"upstream":"https://api.z.ai/api/anthropic","route":"provider","status":200,"latency_ms":150,` +
					`"usage":{"input_tokens":12,"output_tokens":1000},"weight":1e+21,` +
					`"ratio":0.30000000000000004,"request_id":9007199254740992}`,
			},
			{
				"a subagent start with unknown nested keys",
				`{"ts":"2026-10-14T08:00:09.000Z","event":"SubagentStart","session_id":"9d1e","agent_id":"w1",` +
					`"agent_type":"general-purpose","parent_agent_id":"main",` +
					`"meta":{"description":"search the docs","model":"glm-5.3-flash","flags":[true,null,"deep"]},` +
					`"pid":4242,"ppid":4241}`,
				`{"ts":"2026-10-14T08:00:09.000Z","event":"SubagentStart","session_id":"9d1e","agent_id":"w1",` +
					`"agent_type":"general-purpose","parent_agent_id":"main",` +
					`"meta":{"description":"search the docs","model":"glm-5.3-flash","flags":[true,null,"deep"]},` +
					`"pid":4242,"ppid":4241}`,
			},
			{
				"a line with escaped slashes, unicode escapes and line separators",
				`{"ts":"2026-10-14T08:00:10.000Z","event":"Stop","session_id":"9d1e",` +
					`"cwd":"\/Users\/raouf\/work\/radar","note":"café    done","pid":4242,"ppid":4241}`,
				`{"ts":"2026-10-14T08:00:10.000Z","event":"Stop","session_id":"9d1e",` +
					`"cwd":"/Users/raouf/work/radar","note":"café ` + "  " + ` done","pid":4242,"ppid":4241}`,
			},
			{
				"a line with nulls, empties and a mistyped pid",
				`{"ts":"2026-10-14T08:00:11.000Z","event":"SessionEnd","session_id":"9d1e","model_extra":null,` +
					`"migrating":true,"count":3,"opts":{},"tags":["a","b"],"pid":"not-a-pid","ppid":4241}`,
				`{"ts":"2026-10-14T08:00:11.000Z","event":"SessionEnd","session_id":"9d1e","model_extra":null,` +
					`"migrating":true,"count":3,"opts":{},"tags":["a","b"],"pid":"not-a-pid","ppid":4241}`,
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				var line SpoolLine
				if err := json.Unmarshal([]byte(tc.line), &line); err != nil {
					t.Fatalf("Unmarshal: %v", err)
				}
				got, err := domain.MarshalCompactJSON(line)
				if err != nil {
					t.Fatalf("Marshal: %v", err)
				}
				if string(got) != tc.want {
					t.Errorf("marshalled\n%s\nwant\n%s", got, tc.want)
				}
			})
		}
	})

	t.Run("a line built in code marshals its declared fields in the line's own order", func(t *testing.T) {
		built := SpoolLine{
			Ts: "2026-10-14T08:00:00.000Z", Event: "route",
			SessionID: ptr("s1"), AgentID: ptr("main"),
			ToolName: ptr("Bash"), ToolInput: map[string]any{"command": "ls"},
			PID: ptr(int64(7)), BaseURL: ptr("http://127.0.0.1:17787"),
			ModelEnv: map[string]string{"ANTHROPIC_MODEL": "glm-5.3"},
			Model:    ptr("glm-5.3"), Upstream: ptr("https://api.z.ai/api/anthropic"),
			Status: ptr("200"), LatencyMs: ptr(int64(150)),
			Usage: &SpoolUsage{InputTokens: ptr(int64(1)), OutputTokens: ptr(int64(2))},
		}
		got, err := domain.MarshalCompactJSON(built)
		if err != nil {
			t.Fatal(err)
		}
		want := `{"ts":"2026-10-14T08:00:00.000Z","event":"route","session_id":"s1","agent_id":"main",` +
			`"tool_name":"Bash","tool_input":{"command":"ls"},"pid":7,"base_url":"http://127.0.0.1:17787",` +
			`"model_env":{"ANTHROPIC_MODEL":"glm-5.3"},"model":"glm-5.3",` +
			`"upstream":"https://api.z.ai/api/anthropic","status":"200","latency_ms":150,` +
			`"usage":{"input_tokens":1,"output_tokens":2}}`
		if string(got) != want {
			t.Errorf("marshalled %s, want %s", got, want)
		}
		empty, err := domain.MarshalCompactJSON(SpoolLine{})
		if err != nil {
			t.Fatal(err)
		}
		if string(empty) != `{"ts":"","event":""}` {
			t.Errorf("empty line marshalled %s, want ts and event alone", empty)
		}
	})

	t.Run("a declared field of another shape reads as absent while the line keeps its value", func(t *testing.T) {
		var line SpoolLine
		raw := `{"ts":"t","event":"route","status":429,"pid":"not-a-pid","usage":"x"}`
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if line.Status != nil || line.PID != nil || line.Usage != nil {
			t.Errorf("status/pid/usage = %v/%v/%v, want all absent", line.Status, line.PID, line.Usage)
		}
		got, err := domain.MarshalCompactJSON(line)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != raw {
			t.Errorf("marshalled %s, want the line as read: %s", got, raw)
		}
	})
}

func TestSpoolUsageUnmarshal(t *testing.T) {
	t.Run("decodes each counter on its own, so one odd counter keeps the other three", func(t *testing.T) {
		var usage SpoolUsage
		if err := json.Unmarshal([]byte(`{"input_tokens":10,"output_tokens":5,`+
			`"cache_creation_input_tokens":"x","cache_read_input_tokens":3}`), &usage); err != nil {
			t.Fatal(err)
		}
		if *usage.InputTokens != 10 || *usage.OutputTokens != 5 || *usage.CacheReadInputTokens != 3 {
			t.Errorf("counters = %+v, want 10/5/3", usage)
		}
		if usage.CacheCreationInputTokens != nil {
			t.Errorf("cache_creation_input_tokens = %d, want unset", *usage.CacheCreationInputTokens)
		}
	})

	t.Run("whole floats and exponents read as their whole value", func(t *testing.T) {
		var usage SpoolUsage
		if err := json.Unmarshal([]byte(`{"input_tokens":12.0,"output_tokens":1e3}`), &usage); err != nil {
			t.Fatal(err)
		}
		if *usage.InputTokens != 12 || *usage.OutputTokens != 1000 {
			t.Errorf("counters = %d/%d, want 12/1000", *usage.InputTokens, *usage.OutputTokens)
		}
	})

	t.Run("null and fractions read as absent", func(t *testing.T) {
		var usage SpoolUsage
		if err := json.Unmarshal([]byte(`{"input_tokens":null,"output_tokens":12.5}`), &usage); err != nil {
			t.Fatal(err)
		}
		if usage.InputTokens != nil || usage.OutputTokens != nil {
			t.Errorf("counters = %v/%v, want both unset", usage.InputTokens, usage.OutputTokens)
		}
	})

	t.Run("unknown keys still ride in Extra", func(t *testing.T) {
		var usage SpoolUsage
		if err := json.Unmarshal([]byte(`{"input_tokens":1,"thoughts":9}`), &usage); err != nil {
			t.Fatal(err)
		}
		if *usage.InputTokens != 1 || string(usage.Extra["thoughts"]) != "9" {
			t.Errorf("counter/extra = %v/%v", usage.InputTokens, usage.Extra)
		}
	})

	t.Run("rejects a usage block that is not an object", func(t *testing.T) {
		var usage SpoolUsage
		if err := json.Unmarshal([]byte(`"x"`), &usage); err == nil {
			t.Error("Unmarshal(a string) succeeded, want an error")
		}
	})
}

// A line built in code (never read off the spool) marshals its declared fields with
// JSON.stringify's escaping: `<`, `>`, `&` and the two line separators raw. json.Marshal
// escapes all five, and the escapes survive inside a MarshalJSON method's own output.
func TestSpoolLineMarshalBuiltInCode(t *testing.T) {
	line := SpoolLine{Ts: "2026-10-15T05:00:00.000Z", Event: "Notification",
		Error: ptr("a<b>c&d e f")}
	want := "{\"ts\":\"2026-10-15T05:00:00.000Z\",\"event\":\"Notification\"," +
		"\"error\":\"a<b>c&d e f\"}"
	got, err := line.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("MarshalJSON = %s, want %s", got, want)
	}
	encoded, err := domain.MarshalCompactJSON(line)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != want {
		t.Errorf("MarshalCompactJSON(line) = %s, want %s", encoded, want)
	}
}

func TestSessionViewParentKey(t *testing.T) {
	encoded, err := domain.MarshalCompactJSON(SessionView{ID: "job", ParentSessionID: ptr("s1")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"parentSessionId":"s1"`) {
		t.Errorf("session view = %s, want the parent under parentSessionId", encoded)
	}
}
