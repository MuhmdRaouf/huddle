package ingest

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// The transcript parser over one state fed line by line.

var (
	t0       = time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC).UnixMilli()
	t1       = t0 + 1_000
	t2       = t0 + 3_000
	mainFile = FileIdentity{SessionID: "s1", Kind: shared.KindMain}
)

func strp(s string) *string { return &s }

func i64p(v int64) *int64 { return &v }

func iso(ms int64) string { return jsfmt.ISOTime(time.UnixMilli(ms)) }

// feedFile feeds one line: a raw string as it lay in the file, or a value marshalled to JSON.
func feedFile(state *TranscriptState, raw any, file FileIdentity) TranscriptEmit {
	line, isString := raw.(string)
	if !isString {
		data, err := json.Marshal(raw)
		if err != nil {
			panic(err)
		}
		line = string(data)
	}
	return FeedTranscriptLine(state, file, line)
}

func feed(state *TranscriptState, raw any) TranscriptEmit {
	return feedFile(state, raw, mainFile)
}

var inputKeyPattern = regexp.MustCompile(`^[0-9a-f]{8}$`)

// parseBlocks decodes a captured content string, the way the suite parses it back before
// comparing.
func parseBlocks(t *testing.T, captured *string) []map[string]any {
	t.Helper()
	if captured == nil {
		t.Fatalf("captured content is nil")
	}
	var blocks []map[string]any
	if err := json.Unmarshal([]byte(*captured), &blocks); err != nil {
		t.Fatalf("parse captured content %q: %v", *captured, err)
	}
	return blocks
}

func checkWhat(t *testing.T, got *string, want string) {
	t.Helper()
	if got == nil || *got != want {
		t.Errorf("what = %v, want %q", got, want)
	}
}

// firstRequest is the suite's `requests[0]?.…`: it fails the test when no request came.
func firstRequest(t *testing.T, emit TranscriptEmit) shared.RequestRecord {
	t.Helper()
	if len(emit.Requests) == 0 {
		t.Fatalf("requests = 0, want 1")
	}
	return emit.Requests[0]
}

// firstCapture is the same guard for the line's one content capture.
func firstCapture(t *testing.T, emit TranscriptEmit) ContentCapture {
	t.Helper()
	if len(emit.Content) == 0 {
		t.Fatalf("content captures = 0, want 1")
	}
	return emit.Content[0]
}

// assistantOverrides name the fields assistantLine takes over; the omit flags stand for a
// null (drop the field entirely), a nil pointer for an undefined (keep the default).
type assistantOverrides struct {
	timestamp     *string
	requestID     *string
	omitRequestID bool
	uuid          *string
	omitModel     bool
	usage         map[string]any
	omitUsage     bool
	content       []any
	sessionID     *string
}

// assistantLine is one assistant turn: a model, a usage block and a Bash tool_use,
// overridable field by field.
func assistantLine(o assistantOverrides) map[string]any {
	timestamp := iso(t1)
	if o.timestamp != nil {
		timestamp = *o.timestamp
	}
	usage := map[string]any{
		"input_tokens": 10, "output_tokens": 5,
		"cache_read_input_tokens": 2, "cache_creation_input_tokens": 1,
	}
	if o.usage != nil {
		usage = o.usage
	}
	content := o.content
	if content == nil {
		content = []any{map[string]any{
			"type": "tool_use", "id": "tu1", "name": "Bash",
			"input": map[string]any{"command": "ls"},
		}}
	}
	message := map[string]any{"role": "assistant", "stop_reason": "end_turn", "content": content}
	if !o.omitModel {
		message["model"] = "claude-sonnet-5-5"
	}
	if !o.omitUsage {
		message["usage"] = usage
	}
	line := map[string]any{"type": "assistant", "timestamp": timestamp, "message": message}
	if !o.omitRequestID {
		requestID := "req_9"
		if o.requestID != nil {
			requestID = *o.requestID
		}
		line["requestId"] = requestID
	}
	if o.uuid != nil {
		line["uuid"] = *o.uuid
	}
	if o.sessionID != nil {
		line["sessionId"] = *o.sessionID
	}
	return line
}

func TestIdentifyFile(t *testing.T) {
	t.Run("reads session and agent off the path", func(t *testing.T) {
		cases := []struct {
			path string
			want FileIdentity
		}{
			{"/h/.claude/projects/-Users-w-app/abc123.jsonl",
				FileIdentity{SessionID: "abc123", Kind: shared.KindMain}},
			{"/h/.claude/projects/-Users-w-app/abc123/subagents/worker-1.jsonl",
				FileIdentity{SessionID: "abc123", AgentID: strp("worker-1"), Kind: shared.KindSubagent}},
			{"bare-name", FileIdentity{SessionID: "bare-name", Kind: shared.KindMain}},
		}
		for _, test := range cases {
			if got := IdentifyFile(test.path); !reflect.DeepEqual(got, test.want) {
				t.Errorf("IdentifyFile(%q) = %+v, want %+v", test.path, got, test.want)
			}
		}
	})

	t.Run("keeps a backslash inside a session id", func(t *testing.T) {
		// a backslash is an ordinary character: the whole path is the session id, its .jsonl
		// tail dropped
		got := IdentifyFile(`a\sess-x.jsonl`)
		if got.SessionID != `a\sess-x` || got.Kind != shared.KindMain {
			t.Errorf("IdentifyFile(%q) = %+v, want the whole path as the session id", `a\sess-x.jsonl`, got)
		}
	})
}

func TestFeedTranscriptLineRejectsJunk(t *testing.T) {
	t.Run("returns the shared empty emit for bad JSON, non-objects and missing timestamps", func(t *testing.T) {
		state := NewTranscriptState()
		first := feed(state, "{not json")
		if !reflect.DeepEqual(first, TranscriptEmit{}) {
			t.Errorf("bad JSON emit = %+v, want the empty emit", first)
		}
		for _, raw := range []any{
			"[1,2]",
			`"text"`,
			map[string]any{},                 // no timestamp
			map[string]any{"timestamp": 123}, // not a string
			map[string]any{"timestamp": "not-a-date"}, // unparseable
		} {
			if got := feed(state, raw); !reflect.DeepEqual(got, TranscriptEmit{}) {
				t.Errorf("feed(%v) = %+v, want the empty emit", raw, got)
			}
		}
		// the same empty value comes back every time, never a partially filled one
		if again := feed(state, "{not json"); !reflect.DeepEqual(again, first) {
			t.Errorf("second bad-JSON emit = %+v, want %+v", again, first)
		}
	})
}

func TestFeedTranscriptLineHappyPath(t *testing.T) {
	t.Run("emits the main agent once and the session meta on every line that carries it", func(t *testing.T) {
		state := NewTranscriptState()
		first := feed(state, map[string]any{
			"type": "user", "timestamp": iso(t0), "cwd": "/w/app", "version": "2.0.0",
			"message": map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "hi"},
				},
			},
		})
		wantAgents := []AgentDescriptor{{SessionID: "s1", ID: "main", Kind: shared.KindMain}}
		if !reflect.DeepEqual(first.Agents, wantAgents) {
			t.Errorf("agents = %+v, want %+v", first.Agents, wantAgents)
		}
		wantSession := &SessionMeta{ID: "s1", Cwd: strp("/w/app"), CcVersion: strp("2.0.0")}
		if !reflect.DeepEqual(first.Session, wantSession) {
			t.Errorf("session = %+v, want %+v", first.Session, wantSession)
		}

		second := feed(state, map[string]any{"type": "user", "timestamp": iso(t1), "cwd": "/w/app"})
		if len(second.Agents) != 0 {
			t.Errorf("second agents = %+v, want none", second.Agents)
		}
		wantSecond := &SessionMeta{ID: "s1", Cwd: strp("/w/app")}
		if !reflect.DeepEqual(second.Session, wantSecond) { // ccVersion absent when not a string
			t.Errorf("second session = %+v, want %+v", second.Session, wantSecond)
		}
	})

	t.Run("builds a request per assistant message with latency from the previous turn boundary", func(t *testing.T) {
		state := NewTranscriptState()
		feed(state, map[string]any{"type": "user", "timestamp": iso(t0)})
		emit := feed(state, assistantLine(assistantOverrides{}))
		want := []shared.RequestRecord{{
			ID: "req_9", SessionID: "s1", AgentID: "main", Model: "claude-sonnet-5-5",
			Upstream: "", Ts: t1, LatencyMs: i64p(1_000),
			Tokens:     shared.Tokens{Input: 10, Output: 5, CacheRead: 2, CacheWrite: 1},
			StopReason: strp("end_turn"), Provider: "Anthropic", What: strp("→ Bash ls"),
			TailKeys: []string{"what"},
		}}
		if !reflect.DeepEqual(emit.Requests, want) {
			t.Errorf("requests = %+v, want %+v", emit.Requests, want)
		}
	})

	t.Run("reads the billing fields the usage block names: 1 h writes, fast mode, geo and tier", func(t *testing.T) {
		state := NewTranscriptState()
		feed(state, map[string]any{"type": "user", "timestamp": iso(t0)})
		emit := feed(state, assistantLine(assistantOverrides{usage: map[string]any{
			"input_tokens": 10, "output_tokens": 5,
			"cache_read_input_tokens": 2, "cache_creation_input_tokens": 9,
			"cache_creation": map[string]any{
				"ephemeral_5m_input_tokens": 7, "ephemeral_1h_input_tokens": 2,
			},
			"speed": "fast", "inference_geo": "us", "service_tier": "standard",
		}}))
		if len(emit.Requests) != 1 {
			t.Fatalf("requests = %d, want 1", len(emit.Requests))
		}
		req := emit.Requests[0]
		wantTokens := shared.Tokens{Input: 10, Output: 5, CacheRead: 2, CacheWrite: 9}
		if !reflect.DeepEqual(req.Tokens, wantTokens) { // cacheWrite stays the total
			t.Errorf("tokens = %+v, want %+v", req.Tokens, wantTokens)
		}
		if req.CacheWrite1h == nil || *req.CacheWrite1h != 2 {
			t.Errorf("cacheWrite1h = %v, want 2", req.CacheWrite1h)
		}
		if req.Speed == nil || *req.Speed != "fast" {
			t.Errorf("speed = %v, want fast", req.Speed)
		}
		if req.Geo == nil || *req.Geo != "us" {
			t.Errorf("geo = %v, want us", req.Geo)
		}
		if req.ServiceTier == nil || *req.ServiceTier != "standard" {
			t.Errorf("serviceTier = %v, want standard", req.ServiceTier)
		}
	})

	t.Run("ignores an absent or zero 1 h count and empty-string conditions in the usage block", func(t *testing.T) {
		state := NewTranscriptState()
		emit := feed(state, assistantLine(assistantOverrides{usage: map[string]any{
			"input_tokens": 1, "output_tokens": 1, "cache_creation_input_tokens": 0,
			"cache_creation": map[string]any{
				"ephemeral_5m_input_tokens": 0, "ephemeral_1h_input_tokens": 0,
			},
			"speed": "", "inference_geo": "", "service_tier": "",
		}}))
		if len(emit.Requests) != 1 {
			t.Fatalf("requests = %d, want 1", len(emit.Requests))
		}
		req := emit.Requests[0]
		if req.CacheWrite1h != nil {
			t.Errorf("cacheWrite1h = %v, want nil", req.CacheWrite1h)
		}
		if req.Speed != nil {
			t.Errorf("speed = %v, want nil", req.Speed)
		}
		if req.Geo != nil {
			t.Errorf("geo = %v, want nil", req.Geo)
		}
		if req.ServiceTier != nil {
			t.Errorf("serviceTier = %v, want nil", req.ServiceTier)
		}
	})

	t.Run("falls back to msg:<uuid> or msg:<ts> as the request id and drops model-less messages", func(t *testing.T) {
		state := NewTranscriptState()
		withUuid := feed(state, assistantLine(assistantOverrides{omitRequestID: true, uuid: strp("u-7")}))
		if len(withUuid.Requests) != 1 {
			t.Fatalf("requests = %d, want 1", len(withUuid.Requests))
		}
		if got := withUuid.Requests[0].ID; got != "msg:u-7" {
			t.Errorf("id = %q, want msg:u-7", got)
		}
		noUuid := feed(state, assistantLine(assistantOverrides{
			omitRequestID: true, timestamp: strp(iso(t2)),
		}))
		if len(noUuid.Requests) != 1 {
			t.Fatalf("requests = %d, want 1", len(noUuid.Requests))
		}
		if got := noUuid.Requests[0].ID; got != fmt.Sprintf("msg:%d", t2) {
			t.Errorf("id = %q, want msg:%d", got, t2)
		}
		bare := feed(state, assistantLine(assistantOverrides{
			omitRequestID: true, omitModel: true, timestamp: strp(iso(t2)),
		}))
		if len(bare.Requests) != 0 {
			t.Errorf("requests = %+v, want none", bare.Requests)
		}
	})

	t.Run("yields no latency without a prior boundary or when timestamps go backwards", func(t *testing.T) {
		state := NewTranscriptState()
		cold := feed(state, assistantLine(assistantOverrides{timestamp: strp(iso(t2))}))
		if got := firstRequest(t, cold).LatencyMs; got != nil {
			t.Errorf("cold latencyMs = %v, want nil", got)
		}
		feed(state, map[string]any{"type": "user", "timestamp": iso(t2)})
		backwards := feed(state, assistantLine(assistantOverrides{timestamp: strp(iso(t0))}))
		if got := firstRequest(t, backwards).LatencyMs; got != nil {
			t.Errorf("backwards latencyMs = %v, want nil", got)
		}
	})

	t.Run("reads zero for absent or non-numeric usage fields", func(t *testing.T) {
		state := NewTranscriptState()
		garbage := feed(state, assistantLine(assistantOverrides{usage: map[string]any{
			"input_tokens": "x", "output_tokens": "y",
		}}))
		if got := firstRequest(t, garbage).Tokens; !reflect.DeepEqual(got, shared.Tokens{}) {
			t.Errorf("tokens = %+v, want the zero set", got)
		}
		absent := feed(state, assistantLine(assistantOverrides{
			omitUsage: true, requestID: strp("r2"), timestamp: strp(iso(t2)),
		}))
		if got := firstRequest(t, absent).Tokens; !reflect.DeepEqual(got, shared.Tokens{}) {
			t.Errorf("tokens = %+v, want the zero set", got)
		}
	})

	t.Run("keeps the thinking share of the output when the provider breaks it out", func(t *testing.T) {
		state := NewTranscriptState()
		emit := feed(state, assistantLine(assistantOverrides{usage: map[string]any{
			"input_tokens": 10, "output_tokens": 500,
			"cache_read_input_tokens": 0, "cache_creation_input_tokens": 0,
			"output_tokens_details": map[string]any{"thinking_tokens": 320},
		}}))
		want := shared.Tokens{Input: 10, Output: 500, Thinking: i64p(320)}
		if got := firstRequest(t, emit).Tokens; !reflect.DeepEqual(got, want) {
			t.Errorf("tokens = %+v, want %+v", got, want)
		}
		// zero or absent details stay off the record
		silent := feed(state, assistantLine(assistantOverrides{
			requestID: strp("r2"),
			usage: map[string]any{
				"input_tokens": 1, "output_tokens": 2,
				"output_tokens_details": map[string]any{"thinking_tokens": 0},
			},
		}))
		wantSilent := shared.Tokens{Input: 1, Output: 2}
		if got := firstRequest(t, silent).Tokens; !reflect.DeepEqual(got, wantSilent) {
			t.Errorf("tokens = %+v, want %+v", got, wantSilent)
		}
	})

	t.Run("pairs tool_use with its tool_result for a duration, marking failures", func(t *testing.T) {
		state := NewTranscriptState()
		feed(state, assistantLine(assistantOverrides{timestamp: strp(iso(t0))}))
		done := feed(state, map[string]any{
			"type": "user", "timestamp": iso(t2),
			"message": map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "tool_result", "tool_use_id": "tu1"},
				},
			},
		})
		if len(done.ToolCalls) != 1 {
			t.Fatalf("tool calls = %+v, want 1", done.ToolCalls)
		}
		call := done.ToolCalls[0]
		if call.ID != "tu1" || call.SessionID != "s1" || call.AgentID == nil || *call.AgentID != "main" ||
			call.Name != "Bash" || call.StartedAt != t0 ||
			call.DurationMs == nil || *call.DurationMs != 3_000 || !call.Ok {
			t.Errorf("tool call = %+v, want tu1 by main, Bash at %d for 3s ok", call, t0)
		}
		if call.InputKey == nil || !inputKeyPattern.MatchString(*call.InputKey) {
			t.Errorf("inputKey = %v, want 8 hex digits", call.InputKey)
		}
		feed(state, assistantLine(assistantOverrides{timestamp: strp(iso(t2)), requestID: strp("r2")}))
		failed := feed(state, map[string]any{
			"type": "user", "timestamp": iso(t2),
			"message": map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "tool_result", "tool_use_id": "nope", "is_error": true},
				},
			},
		})
		// no tool_use waited under that id, so nothing is emitted: an unmatched result is not a call
		if len(failed.ToolCalls) != 0 {
			t.Errorf("tool calls = %+v, want none for a result whose tool_use never showed up", failed.ToolCalls)
		}
	})

	t.Run("emits no tool call when the transcript opens on a tool_result", func(t *testing.T) {
		// a file read from a later offset, or after a compaction, starts mid-conversation: its
		// first results have no tool_use behind them and must not surface as a tool named unknown
		state := NewTranscriptState()
		emit := feed(state, map[string]any{
			"type": "user", "timestamp": iso(t2),
			"message": map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "tool_result", "tool_use_id": "late", "is_error": true},
				},
			},
		})
		if len(emit.ToolCalls) != 0 {
			t.Errorf("tool calls = %+v, want none", emit.ToolCalls)
		}
	})

	t.Run("bounds its pending tool calls: interrupted ones drop oldest-first like the other maps", func(t *testing.T) {
		state := NewTranscriptState()
		for at := int64(0); at < 600; at++ {
			feed(state, assistantLine(assistantOverrides{
				timestamp: strp(iso(t0 + at)),
				requestID: strp(fmt.Sprintf("r%d", at)),
				content: []any{map[string]any{
					"type": "tool_use", "id": fmt.Sprintf("tu%d", at), "name": "Bash",
					"input": map[string]any{"command": "ls"},
				}},
			})) // no tool_result ever lands: an interrupted run's calls would otherwise sit forever
		}
		if got := state.pendingTools.len(); got != 512 {
			t.Errorf("pendingTools holds %d, want 512", got)
		}
		if state.pendingTools.has("tu0") { // the oldest went
			t.Errorf("pendingTools still holds tu0")
		}
		if !state.pendingTools.has("tu599") { // the newest held
			t.Errorf("pendingTools lost tu599")
		}
	})

	t.Run("ignores string content and tool_use blocks without usable ids", func(t *testing.T) {
		state := NewTranscriptState()
		str := feed(state, map[string]any{
			"type": "user", "timestamp": iso(t0),
			"message": map[string]any{"role": "user", "content": "plain text"},
		})
		if len(str.ToolCalls) != 0 {
			t.Errorf("tool calls = %+v, want none", str.ToolCalls)
		}
		emit := feed(state, assistantLine(assistantOverrides{
			timestamp: strp(iso(t1)),
			content: []any{
				map[string]any{"type": "tool_use", "id": 7, "name": "Bash"},
				map[string]any{"type": "tool_use", "id": "ok-1", "name": "Read"},
			},
		}))
		if got := state.pendingTools.len(); got != 1 {
			t.Errorf("pendingTools holds %d, want 1 (ok-1 alone)", got)
		}
		if !state.pendingTools.has("ok-1") {
			t.Errorf("pendingTools lost ok-1")
		}
		if len(emit.Requests) != 1 {
			t.Errorf("requests = %d, want 1", len(emit.Requests))
		}
	})
}

func TestAgentAttribution(t *testing.T) {
	t.Run("routes sidechain entries to their own subagent tag", func(t *testing.T) {
		state := NewTranscriptState()
		emit := feed(state, map[string]any{
			"type": "assistant", "timestamp": iso(t1), "isSidechain": true, "agentId": "chain-1",
			"message": map[string]any{"role": "assistant", "model": "glm-5.3", "content": []any{}},
		})
		want := []AgentDescriptor{
			{SessionID: "s1", ID: "chain-1", ParentID: strp("main"), Kind: shared.KindSubagent},
		}
		if !reflect.DeepEqual(emit.Agents, want) {
			t.Errorf("agents = %+v, want %+v", emit.Agents, want)
		}
		if got := firstRequest(t, emit).AgentID; got != "chain-1" {
			t.Errorf("agentId = %q, want chain-1", got)
		}
		if got := firstRequest(t, emit).Provider; got != "Z.ai" {
			t.Errorf("provider = %q, want Z.ai", got)
		}
	})

	t.Run("falls back to the literal id 'sidechain' when the entry carries no agentId", func(t *testing.T) {
		state := NewTranscriptState()
		emit := feed(state, map[string]any{
			"type": "assistant", "timestamp": iso(t1), "isSidechain": true,
			"message": map[string]any{"role": "assistant", "model": "kimi-k2.5", "content": []any{}},
		})
		want := []AgentDescriptor{
			{SessionID: "s1", ID: "sidechain", ParentID: strp("main"), Kind: shared.KindSubagent},
		}
		if !reflect.DeepEqual(emit.Agents, want) {
			t.Errorf("agents = %+v, want %+v", emit.Agents, want)
		}
	})

	t.Run("lets entry.sessionId override the file's session and keeps per-agent turn boundaries", func(t *testing.T) {
		state := NewTranscriptState()
		emit := feed(state, map[string]any{
			"type": "user", "timestamp": iso(t0), "sessionId": "zz", "cwd": "/w/zz",
		})
		wantSession := &SessionMeta{ID: "zz", Cwd: strp("/w/zz")}
		if !reflect.DeepEqual(emit.Session, wantSession) {
			t.Errorf("session = %+v, want %+v", emit.Session, wantSession)
		}
		feed(state, map[string]any{"type": "system", "timestamp": iso(t1), "sessionId": "zz"})
		req := feed(state, assistantLine(assistantOverrides{timestamp: strp(iso(t2)), sessionID: strp("zz")}))
		if got := firstRequest(t, req).SessionID; got != "zz" {
			t.Errorf("sessionId = %q, want zz", got)
		}
		if got := firstRequest(t, req).LatencyMs; got == nil || *got != 2_000 { // system entries also mark turn boundaries
			t.Errorf("latencyMs = %v, want 2000", got)
		}
	})

	t.Run("attributes a subagents/*.jsonl file to its named agent", func(t *testing.T) {
		state := NewTranscriptState()
		file := FileIdentity{SessionID: "s1", AgentID: strp("w1"), Kind: shared.KindSubagent}
		emit := feedFile(state, map[string]any{"type": "user", "timestamp": iso(t0)}, file)
		want := []AgentDescriptor{
			{SessionID: "s1", ID: "w1", ParentID: strp("main"), Kind: shared.KindSubagent},
		}
		if !reflect.DeepEqual(emit.Agents, want) {
			t.Errorf("agents = %+v, want %+v", emit.Agents, want)
		}
		// the subagent has its own turn clock, independent of s1:main
		req := feedFile(state, assistantLine(assistantOverrides{timestamp: strp(iso(t1))}), file)
		if got := firstRequest(t, req).AgentID; got != "w1" {
			t.Errorf("agentId = %q, want w1", got)
		}
		if got := firstRequest(t, req).LatencyMs; got == nil || *got != 1_000 {
			t.Errorf("latencyMs = %v, want 1000", got)
		}
	})

	t.Run("carries a subagent meta file's type and task, and names the agent after its type", func(t *testing.T) {
		state := NewTranscriptState()
		file := FileIdentity{
			SessionID: "s1", AgentID: strp("agent-w1"), Kind: shared.KindSubagent,
			AgentMeta: &AgentMeta{AgentType: "Explore", Description: "find the flaky test"},
		}
		emit := feedFile(state, map[string]any{"type": "user", "timestamp": iso(t0)}, file)
		want := []AgentDescriptor{{
			SessionID: "s1", ID: "agent-w1", ParentID: strp("main"), Kind: shared.KindSubagent,
			Name: strp("Explore"), AgentType: strp("Explore"), Description: strp("find the flaky test"),
		}}
		if !reflect.DeepEqual(emit.Agents, want) {
			t.Errorf("agents = %+v, want %+v", emit.Agents, want)
		}
	})

	t.Run("names a subagent after the head of its first prompt when no meta file does", func(t *testing.T) {
		state := NewTranscriptState()
		file := FileIdentity{SessionID: "s1", AgentID: strp("agent-a95a00"), Kind: shared.KindSubagent}
		first := feedFile(state, map[string]any{
			"type": "user", "timestamp": iso(t0),
			"message": map[string]any{"role": "user", "content": "  Summarize the\n flaky test  "},
		}, file)
		if len(first.Agents) != 2 { // the agent's descriptor, then its prompt title
			t.Fatalf("agents = %+v, want the descriptor then the prompt title", first.Agents)
		}
		titled := first.Agents[1]
		if titled.ID != "agent-a95a00" || titled.Kind != shared.KindSubagent || titled.Name != nil ||
			titled.Title == nil || *titled.Title != "Summarize the flaky test" {
			t.Errorf("titled agent = %+v, want agent-a95a00 titled 'Summarize the flaky test'", titled)
		}
		again := feedFile(state, map[string]any{
			"type": "user", "timestamp": iso(t1),
			"message": map[string]any{"role": "user", "content": "another task"},
		}, file)
		if len(again.Agents) != 0 { // once per agent: later prompts never rename it
			t.Errorf("agents = %+v, want none", again.Agents)
		}
	})
}

func TestSessionNamingMeta(t *testing.T) {
	t.Run("emits a naming line as session meta, timestamp or not", func(t *testing.T) {
		state := NewTranscriptState()
		cases := []struct {
			line map[string]any
			want *SessionMeta
		}{
			{map[string]any{"type": "custom-title", "customTitle": "Port the router"},
				&SessionMeta{ID: "s1", CustomTitle: strp("Port the router")}},
			{map[string]any{"type": "agent-name", "agentName": "Scout"},
				&SessionMeta{ID: "s1", AgentName: strp("Scout")}},
			{map[string]any{"type": "ai-title", "aiTitle": "Porting the router"},
				&SessionMeta{ID: "s1", AITitle: strp("Porting the router")}},
		}
		for _, test := range cases {
			if got := feed(state, test.line).Session; !reflect.DeepEqual(got, test.want) {
				t.Errorf("session = %+v, want %+v", got, test.want)
			}
		}
	})

	t.Run("ignores naming lines with empty or non-string values", func(t *testing.T) {
		state := NewTranscriptState()
		for _, line := range []map[string]any{
			{"type": "custom-title", "customTitle": ""},
			{"type": "ai-title", "aiTitle": 42},
			{"type": "custom-title"},
		} {
			if got := feed(state, line).Session; got != nil {
				t.Errorf("session = %+v, want nil", got)
			}
		}
	})

	t.Run("reads slug and gitBranch off ordinary entries alongside cwd and version", func(t *testing.T) {
		emit := feed(NewTranscriptState(), map[string]any{
			"type": "user", "timestamp": iso(t0), "cwd": "/w/app", "version": "2.0.0",
			"slug": "porting-the-router", "gitBranch": "feat/router",
		})
		want := &SessionMeta{
			ID: "s1", Cwd: strp("/w/app"), CcVersion: strp("2.0.0"),
			Slug: strp("porting-the-router"), Branch: strp("feat/router"),
		}
		if !reflect.DeepEqual(emit.Session, want) {
			t.Errorf("session = %+v, want %+v", emit.Session, want)
		}
	})

	t.Run("lets entry.sessionId route a naming line to that session", func(t *testing.T) {
		got := feed(NewTranscriptState(), map[string]any{
			"type": "custom-title", "customTitle": "Other", "sessionId": "zz",
		}).Session
		want := &SessionMeta{ID: "zz", CustomTitle: strp("Other")}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("session = %+v, want %+v", got, want)
		}
	})
}

func TestRemappedFile(t *testing.T) {
	jobMain := FileIdentity{
		SessionID: "zai:j1", AgentID: strp("j1"), Kind: shared.KindExternal,
		Upstream: strp("https://api.z.ai/api/anthropic"),
		Remap:    &Remap{SessionID: "zai:j1", AgentID: "j1"},
	}
	jobSub := FileIdentity{
		SessionID: "zai:j1", AgentID: strp("agent-w1"), Kind: shared.KindSubagent,
		Remap: &Remap{SessionID: "zai:j1", AgentID: "j1", ParentID: strp("j1")},
	}

	t.Run("lands every record on the job session and agent, whatever the lines carry", func(t *testing.T) {
		state := NewTranscriptState()
		first := feedFile(state, map[string]any{
			"type": "user", "timestamp": iso(t0), "sessionId": "att-1", "cwd": "/w/wt",
		}, jobMain)
		want := []AgentDescriptor{
			{SessionID: "zai:j1", ID: "j1", ParentID: strp("main"), Kind: shared.KindExternal},
		}
		if !reflect.DeepEqual(first.Agents, want) {
			t.Errorf("agents = %+v, want %+v", first.Agents, want)
		}
		emit := feedFile(state, assistantLine(assistantOverrides{
			timestamp: strp(iso(t1)), sessionID: strp("att-1"), requestID: strp("req-1"),
		}), jobMain)
		req := firstRequest(t, emit)
		if req.ID != "req-1" || req.SessionID != "zai:j1" || req.AgentID != "j1" ||
			req.Upstream != "https://api.z.ai/api/anthropic" {
			t.Errorf("request = %+v, want req-1 on zai:j1/j1 via api.z.ai", req)
		}
	})

	t.Run("keeps the job session's name, title and branch free of the attempt transcript's own", func(t *testing.T) {
		state := NewTranscriptState()
		if got := feedFile(state, map[string]any{
			"type": "custom-title", "customTitle": "Attempt", "sessionId": "att-1",
		}, jobMain).Session; got != nil {
			t.Errorf("session = %+v, want nil", got)
		}
		turn := feedFile(state, map[string]any{
			"type": "user", "timestamp": iso(t1), "sessionId": "att-1",
			"slug": "attempting", "gitBranch": "attempt",
		}, jobMain)
		if turn.Session != nil { // a naming line and slug/branch never rename a job session
			t.Errorf("session = %+v, want nil", turn.Session)
		}
		if _, ok := state.lastUserTs["zai:j1:j1"]; !ok { // the turn boundary still lands on the job agent
			t.Errorf("lastUserTs has no zai:j1:j1")
		}
	})

	t.Run("hangs a remapped subagent off the job agent and keeps its own id", func(t *testing.T) {
		state := NewTranscriptState()
		emit := feedFile(state, assistantLine(assistantOverrides{
			timestamp: strp(iso(t1)), sessionID: strp("att-1"),
		}), jobSub)
		want := []AgentDescriptor{
			{SessionID: "zai:j1", ID: "agent-w1", ParentID: strp("j1"), Kind: shared.KindSubagent},
		}
		if !reflect.DeepEqual(emit.Agents, want) {
			t.Errorf("agents = %+v, want %+v", emit.Agents, want)
		}
		if req := firstRequest(t, emit); req.SessionID != "zai:j1" || req.AgentID != "agent-w1" {
			t.Errorf("request = %+v, want it on zai:j1/agent-w1", req)
		}
	})
}

func TestHeadlessStreamTotalUsage(t *testing.T) {
	job := FileIdentity{
		SessionID: "zai:j1", AgentID: strp("j1"), Kind: shared.KindExternal,
		Upstream: strp("https://api.z.ai/api/anthropic"),
	}
	assistant := func(id string, ts int64, usage map[string]any) map[string]any {
		return map[string]any{
			"type": "assistant", "timestamp": iso(ts), "requestId": id,
			"message": map[string]any{"model": "glm-5.3-flash", "usage": usage, "content": []any{}},
		}
	}
	result := func(usage map[string]any) map[string]any {
		return map[string]any{"type": "result", "session_id": "x", "usage": usage}
	}
	zeroUsage := func() map[string]any {
		return map[string]any{"input_tokens": 0, "output_tokens": 0}
	}

	t.Run("puts the result's total on the last request, sent where the file's source says", func(t *testing.T) {
		state := NewTranscriptState()
		first := feedFile(state, assistant("r1", t0, zeroUsage()), job)
		if got := firstRequest(t, first).Upstream; got != "https://api.z.ai/api/anthropic" {
			t.Errorf("upstream = %q, want the zai api", got)
		}
		feedFile(state, assistant("r2", t1, zeroUsage()), job)
		end := feedFile(state, result(map[string]any{
			"input_tokens": 62_639, "output_tokens": 10_446, "cache_read_input_tokens": 857_600,
		}), job)
		if len(end.Requests) != 1 {
			t.Fatalf("requests = %d, want 1", len(end.Requests))
		}
		req := end.Requests[0]
		if req.ID != "r2" {
			t.Errorf("id = %q, want r2", req.ID)
		}
		wantTokens := shared.Tokens{Input: 62_639, Output: 10_446, CacheRead: 857_600}
		if !reflect.DeepEqual(req.Tokens, wantTokens) {
			t.Errorf("tokens = %+v, want %+v", req.Tokens, wantTokens)
		}
		// a second result (a retried attempt's) does not add the total twice
		if again := feedFile(state, result(map[string]any{
			"input_tokens": 5, "output_tokens": 5,
		}), job); len(again.Requests) != 0 {
			t.Errorf("requests = %+v, want none", again.Requests)
		}
	})

	t.Run("marks the request it fills as a run total, so the context alert can skip it", func(t *testing.T) {
		state := NewTranscriptState()
		feedFile(state, assistant("r1", t0, zeroUsage()), job)
		feedFile(state, assistant("r2", t1, zeroUsage()), job)
		end := feedFile(state, result(map[string]any{
			"input_tokens": 500, "output_tokens": 20,
		}), job)
		if len(end.Requests) != 1 || end.Requests[0].ID != "r2" || !end.Requests[0].Totals {
			t.Errorf("requests = %+v, want r2 marked as the run total", end.Requests)
		}
	})

	t.Run("leaves requests that reported their own usage alone, and ignores an empty or orphan result", func(t *testing.T) {
		state := NewTranscriptState()
		if got := feedFile(state, result(map[string]any{"input_tokens": 9}), job); !reflect.DeepEqual(got, TranscriptEmit{}) {
			t.Errorf("emit = %+v, want the empty emit (no request yet)", got)
		}
		feedFile(state, assistant("r1", t0, zeroUsage()), job)
		if got := feedFile(state, result(map[string]any{}), job); !reflect.DeepEqual(got, TranscriptEmit{}) {
			t.Errorf("emit = %+v, want the empty emit (nothing in the result either)", got)
		}
		feedFile(state, assistant("r2", t1, map[string]any{
			"input_tokens": 4, "output_tokens": 2,
		}), job)
		if got := feedFile(state, result(map[string]any{
			"input_tokens": 99, "output_tokens": 1,
		}), job); !reflect.DeepEqual(got, TranscriptEmit{}) {
			t.Errorf("emit = %+v, want the empty emit (r2 reported its own usage)", got)
		}
	})
}

func TestContentCapture(t *testing.T) {
	t.Run("captures a text prompt as the request's input and the answer's blocks as its output", func(t *testing.T) {
		state := NewTranscriptState()
		feed(state, map[string]any{
			"type": "user", "timestamp": iso(t0),
			"message": map[string]any{"content": "Fix the login bug"},
		})
		emit := feed(state, assistantLine(assistantOverrides{
			timestamp: strp(iso(t1)),
			content: []any{
				map[string]any{"type": "thinking", "text": "plan"},
				map[string]any{"type": "text", "text": "On it"},
				map[string]any{"type": "tool_use", "id": "tu1", "name": "Bash",
					"input": map[string]any{"command": "ls"}},
			},
		}))
		if len(emit.Content) != 1 {
			t.Fatalf("content = %d captures, want 1", len(emit.Content))
		}
		captured := emit.Content[0]
		if captured.RequestID != "req_9" {
			t.Errorf("requestId = %q, want req_9", captured.RequestID)
		}
		wantInput := []map[string]any{{"type": "text", "text": "Fix the login bug"}}
		if got := parseBlocks(t, captured.Input); !reflect.DeepEqual(got, wantInput) {
			t.Errorf("input = %+v, want %+v", got, wantInput)
		}
		wantOutput := []map[string]any{
			{"type": "thinking", "text": "plan"},
			{"type": "text", "text": "On it"},
			{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "ls"}},
		}
		if got := parseBlocks(t, captured.Output); !reflect.DeepEqual(got, wantOutput) {
			t.Errorf("output = %+v, want %+v", got, wantOutput)
		}
	})

	t.Run("feeds the next request the tool results the chain logged since the last answer", func(t *testing.T) {
		state := NewTranscriptState()
		feed(state, assistantLine(assistantOverrides{timestamp: strp(iso(t0))}))
		feed(state, map[string]any{
			"type": "user", "timestamp": iso(t1),
			"message": map[string]any{"content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "tu1",
					"content": []any{map[string]any{"type": "text", "text": "out"}}},
				map[string]any{"type": "tool_result", "tool_use_id": "tu2", "is_error": true,
					"content": "boom"},
			}},
		})
		emit := feed(state, assistantLine(assistantOverrides{
			timestamp: strp(iso(t2)), requestID: strp("r2"),
		}))
		wantInput := []map[string]any{
			{"type": "tool_result", "tool_use_id": "tu1", "text": "out"},
			{"type": "tool_result", "tool_use_id": "tu2", "is_error": true, "text": "boom"},
		}
		if got := parseBlocks(t, firstCapture(t, emit).Input); !reflect.DeepEqual(got, wantInput) {
			t.Errorf("input = %+v, want %+v", got, wantInput)
		}
		// consumed by that request: a second answer with no user entry between has no input
		again := feed(state, assistantLine(assistantOverrides{
			timestamp: strp(iso(t2 + 1)), requestID: strp("r3"),
		}))
		if got := firstCapture(t, again).Input; got != nil {
			t.Errorf("input = %v, want nil", got)
		}
		wantAgain := []map[string]any{
			{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "ls"}},
		}
		if got := parseBlocks(t, firstCapture(t, again).Output); !reflect.DeepEqual(got, wantAgain) {
			t.Errorf("output = %+v, want %+v", got, wantAgain)
		}
	})

	t.Run("gives a subagent message one request id and the tool results logged between its own blocks", func(t *testing.T) {
		state := NewTranscriptState()
		sub := FileIdentity{SessionID: "s1", AgentID: strp("agent-1"), Kind: shared.KindSubagent}
		// a subagent transcript carries no requestId and writes one entry per content block, tool
		// results landing between the blocks of the same message (shape of a real subagents/agent-*.jsonl)
		block := func(msgID, lineUUID string, ts int64, content []any) TranscriptEmit {
			return feedFile(state, map[string]any{
				"type": "assistant", "isSidechain": true, "uuid": lineUUID, "timestamp": iso(ts),
				"message": map[string]any{
					"id": msgID, "role": "assistant", "model": "claude-sonnet-5-5", "content": content,
				},
			}, sub)
		}
		toolResult := func(lineUUID string, ts int64, id, text string) TranscriptEmit {
			return feedFile(state, map[string]any{
				"type": "user", "isSidechain": true, "uuid": lineUUID, "timestamp": iso(ts),
				"message": map[string]any{"content": []any{
					map[string]any{"type": "tool_result", "tool_use_id": id,
						"content": []any{map[string]any{"type": "text", "text": text}}},
				}},
			}, sub)
		}
		feedFile(state, map[string]any{
			"type": "user", "timestamp": iso(t0), "message": map[string]any{"content": "list the files"},
		}, sub)
		firstBlock := block("msg_a", "aaaa0001", t1, []any{
			map[string]any{"type": "thinking", "text": "plan"},
		})
		block("msg_a", "aaaa0002", t1+1, []any{
			map[string]any{"type": "tool_use", "id": "tu1", "name": "Bash",
				"input": map[string]any{"command": "ls"}},
		})
		toolResult("rrrr0001", t1+2, "tu1", "out")
		feedFile(state, map[string]any{
			"type": "attachment", "isSidechain": true, "uuid": "tttt0001",
			"timestamp": iso(t1 + 3), "attachment": map[string]any{},
		}, sub)
		// the same response's later block arrives after that tool result
		lateBlock := block("msg_a", "aaaa0003", t1+4, []any{
			map[string]any{"type": "tool_use", "id": "tu2", "name": "Read",
				"input": map[string]any{"file_path": "/tmp/x"}},
		})
		toolResult("rrrr0002", t1+5, "tu2", "body")
		next := block("msg_b", "bbbb0001", t1+6, []any{
			map[string]any{"type": "text", "text": "Done"},
		})

		// every block of one API response is that one request, not a request of its own
		if got := firstCapture(t, firstBlock).RequestID; got != "msg_a" {
			t.Errorf("requestId = %q, want msg_a", got)
		}
		if got := firstRequest(t, lateBlock).ID; got != "msg_a" {
			t.Errorf("id = %q, want msg_a", got)
		}
		if got := firstRequest(t, next).ID; got != "msg_b" {
			t.Errorf("id = %q, want msg_b", got)
		}
		// its first block took the prompt logged before the message started
		wantPrompt := []map[string]any{{"type": "text", "text": "list the files"}}
		if got := parseBlocks(t, firstCapture(t, firstBlock).Input); !reflect.DeepEqual(got, wantPrompt) {
			t.Errorf("input = %+v, want %+v", got, wantPrompt)
		}
		// the late block took nothing: its tool result waits for the next request
		lateCapture := firstCapture(t, lateBlock)
		if got := lateCapture.Input; got != nil {
			t.Errorf("input = %v, want nil", got)
		}
		// both tool results, in order, though the first arrived between msg_a's own blocks
		wantResults := []map[string]any{
			{"type": "tool_result", "tool_use_id": "tu1", "text": "out"},
			{"type": "tool_result", "tool_use_id": "tu2", "text": "body"},
		}
		if got := parseBlocks(t, firstCapture(t, next).Input); !reflect.DeepEqual(got, wantResults) {
			t.Errorf("input = %+v, want %+v", got, wantResults)
		}
	})

	t.Run("keeps a thinking block's words, which the wire carries under `thinking`, not `text`", func(t *testing.T) {
		state := NewTranscriptState()
		emit := feed(state, assistantLine(assistantOverrides{
			timestamp: strp(iso(t1)),
			content: []any{
				map[string]any{"type": "thinking", "thinking": "secret plan text", "signature": "sig"},
				map[string]any{"type": "text", "text": "answer"},
			},
		}))
		want := []map[string]any{
			{"type": "thinking", "thinking": "secret plan text"},
			{"type": "text", "text": "answer"},
		}
		if got := parseBlocks(t, firstCapture(t, emit).Output); !reflect.DeepEqual(got, want) {
			t.Errorf("output = %+v, want %+v", got, want)
		}
	})

	t.Run("grows a streamed thinking block across its entries the way a text one grows", func(t *testing.T) {
		state := NewTranscriptState()
		feed(state, map[string]any{
			"type": "user", "timestamp": iso(t0), "message": map[string]any{"content": "hi"},
		})
		feed(state, assistantLine(assistantOverrides{
			timestamp: strp(iso(t1)),
			content:   []any{map[string]any{"type": "thinking", "thinking": "plan"}},
		}))
		emit := feed(state, assistantLine(assistantOverrides{
			timestamp: strp(iso(t1 + 1)),
			content:   []any{map[string]any{"type": "thinking", "thinking": "plan more"}},
		}))
		want := []map[string]any{{"type": "thinking", "thinking": "plan more"}}
		if got := parseBlocks(t, firstCapture(t, emit).Output); !reflect.DeepEqual(got, want) {
			t.Errorf("output = %+v, want %+v", got, want)
		}
	})

	t.Run("keeps a prompt's image as its media type and decoded size, never the data", func(t *testing.T) {
		state := NewTranscriptState()
		data := base64.StdEncoding.EncodeToString([]byte("png-bytes")) // 9 bytes, 12 base64 characters
		feed(state, map[string]any{
			"type": "user", "timestamp": iso(t0),
			"message": map[string]any{"content": []any{
				map[string]any{"type": "image", "source": map[string]any{
					"type": "base64", "media_type": "image/png", "data": data,
				}},
			}},
		})
		emit := feed(state, assistantLine(assistantOverrides{timestamp: strp(iso(t1))}))
		want := []map[string]any{{
			"type": "image", "media_type": "image/png", "bytes": float64(9),
			"text": "[image image/png, 9 bytes]",
		}}
		if got := parseBlocks(t, firstCapture(t, emit).Input); !reflect.DeepEqual(got, want) {
			t.Errorf("input = %+v, want %+v", got, want)
		}
	})

	t.Run("marks a tool result that only carried an image, instead of reading as empty", func(t *testing.T) {
		state := NewTranscriptState()
		data := base64.StdEncoding.EncodeToString(make([]byte, 32))
		feed(state, assistantLine(assistantOverrides{
			timestamp: strp(iso(t0)),
			content: []any{map[string]any{
				"type": "tool_use", "id": "tu1", "name": "Read", "input": map[string]any{"path": "x.png"},
			}},
		}))
		feed(state, map[string]any{
			"type": "user", "timestamp": iso(t1),
			"message": map[string]any{"content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "tu1",
					"content": []any{map[string]any{"type": "image", "source": map[string]any{
						"type": "base64", "media_type": "image/png", "data": data,
					}}}},
			}},
		})
		emit := feed(state, assistantLine(assistantOverrides{
			timestamp: strp(iso(t2)), requestID: strp("r2"),
		}))
		want := []map[string]any{
			{"type": "tool_result", "tool_use_id": "tu1", "text": "[image image/png, 32 bytes]"},
		}
		if got := parseBlocks(t, firstCapture(t, emit).Input); !reflect.DeepEqual(got, want) {
			t.Errorf("input = %+v, want %+v", got, want)
		}
	})

	t.Run("merges a streamed message's rewrites into one growing output, tool_use kept once", func(t *testing.T) {
		state := NewTranscriptState()
		feed(state, map[string]any{
			"type": "user", "timestamp": iso(t0), "message": map[string]any{"content": "hi"},
		})
		first := feed(state, assistantLine(assistantOverrides{
			timestamp: strp(iso(t1)), content: []any{map[string]any{"type": "text", "text": "Hel"}},
		}))
		wantHel := []map[string]any{{"type": "text", "text": "Hel"}}
		if got := parseBlocks(t, firstCapture(t, first).Output); !reflect.DeepEqual(got, wantHel) {
			t.Errorf("output = %+v, want %+v", got, wantHel)
		}
		second := feed(state, assistantLine(assistantOverrides{
			timestamp: strp(iso(t1 + 1)), content: []any{map[string]any{"type": "text", "text": "Hello"}},
		}))
		wantHello := []map[string]any{{"type": "text", "text": "Hello"}}
		if got := parseBlocks(t, firstCapture(t, second).Output); !reflect.DeepEqual(got, wantHello) {
			t.Errorf("output = %+v, want %+v", got, wantHello)
		}
		third := feed(state, assistantLine(assistantOverrides{
			timestamp: strp(iso(t1 + 2)),
			content: []any{
				map[string]any{"type": "text", "text": "Hello"},
				map[string]any{"type": "tool_use", "id": "tu1", "name": "Bash",
					"input": map[string]any{"command": "ls"}},
				map[string]any{"type": "tool_use", "id": "tu1", "name": "Bash",
					"input": map[string]any{"command": "ls"}},
			},
		}))
		wantThird := []map[string]any{
			{"type": "text", "text": "Hello"},
			{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "ls"}},
		}
		if got := parseBlocks(t, firstCapture(t, third).Output); !reflect.DeepEqual(got, wantThird) {
			t.Errorf("output = %+v, want %+v", got, wantThird)
		}
	})

	t.Run("keeps a 200 KB block whole, well under the 1 MB cap", func(t *testing.T) {
		state := NewTranscriptState()
		big := strings.Repeat("a", 200_000)
		feed(state, map[string]any{
			"type": "user", "timestamp": iso(t0), "message": map[string]any{"content": big},
		})
		emit := feed(state, assistantLine(assistantOverrides{
			timestamp: strp(iso(t1)), content: []any{map[string]any{"type": "text", "text": big}},
		}))
		input := parseBlocks(t, firstCapture(t, emit).Input)
		output := parseBlocks(t, firstCapture(t, emit).Output)
		if got := input[0]["text"]; got != big {
			t.Errorf("input text kept %d characters, want all %d", len(got.(string)), len(big))
		}
		if got := output[0]["text"]; got != big {
			t.Errorf("output text kept %d characters, want all %d", len(got.(string)), len(big))
		}
	})

	t.Run("caps a block past 1 MB with a marker naming the lost bytes, and records the cut for the UI", func(t *testing.T) {
		state := NewTranscriptState()
		huge := strings.Repeat("a", 1024*1024+4_464)
		feed(state, map[string]any{
			"type": "user", "timestamp": iso(t0), "message": map[string]any{"content": huge},
		})
		emit := feed(state, assistantLine(assistantOverrides{
			timestamp: strp(iso(t1)), content: []any{map[string]any{"type": "text", "text": huge}},
		}))
		for name, captured := range map[string]*string{
			"input":  firstCapture(t, emit).Input,
			"output": firstCapture(t, emit).Output,
		} {
			blocks := parseBlocks(t, captured)
			block := blocks[0]
			text, isText := block["text"].(string)
			if !isText || !strings.HasSuffix(text, "…[truncated 4464 bytes]") {
				t.Errorf("%s text = %v…, want it to end with the truncation marker", name, tailOf(text))
			}
			if got := block["truncated"]; got != float64(4_464) {
				t.Errorf("%s truncated = %v, want 4464", name, got)
			}
			if len(text) > 1024*1024+32 {
				t.Errorf("%s text is %d bytes, want at most 1 MB + 32", name, len(text))
			}
		}
	})
}

func tailOf(text string) string {
	if len(text) > 40 {
		return text[len(text)-40:]
	}
	return text
}

func TestTheWhatLine(t *testing.T) {
	defaultUser := func() map[string]any {
		return map[string]any{
			"type": "user", "timestamp": iso(t0),
			"message": map[string]any{"content": "Fix the login bug"},
		}
	}
	// whatOfContent feeds one user line and one assistant line; a nil user feeds no input.
	whatOfContent := func(user map[string]any, content []any) *string {
		state := NewTranscriptState()
		if user != nil {
			feed(state, user)
		}
		emit := feed(state, assistantLine(assistantOverrides{timestamp: strp(iso(t1)), content: content}))
		if len(emit.Requests) == 0 {
			return nil
		}
		return emit.Requests[0].What
	}

	t.Run("names the prompt's first 40 characters and the answer's tool calls", func(t *testing.T) {
		got := whatOfContent(defaultUser(), []any{map[string]any{"type": "text", "text": "On it"}})
		checkWhat(t, got, "↳ prompt: Fix the login bug  → text")
		got = whatOfContent(
			map[string]any{
				"type": "user", "timestamp": iso(t0),
				"message": map[string]any{"content": strings.Repeat("x", 60)},
			},
			[]any{
				map[string]any{"type": "text", "text": "On it"},
				map[string]any{"type": "tool_use", "id": "tu1", "name": "Bash",
					"input": map[string]any{"command": "git status --porcelain"}},
			},
		)
		checkWhat(t, got, "↳ prompt: "+strings.Repeat("x", 40)+"  → Bash git status")
	})

	t.Run("counts tool results on the input side", func(t *testing.T) {
		user := map[string]any{
			"type": "user", "timestamp": iso(t0),
			"message": map[string]any{"content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "tu1", "content": "out"},
				map[string]any{"type": "tool_result", "tool_use_id": "tu2", "content": "out"},
				map[string]any{"type": "tool_result", "tool_use_id": "tu3", "content": "out"},
			}},
		}
		got := whatOfContent(user, []any{map[string]any{"type": "text", "text": "Done"}})
		checkWhat(t, got, "↳ 3 tool results  → text")
	})

	t.Run("picks the telling argument per tool: file basename, command head, pattern", func(t *testing.T) {
		cases := []struct {
			content []any
			want    string
		}{
			{[]any{map[string]any{"type": "tool_use", "id": "tu1", "name": "Edit",
				"input": map[string]any{
					"file_path": "/w/app/src/store/store.ts", "old_string": "a", "new_string": "b",
				}}},
				"→ Edit store.ts"},
			{[]any{map[string]any{"type": "tool_use", "id": "tu1", "name": "Grep",
				"input": map[string]any{"pattern": "placeRequest", "path": "/w"}}},
				"→ Grep placeRequest"},
			{[]any{map[string]any{"type": "tool_use", "id": "tu1", "name": "NotebookEdit",
				"input": map[string]any{"notebook_path": "/w/n.ipynb"}}},
				"→ NotebookEdit n.ipynb"},
			// a tool whose input names nothing shows under its name alone
			{[]any{map[string]any{"type": "tool_use", "id": "tu1", "name": "TaskOutput",
				"input": map[string]any{"task_id": 7}}},
				"→ TaskOutput"},
		}
		for _, test := range cases {
			if got := whatOfContent(nil, test.content); got == nil || *got != test.want {
				t.Errorf("what = %v, want %q", got, test.want)
			}
		}
	})

	t.Run("lists at most three tool calls, then the count", func(t *testing.T) {
		read := func(id, path string) map[string]any {
			return map[string]any{"type": "tool_use", "id": id, "name": "Read",
				"input": map[string]any{"file_path": path}}
		}
		got := whatOfContent(nil, []any{
			read("t1", "/w/a.ts"), read("t2", "/w/b.ts"), read("t3", "/w/c.ts"), read("t4", "/w/d.ts"),
		})
		checkWhat(t, got, "→ Read a.ts, Read b.ts, Read c.ts +1")
	})

	t.Run("keeps the input part a streamed message's first entry computed", func(t *testing.T) {
		state := NewTranscriptState()
		feed(state, map[string]any{
			"type": "user", "timestamp": iso(t0),
			"message": map[string]any{"content": "Fix the login bug"},
		})
		first := feed(state, assistantLine(assistantOverrides{
			timestamp: strp(iso(t1)), content: []any{map[string]any{"type": "text", "text": "Working"}},
		}))
		checkWhat(t, firstRequest(t, first).What, "↳ prompt: Fix the login bug  → text")
		// the streamed rewrite carries no input of its own, and ends with a tool call
		second := feed(state, assistantLine(assistantOverrides{
			timestamp: strp(iso(t2)),
			content: []any{
				map[string]any{"type": "text", "text": "Working"},
				map[string]any{"type": "tool_use", "id": "tu9", "name": "Edit",
					"input": map[string]any{"file_path": "/w/app/auth.ts"}},
			},
		}))
		checkWhat(t, firstRequest(t, second).What, "↳ prompt: Fix the login bug  → Edit auth.ts")
	})

	t.Run("stays undefined when nothing was captured on either side", func(t *testing.T) {
		state := NewTranscriptState()
		emit := feed(state, assistantLine(assistantOverrides{
			timestamp: strp(iso(t1)), content: []any{},
		}))
		if got := firstRequest(t, emit).What; got != nil {
			t.Errorf("what = %v, want nil", got)
		}
	})
}

func TestShortHashAndToolInputKey(t *testing.T) {
	t.Run("hashes UTF-16 code units to 8 hex digits", func(t *testing.T) {
		cases := []struct {
			text string
			want string
		}{
			{"Bash", "a1bc273f"},
			{"a\U0001F600b", "8fca8501"}, // a surrogate pair counts as two units
			{"", "811c9dc5"},
		}
		for _, test := range cases {
			if got := ShortHash(test.text); got != test.want {
				t.Errorf("ShortHash(%q) = %s, want %s", test.text, got, test.want)
			}
		}
	})

	t.Run("keys a tool call by its name and its whole input, compacted", func(t *testing.T) {
		if got := ToolInputKey("Bash", json.RawMessage(`{ "command": "ls" }`)); got != "451dfa5d" {
			t.Errorf("ToolInputKey = %s, want 451dfa5d", got)
		}
		if got := ToolInputKey("Bash", nil); got != "da31c82d" {
			t.Errorf("ToolInputKey with no input = %s, want da31c82d", got)
		}
	})

	t.Run("reads an API error's status when it is one Claude Code retries", func(t *testing.T) {
		cases := []struct {
			text  string
			want  int64
			found bool
		}{
			{`API Error: 529 {"type":"error"}`, 529, true},
			{"API Error: 429", 429, true},
			{"API Error:   500 overloaded", 500, true},
			// The pattern's \s is JavaScript's, which U+00A0 and U+3000 belong to; bun:
			// /API Error:\s*(\d{3})/.exec("API Error:\u00a0529 {…}")[1] === "529"
			{"API Error:\u00a0529 {…}", 529, true},
			{"API Error:\u3000500", 500, true},
			{"API Error: 404 Not Found", 0, false},
			{"no error in here", 0, false},
		}
		for _, test := range cases {
			got, found := APIErrorStatus(test.text)
			if found != test.found || got != test.want {
				t.Errorf("APIErrorStatus(%q) = %d,%v want %d,%v", test.text, got, found, test.want, test.found)
			}
		}
	})
}

func TestAPIErrorsNoticesAndInterrupts(t *testing.T) {
	t.Run("reports an assistant error entry as an api error and a notice, never a request", func(t *testing.T) {
		state := NewTranscriptState()
		emit := feed(state, map[string]any{
			"type": "assistant", "timestamp": iso(t1), "isApiErrorMessage": true, "error": "rate_limit",
			"message": map[string]any{
				"role": "assistant", "model": SyntheticModel,
				"content": []any{map[string]any{"type": "text", "text": `API Error: 429 {"type":"error"}`}},
			},
		})
		wantErrors := []shared.APIErrorRecord{{
			Ts: t1, SessionID: strp("s1"), AgentID: strp("main"),
			Status: 429, Source: shared.SourceTranscript,
		}}
		if !reflect.DeepEqual(emit.APIErrors, wantErrors) {
			t.Errorf("apiErrors = %+v, want %+v", emit.APIErrors, wantErrors)
		}
		wantNotices := []Notice{{SessionID: "s1", AgentID: "main", Ts: t1, What: "usage limit"}}
		if !reflect.DeepEqual(emit.Notices, wantNotices) {
			t.Errorf("notices = %+v, want %+v", emit.Notices, wantNotices)
		}
		if len(emit.Requests) != 0 {
			t.Errorf("requests = %+v, want none", emit.Requests)
		}
	})

	t.Run("reads a synthetic model's api error without the error flag", func(t *testing.T) {
		state := NewTranscriptState()
		emit := feed(state, map[string]any{
			"type": "assistant", "timestamp": iso(t1),
			"message": map[string]any{
				"role": "assistant", "model": SyntheticModel,
				"content": []any{map[string]any{"type": "text", "text": "API Error: 503 bad gateway"}},
			},
		})
		if len(emit.APIErrors) != 1 || emit.APIErrors[0].Status != 503 {
			t.Errorf("apiErrors = %+v, want one 503", emit.APIErrors)
		}
		if len(emit.Notices) != 0 {
			t.Errorf("notices = %+v, want none without the error flag", emit.Notices)
		}
		if len(emit.Requests) != 0 {
			t.Errorf("requests = %+v, want none", emit.Requests)
		}
	})

	t.Run("names an unknown notice kind plainly", func(t *testing.T) {
		state := NewTranscriptState()
		emit := feed(state, map[string]any{
			"type": "assistant", "timestamp": iso(t1), "isApiErrorMessage": true, "error": "something_odd",
			"message": map[string]any{
				"role": "assistant", "model": SyntheticModel,
				"content": []any{map[string]any{"type": "text", "text": "API Error: 500"}},
			},
		})
		if len(emit.Notices) != 1 || emit.Notices[0].What != "error" {
			t.Errorf("notices = %+v, want one plain 'error'", emit.Notices)
		}
	})

	t.Run("reads a system retry line's status from its error block, or its own", func(t *testing.T) {
		state := NewTranscriptState()
		fromError := feed(state, map[string]any{
			"type": "system", "subtype": "api_error", "timestamp": iso(t1),
			"error": map[string]any{"status": 529},
		})
		if len(fromError.APIErrors) != 1 || fromError.APIErrors[0].Status != 529 {
			t.Errorf("apiErrors = %+v, want one 529", fromError.APIErrors)
		}
		fromEntry := feed(state, map[string]any{
			"type": "system", "subtype": "api_error", "timestamp": iso(t1), "status": 429,
		})
		if len(fromEntry.APIErrors) != 1 || fromEntry.APIErrors[0].Status != 429 {
			t.Errorf("apiErrors = %+v, want one 429", fromEntry.APIErrors)
		}
		// the error block's number wins even when nobody retries it: the entry's own is not read
		unretried := feed(state, map[string]any{
			"type": "system", "subtype": "api_error", "timestamp": iso(t1),
			"error": map[string]any{"status": 200}, "status": 503,
		})
		if len(unretried.APIErrors) != 0 {
			t.Errorf("apiErrors = %+v, want none", unretried.APIErrors)
		}
	})

	t.Run("marks a turn the user interrupted, from a string or a text block", func(t *testing.T) {
		state := NewTranscriptState()
		asString := feed(state, map[string]any{
			"type": "user", "timestamp": iso(t1),
			"message": map[string]any{"content": "[Request interrupted by user for tool use]"},
		})
		want := []Interrupt{{SessionID: "s1", AgentID: "main", Ts: t1}}
		if !reflect.DeepEqual(asString.Interrupts, want) {
			t.Errorf("interrupts = %+v, want %+v", asString.Interrupts, want)
		}
		asBlock := feed(state, map[string]any{
			"type": "user", "timestamp": iso(t2),
			"message": map[string]any{"content": []any{
				map[string]any{"type": "text", "text": "[Request interrupted by user]"},
			}},
		})
		wantBlock := []Interrupt{{SessionID: "s1", AgentID: "main", Ts: t2}}
		if !reflect.DeepEqual(asBlock.Interrupts, wantBlock) {
			t.Errorf("interrupts = %+v, want %+v", asBlock.Interrupts, wantBlock)
		}
	})
}

func TestLineEdgeShapes(t *testing.T) {
	t.Run("prints a numeric, boolean or object uuid into the fallback request id", func(t *testing.T) {
		state := NewTranscriptState()
		numbered := feed(state, map[string]any{
			"type": "assistant", "timestamp": iso(t1), "uuid": 42,
			"message": map[string]any{"role": "assistant", "model": "claude-sonnet-5-5", "content": []any{}},
		})
		if got := firstRequest(t, numbered).ID; got != "msg:42" {
			t.Errorf("id = %q, want msg:42", got)
		}
		boolean := feed(state, map[string]any{
			"type": "assistant", "timestamp": iso(t1), "uuid": true,
			"message": map[string]any{"role": "assistant", "model": "claude-sonnet-5-5", "content": []any{}},
		})
		if got := firstRequest(t, boolean).ID; got != "msg:true" {
			t.Errorf("id = %q, want msg:true", got)
		}
		nulled := feed(state, map[string]any{
			"type": "assistant", "timestamp": iso(t2), "uuid": nil,
			"message": map[string]any{"role": "assistant", "model": "claude-sonnet-5-5", "content": []any{}},
		})
		if got := firstRequest(t, nulled).ID; got != fmt.Sprintf("msg:%d", t2) {
			t.Errorf("id = %q, want msg:%d", got, t2)
		}
		object := feed(state, map[string]any{
			"type": "assistant", "timestamp": iso(t1), "uuid": map[string]any{"k": 1},
			"message": map[string]any{"role": "assistant", "model": "claude-sonnet-5-5", "content": []any{}},
		})
		if got := firstRequest(t, object).ID; got != "msg:"+`{"k":1}` {
			t.Errorf("id = %q, want the uuid's JSON text", got)
		}
	})

	t.Run("titles a subagent from the first block that carries words", func(t *testing.T) {
		state := NewTranscriptState()
		file := FileIdentity{
			SessionID: "s1", AgentID: strp("agent-1"), Kind: shared.KindSubagent,
			AgentMeta: &AgentMeta{AgentType: "general-purpose", Description: "anything"},
		}
		// whitespace-only text, a wordless block and a tool result name nothing
		quiet := feedFile(state, map[string]any{
			"type": "user", "timestamp": iso(t0),
			"message": map[string]any{"content": []any{
				map[string]any{"type": "text", "text": "   "},
				map[string]any{"type": "image"},
				map[string]any{"type": "tool_result", "tool_use_id": "x"},
			}},
		}, file)
		if len(quiet.Agents) != 1 {
			t.Fatalf("agents = %+v, want the descriptor alone", quiet.Agents)
		}
		titled := feedFile(state, map[string]any{
			"type": "user", "timestamp": iso(t1),
			"message": map[string]any{"content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "x"},
				map[string]any{"type": "text", "text": "the real task"},
			}},
		}, file)
		if len(titled.Agents) != 1 {
			t.Fatalf("agents = %+v, want the prompt title alone", titled.Agents)
		}
		if got := titled.Agents[0]; got.Title == nil || *got.Title != "the real task" {
			t.Errorf("title = %v, want 'the real task'", got.Title)
		}
		// null content names nothing either
		empty := feedFile(state, map[string]any{
			"type": "user", "timestamp": iso(t2),
			"message": map[string]any{"content": nil},
		}, file)
		if len(empty.Agents) != 0 {
			t.Errorf("agents = %+v, want none", empty.Agents)
		}
	})

	t.Run("skips content that is no array of objects, and a non-bool is_error", func(t *testing.T) {
		state := NewTranscriptState()
		feed(state, map[string]any{
			"type": "user", "timestamp": iso(t0),
			"message": map[string]any{"content": 42},
		})
		numeric := feed(state, assistantLine(assistantOverrides{timestamp: strp(iso(t1))}))
		if got := firstCapture(t, numeric).Input; got != nil {
			t.Errorf("input = %v, want nil (content was no array)", got)
		}
		mixed := feed(state, map[string]any{
			"type": "user", "timestamp": iso(t1),
			"message": map[string]any{"content": []any{
				7, "text", nil,
				map[string]any{"type": "text", "text": "kept"},
				map[string]any{"type": "tool_result", "tool_use_id": 9, "is_error": "yes"},
			}},
		})
		if len(mixed.ToolCalls) != 0 { // the tool_result's id is no string, the is_error no bool
			t.Errorf("tool calls = %+v, want none", mixed.ToolCalls)
		}
		emit := feed(state, assistantLine(assistantOverrides{
			timestamp: strp(iso(t2)), requestID: strp("r2"),
		}))
		wantInput := []map[string]any{
			{"type": "text", "text": "kept"},
			// the unusable tool_result still logs as an input block, bare of its id and flag
			{"type": "tool_result"},
		}
		if got := parseBlocks(t, firstCapture(t, emit).Input); !reflect.DeepEqual(got, wantInput) {
			t.Errorf("input = %+v, want %+v", got, wantInput)
		}
	})

	t.Run("counts an image's decoded bytes with either padding, or none when data is no string", func(t *testing.T) {
		state := NewTranscriptState()
		feed(state, map[string]any{
			"type": "user", "timestamp": iso(t0),
			"message": map[string]any{"content": []any{
				map[string]any{"type": "image", "source": map[string]any{
					"type": "base64", "media_type": "image/png", "data": "eA==", // 1 byte, double padding
				}},
				map[string]any{"type": "document", "source": map[string]any{"data": 5}},
				map[string]any{"type": "image"},
			}},
		})
		emit := feed(state, assistantLine(assistantOverrides{timestamp: strp(iso(t1))}))
		want := []map[string]any{
			{"type": "image", "media_type": "image/png", "bytes": float64(1),
				"text": "[image image/png, 1 bytes]"},
			{"type": "document", "text": "[document]"},
			{"type": "image", "text": "[image]"},
		}
		if got := parseBlocks(t, firstCapture(t, emit).Input); !reflect.DeepEqual(got, want) {
			t.Errorf("input = %+v, want %+v", got, want)
		}
	})

	t.Run("reads an over-large usage number as zero tokens", func(t *testing.T) {
		state := NewTranscriptState()
		emit := feed(state, `{"type":"assistant","timestamp":"`+iso(t1)+
			`","requestId":"r9","message":{"model":"claude-sonnet-5-5","usage":{"input_tokens":1e999}}}`)
		if got := firstRequest(t, emit).Tokens; !reflect.DeepEqual(got, shared.Tokens{}) {
			t.Errorf("tokens = %+v, want the zero set", got)
		}
	})

	t.Run("caps a multibyte block on a UTF-16 boundary that fits the byte cap", func(t *testing.T) {
		state := NewTranscriptState()
		huge := "éé" + strings.Repeat("a", 1048574+4464) // 1 053 042 bytes over 1 053 040 units
		feed(state, map[string]any{
			"type": "user", "timestamp": iso(t0), "message": map[string]any{"content": huge},
		})
		emit := feed(state, assistantLine(assistantOverrides{
			timestamp: strp(iso(t1)), content: []any{map[string]any{"type": "text", "text": huge}},
		}))
		blocks := parseBlocks(t, firstCapture(t, emit).Input)
		block := blocks[0]
		text, isText := block["text"].(string)
		if !isText || !strings.HasSuffix(text, "…[truncated 4466 bytes]") {
			t.Errorf("text ends with %q, want the 4466-byte marker", tailOf(text))
		}
		if got := block["truncated"]; got != float64(4466) {
			t.Errorf("truncated = %v, want 4466", got)
		}
		if len(text) > 1024*1024+32 {
			t.Errorf("text is %d bytes, want at most 1 MB + 32", len(text))
		}
	})

	t.Run("keeps a wordless text block and dedupes tool_use only on the same name", func(t *testing.T) {
		state := NewTranscriptState()
		emit := feed(state, assistantLine(assistantOverrides{
			timestamp: strp(iso(t1)),
			content: []any{
				map[string]any{"type": "text"},
				map[string]any{"type": "text", "text": "words"},
				map[string]any{"type": "tool_use", "id": "t1", "name": "A", "input": map[string]any{}},
				map[string]any{"type": "tool_use", "id": "t2", "input": map[string]any{}},
				map[string]any{"type": "tool_use", "id": "t3", "input": map[string]any{}},
			},
		}))
		want := []map[string]any{
			{"type": "text"}, // no words of its own, still a block
			{"type": "text", "text": "words"},
			{"type": "tool_use", "name": "A", "input": map[string]any{}},
			{"type": "tool_use", "input": map[string]any{}}, // t2 and t3 share no name and no input
		}
		if got := parseBlocks(t, firstCapture(t, emit).Output); !reflect.DeepEqual(got, want) {
			t.Errorf("output = %+v, want %+v", got, want)
		}
	})

	t.Run("keeps only the last 256 logged input blocks", func(t *testing.T) {
		state := NewTranscriptState()
		var results []any
		for at := 0; at < 300; at++ {
			results = append(results, map[string]any{
				"type": "tool_result", "tool_use_id": fmt.Sprintf("tu%d", at), "content": "out",
			})
		}
		feed(state, map[string]any{
			"type": "user", "timestamp": iso(t0),
			"message": map[string]any{"content": results},
		})
		emit := feed(state, assistantLine(assistantOverrides{timestamp: strp(iso(t1))}))
		got := parseBlocks(t, firstCapture(t, emit).Input)
		if len(got) != 256 {
			t.Fatalf("input holds %d blocks, want 256", len(got))
		}
		if first := got[0]["tool_use_id"]; first != "tu44" { // the oldest 44 went
			t.Errorf("first block = %v, want tu44", first)
		}
	})

	t.Run("cuts a command head and a pattern at the argument width", func(t *testing.T) {
		state := NewTranscriptState()
		emit := feed(state, assistantLine(assistantOverrides{
			timestamp: strp(iso(t1)),
			content: []any{
				map[string]any{"type": "tool_use", "id": "tu1", "name": "Bash",
					"input": map[string]any{"command": "extraordinarily-long-command --flag"}},
				map[string]any{"type": "tool_use", "id": "tu2", "name": "Grep",
					"input": map[string]any{"pattern": strings.Repeat("p", 30)}},
			},
		}))
		checkWhat(t, firstRequest(t, emit).What,
			"→ Bash extraordinarily-long-com, Grep "+strings.Repeat("p", 24))
	})

	t.Run("leaves the what's input side off when the prompt carried no words", func(t *testing.T) {
		state := NewTranscriptState()
		feed(state, map[string]any{
			"type": "user", "timestamp": iso(t0),
			"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "   "}}},
		})
		emit := feed(state, assistantLine(assistantOverrides{timestamp: strp(iso(t1))}))
		checkWhat(t, firstRequest(t, emit).What, "→ Bash ls")
	})
}

// stringifyBlocks prints JSON.stringify's bytes: `<`, `>`, `&` and the two line separators
// raw, so a stored capture reads back exactly as the wire carried it.
func TestStringifyBlocksKeepsJSONStringifyBytes(t *testing.T) {
	blocks := []ContentBlock{{Type: "text", Text: strp("a<b>c&d\u2028e\u2029f")}}
	printed := stringifyBlocks(blocks)
	if printed == nil {
		t.Fatal("stringifyBlocks = nil, want the blocks printed")
	}
	want := "[{\"type\":\"text\",\"text\":\"a<b>c&d\u2028e\u2029f\"}]"
	if *printed != want {
		t.Errorf("stringifyBlocks = %q, want %q", *printed, want)
	}
}
