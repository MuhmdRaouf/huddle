// Package shared holds radar's vocabulary: token counters, the record types the store
// keeps, and the view shapes the API serves. Records are plain data so the store can
// aggregate with pure functions and the UI can render from JSON alone.
package shared

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"unicode"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// ptr is the usual pointer-of helper.
func ptr[T any](value T) *T { return &value }

// Tokens are a request's four counters.
type Tokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
	// Thinking is the reasoning part of Output the provider breaks out
	// (usage.output_tokens_details); a subset, never added again — nil when the usage
	// block said nothing or said zero.
	Thinking *int64 `json:"thinking,omitempty"`
}

// ZeroTokens is the empty counter set.
var ZeroTokens = Tokens{}

// AddTokens sums two counters' four fields; thinking stays with its own request.
func AddTokens(a, b Tokens) Tokens {
	return Tokens{
		Input:      a.Input + b.Input,
		Output:     a.Output + b.Output,
		CacheRead:  a.CacheRead + b.CacheRead,
		CacheWrite: a.CacheWrite + b.CacheWrite,
	}
}

// NoticeEvent is the timeline event for an error notice Claude Code wrote itself (a usage
// limit, a lost connection).
const NoticeEvent = "Notice"

// OutsideSession is the session a router's request counts under when no Claude Code
// session sent it (a curl, a script).
const OutsideSession = "outside-a-session"

// UnattachedName is the name the unattached traffic shows under, in the live rail and as
// its History group.
const UnattachedName = "Unattached"

// RegistrySession is one entry of Claude Code's own session registry: a running
// interactive session, one file per pid.
type RegistrySession struct {
	PID       int64  `json:"pid"`
	SessionID string `json:"sessionId"`
	// Name is the session's display name, when someone named it (see NameSource).
	Name *string `json:"name"`
	// NameSource says who set the name ("user" wins over everything radar derived itself).
	NameSource *string `json:"nameSource"`
	// Status is what the session is doing right now ("busy", "idle", …).
	Status *string `json:"status"`
	// Cwd is where the session works, when the registry file says; a session ingest never
	// saw still gets one.
	Cwd *string `json:"cwd"`
	// StartedAt is when the session started, in epoch ms, when the registry file says.
	StartedAt *int64 `json:"startedAt"`
}

// Activity is the per-session activity sparkline: 48 buckets of request counts, each
// naming its most-seen model.
type Activity struct {
	BucketMs int64    `json:"bucketMs"`
	Counts   []int64  `json:"counts"`
	Models   []string `json:"models"`
}

// ActivityBuckets is how many buckets a sparkline holds, live or ended.
const ActivityBuckets = 48

// ActivityLiveMs is a live session's activity window: its last 15 minutes, so one bucket
// is 18,750 ms.
const ActivityLiveMs = 15 * 60_000

// ActivityLiveBucketMs is one live sparkline bucket.
const ActivityLiveBucketMs = ActivityLiveMs / ActivityBuckets

// MaxSessions caps the sessions the store keeps.
const MaxSessions = 200

// MaxRecords caps the records the store keeps.
const MaxRecords = 200_000

// USD is a dollar figure that serializes as JSON the way JavaScript prints the number.
type USD float64

// PromptTitle is the name-shaped head of a prompt: whitespace folded to single spaces,
// cut at 80 characters. The length and the cut count UTF-16 code units, the way the
// dashboard's own copy of the title does.
func PromptTitle(text string) string {
	var flat strings.Builder
	space := false
	for _, r := range text {
		if isJSSpace(r) {
			space = true
			continue
		}
		if space && flat.Len() > 0 {
			flat.WriteByte(' ')
		}
		space = false
		flat.WriteRune(r)
	}
	oneLine := strings.TrimFunc(flat.String(), isJSSpace)
	if jsscalar.UTF16Len(oneLine) <= 80 {
		return oneLine
	}
	return jsscalar.UTF16Slice(oneLine, 0, 80)
}

// isJSSpace reports whether r counts as whitespace in JavaScript (\s and trim): Go's
// space set minus NEL, plus the BOM.
func isJSSpace(r rune) bool {
	return r == 0xFEFF || (unicode.IsSpace(r) && r != 0x85)
}

// TotalTokens sums a request's four counters.
func TotalTokens(t Tokens) int64 {
	return t.Input + t.Output + t.CacheRead + t.CacheWrite
}

// HeaderPair is one response header a route line recorded: its name and its value.
type HeaderPair struct {
	Name  string
	Value string
}

// Headers is the response headers one route line recorded, as the pairs in the order the
// line carried them. It reads and writes the JSON object the line carried — arrival
// order, not alphabetical — so the stored row and the capture answer spell the headers
// exactly as the line did.
type Headers []HeaderPair

// Get answers the value one header name holds, the last one — the value a JSON parse
// keeps; false when the name is not held.
func (h Headers) Get(name string) (string, bool) {
	value, held := "", false
	for _, pair := range h {
		if pair.Name == name {
			value, held = pair.Value, true
		}
	}
	return value, held
}

// MarshalJSON encodes the headers as the JSON object the route line carried: the pairs
// in their own order, the strings written the way JSON.stringify writes them.
func (h Headers) MarshalJSON() ([]byte, error) {
	names := make([]string, len(h))
	values := make([]*jsfmt.Value, len(h))
	for at, pair := range h {
		names[at] = pair.Name
		values[at] = jsfmt.String(pair.Value)
	}
	return []byte(jsfmt.Object(names, values).EncodeJSON()), nil
}

// UnmarshalJSON reads the pairs in the order the object spells them, folding a repeated
// name the way a JSON parse does — its first position, its last value — and dropping the
// values that are not strings, the way every reader of the object guards the value it
// reads. Null reads as none; an object with no string values stays an empty one.
func (h *Headers) UnmarshalJSON(data []byte) error {
	value, err := jsfmt.DecodeValue(data)
	if err != nil {
		return err
	}
	switch value.Kind() {
	case jsfmt.KindNull:
		*h = nil
		return nil
	case jsfmt.KindObject:
	default:
		return &json.UnmarshalTypeError{Value: jsonKindName(value), Type: reflect.TypeOf(Headers{})}
	}
	out := Headers{}
	fields := value.Fields()
	for at, name := range value.Keys() {
		if at < len(fields) && fields[at].Kind() == jsfmt.KindString {
			out.add(name, fields[at].Text())
		}
	}
	*h = out
	return nil
}

// add folds one pair in: a name already held keeps its position and takes the later value.
func (h *Headers) add(name, value string) {
	for at, pair := range *h {
		if pair.Name == name {
			(*h)[at].Value = value
			return
		}
	}
	*h = append(*h, HeaderPair{Name: name, Value: value})
}

// The harnesses a request names. An empty Harness on a record reads as HarnessClaude: the
// Claude transcripts never tagged themselves, and the history column defaults to claude.
const (
	HarnessClaude   = "claude"
	HarnessCodex    = "codex"
	HarnessOpencode = "opencode"
)

// RequestRecord is one model request = one assistant message (streaming duplicates merged
// by requestId before this point).
type RequestRecord struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	AgentID   string `json:"agentId"`
	Model     string `json:"model"`
	Upstream  string `json:"upstream"`
	Ts        int64  `json:"ts"`
	LatencyMs *int64 `json:"latencyMs"`
	// TtfbMs is time to the answer's head (first response byte) and TtftMs time to its
	// first token, from the route line; nil when no router saw the call or it said none.
	TtfbMs     *int64  `json:"ttfbMs"`
	TtftMs     *int64  `json:"ttftMs"`
	Tokens     Tokens  `json:"tokens"`
	StopReason *string `json:"stopReason"`
	Provider   string  `json:"provider"`
	// Harness is the agent CLI that made the call ("codex", "opencode"); empty reads as
	// Claude Code and prints nothing.
	Harness string `json:"-"`
	// CostUSD is the cost the harness itself reported for the call; nil when radar prices
	// it from the token counts.
	CostUSD *float64 `json:"-"`
	// Cwd is where the harness ran, from its own session store; the watcher files it on
	// the session once, never printed on the request.
	Cwd *string `json:"-"`
	// Route is how a provider plugin's router sent it ("anthropic" passthrough,
	// "provider"); nil when no router saw it.
	Route *string `json:"route,omitempty"`
	// Via is the router it went through: the plugin's name ("zai") from its spool line,
	// or the router's address when only the session's base URL said so; nil for a direct
	// call. Upstream is where the router sent it.
	Via *string `json:"via,omitempty"`
	// Totals marks tokens as the run's total from its result event, not one call's
	// context; the context alert skips these.
	Totals bool `json:"totals,omitempty"`
	// Error is why a failed call failed: the provider's own one-line reason, or the
	// router's note on a dropped connection, from the route line it wrote. Nil on a
	// success and on a call no router saw.
	Error *string `json:"error,omitempty"`
	// ParentAgentID is the agent chain this call's agent hangs under, from a route line's
	// parent_agent_id; nil when the line did not say.
	ParentAgentID *string `json:"parentAgentId,omitempty"`
	// Fallback is the model the call asked for when its router's lane sent it to Model
	// instead, from the route line; nil for a call served as it asked.
	Fallback *string `json:"fallback,omitempty"`
	// CacheWrite1h is the part of CacheWrite the request wrote to the 1-hour cache; nil
	// when the usage block said nothing.
	CacheWrite1h *int64 `json:"cacheWrite1h,omitempty"`
	// Speed is how fast the provider ran it ("fast" bills fast-mode rates); nil when the
	// usage block said nothing.
	Speed *string `json:"speed,omitempty"`
	// Geo is the provider's inference region ("us" bills Claude at ×1.1); nil when the
	// usage block said nothing.
	Geo *string `json:"geo,omitempty"`
	// ServiceTier is the provider's service tier ("priority" bills MiniMax at ×1.5); nil
	// when the usage block said nothing.
	ServiceTier *string `json:"serviceTier,omitempty"`
	// What is one line on what the call was, computed at ingest from its captured
	// content: the input kind ("↳ prompt: …" / "↳ N tool results") and the output's tool
	// calls ("→ Edit a.ts, Bash git status") or "→ text", joined by two spaces. Nil when
	// nothing was captured.
	What *string `json:"what,omitempty"`
	// PromptHash is the hash the request's system prompt and tools sit under in the
	// history's captures table, from the route line; nil when the request carried neither
	// and no capture exists.
	PromptHash *string `json:"promptHash,omitempty"`
	// Headers are the allow-listed response headers the route line recorded, as the pairs
	// in the order the line carried them; nil when it recorded none.
	Headers *Headers `json:"headers,omitempty"`
	// Status is the HTTP status the request's route line recorded ("200", "429"), as the
	// wire answered; nil when no router saw the call or the line said none. Kept apart from
	// StopReason, which names how the model ended its turn — a 200 never was one.
	Status *string `json:"status,omitempty"`
	// BodyID is the id the router keys this request's kept body under in its own store, from
	// the route line; nil when no router saw the call or the line named none.
	BodyID *string `json:"-"`
	// Body says whether the router kept or dropped this request's body ("kept", "dropped"),
	// from the route line; nil when it said none.
	Body *string `json:"-"`
	// RequestID is the provider's own id for the call, the key both sources share: the
	// route line's request-id response header, the transcript entry's requestId. Empty when
	// the source named none; the store pairs on it and never prints it.
	RequestID string `json:"-"`

	// EarlyKeys lists the optional keys that ride between agentId and model, in order: a
	// route line's own error, parentAgentId, fallback, promptHash and headers. Empty for every
	// record not born from a route line.
	EarlyKeys []string `json:"-"`

	// TailKeys lists the optional keys after provider in the order the record gained them,
	// with the billing keys kept in their slot after tokens. Nil means the history read's
	// own fixed order, where every optional rides after provider and the billing keys
	// close the record.
	TailKeys []string `json:"-"`
}

// The optional keys, spelled as they print.
const (
	keyRoute        = "route"
	keyVia          = "via"
	keyStatus       = "status"
	keyHarness      = "harness"
	keyCostUsd      = "costUsd"
	keyTotals       = "totals"
	keyError        = "error"
	keyParentAgent  = "parentAgentId"
	keyFallback     = "fallback"
	keyCacheWrite1h = "cacheWrite1h"
	keySpeed        = "speed"
	keyGeo          = "geo"
	keyServiceTier  = "serviceTier"
	keyWhat         = "what"
	keyPromptHash   = "promptHash"
	keyHeaders      = "headers"
	keyBodyID       = "bodyId"
	keyBody         = "body"
)

// billingKeys sit between tokens and stopReason for a record the live side holds.
var billingKeys = []string{keyCacheWrite1h, keySpeed, keyGeo, keyServiceTier}

// historyKeys is the order a record read back out of the history answers its optionals in.
var historyKeys = []string{
	keyRoute, keyVia, keyStatus, keyHarness, keyCostUsd, keyWhat, keyError, keyParentAgent,
	keyFallback, keyPromptHash, keyHeaders, keyTotals, keyCacheWrite1h, keySpeed, keyGeo, keyServiceTier,
}

// MarshalJSON prints the record's keys in the wire's fixed order: the base fields first, and
// each optional key at its fixed place — a route line's own extras ride before model, the
// rest follow provider in the order they landed, and a history read keeps its single fixed
// tail.
func (r RequestRecord) MarshalJSON() ([]byte, error) {
	out := []byte{'{'}
	var failed error
	add := func(key string, value any) {
		if failed != nil {
			return
		}
		encoded, err := domain.MarshalCompactJSON(value)
		if err != nil {
			failed = err
			return
		}
		if len(out) > 1 {
			out = append(out, ',')
		}
		out = append(out, '"')
		out = append(out, key...)
		out = append(out, '"', ':')
		out = append(out, encoded...)
	}
	optional := func(key string) {
		switch key {
		case keyRoute:
			if r.Route != nil {
				add(keyRoute, *r.Route)
			}
		case keyVia:
			if r.Via != nil {
				add(keyVia, *r.Via)
			}
		case keyHarness:
			if r.Harness != "" && r.Harness != HarnessClaude {
				add(keyHarness, r.Harness)
			}
		case keyCostUsd:
			if r.CostUSD != nil {
				add(keyCostUsd, USD(*r.CostUSD))
			}
		case keyTotals:
			if r.Totals {
				add(keyTotals, true)
			}
		case keyError:
			if r.Error != nil {
				add(keyError, *r.Error)
			}
		case keyParentAgent:
			if r.ParentAgentID != nil {
				add(keyParentAgent, *r.ParentAgentID)
			}
		case keyFallback:
			if r.Fallback != nil {
				add(keyFallback, *r.Fallback)
			}
		case keyCacheWrite1h:
			if r.CacheWrite1h != nil {
				add(keyCacheWrite1h, *r.CacheWrite1h)
			}
		case keySpeed:
			if r.Speed != nil {
				add(keySpeed, *r.Speed)
			}
		case keyGeo:
			if r.Geo != nil {
				add(keyGeo, *r.Geo)
			}
		case keyServiceTier:
			if r.ServiceTier != nil {
				add(keyServiceTier, *r.ServiceTier)
			}
		case keyWhat:
			if r.What != nil {
				add(keyWhat, *r.What)
			}
		case keyPromptHash:
			if r.PromptHash != nil {
				add(keyPromptHash, *r.PromptHash)
			}
		case keyHeaders:
			if r.Headers != nil {
				add(keyHeaders, r.Headers)
			}
		case keyStatus:
			if r.Status != nil {
				add(keyStatus, *r.Status)
			}
		case keyBodyID:
			if r.BodyID != nil {
				add(keyBodyID, *r.BodyID)
			}
		case keyBody:
			if r.Body != nil {
				add(keyBody, *r.Body)
			}
		}
	}
	add("id", r.ID)
	add("sessionId", r.SessionID)
	add("agentId", r.AgentID)
	for _, key := range r.EarlyKeys {
		optional(key)
	}
	add("model", r.Model)
	add("upstream", r.Upstream)
	add("ts", r.Ts)
	add("latencyMs", r.LatencyMs)
	if r.TtfbMs != nil {
		add("ttfbMs", r.TtfbMs)
	}
	if r.TtftMs != nil {
		add("ttftMs", r.TtftMs)
	}
	add("tokens", r.Tokens)
	if r.TailKeys != nil {
		for _, key := range billingKeys {
			optional(key)
		}
	}
	add("stopReason", r.StopReason)
	add("provider", r.Provider)
	tail := r.TailKeys
	if tail == nil {
		tail = historyKeys
	}
	for _, key := range tail {
		optional(key)
	}
	if failed != nil {
		return nil, failed
	}
	return append(out, '}'), nil
}

// IsInFlight reports whether a request is still streaming: no stop reason yet and no
// output tokens — its numbers are the opening estimate, so nothing prices it and the views
// show it as still running until the final copy lands.
func IsInFlight(r *RequestRecord) bool {
	return r.StopReason == nil && r.Tokens.Output == 0
}

// CaptureRecord is one distinct prompt a router captured: its hash, its gzip bytes as
// base64 exactly as the spool line carried them, and when the first request carrying it
// was seen. Stored once, never re-expanded.
type CaptureRecord struct {
	Hash    string `json:"hash"`
	Gz      string `json:"gz"`
	FirstTs int64  `json:"firstTs"`
}

// ToolCallRecord is one tool call a hook saw.
type ToolCallRecord struct {
	ID         string  `json:"id"`
	SessionID  string  `json:"sessionId"`
	AgentID    *string `json:"agentId"`
	Name       string  `json:"name"`
	StartedAt  int64   `json:"startedAt"`
	DurationMs *int64  `json:"durationMs"`
	Ok         bool    `json:"ok"`
	// InputKey is a short hash of the tool's name and input, so the loop detector can see
	// the same call repeated.
	InputKey *string `json:"inputKey,omitempty"`
}

// APISource is where an API error was seen.
type APISource string

const (
	SourceTranscript APISource = "transcript"
	SourceRoute      APISource = "route"
	SourceRouter     APISource = "router"
)

// APIErrorRecord is a request that came back 429 or 5xx: from a transcript's API-error
// entry, a route line or a router event.
type APIErrorRecord struct {
	Ts        int64     `json:"ts"`
	SessionID *string   `json:"sessionId"`
	AgentID   *string   `json:"agentId"`
	Status    int64     `json:"status"`
	Source    APISource `json:"source"`
	// Plugin is the provider plugin whose router saw it (route/router sources), else null.
	Plugin *string `json:"plugin"`
}

// RouterEventKind is one kind of router health event.
type RouterEventKind string

const (
	EventFallback    RouterEventKind = "fallback"
	EventRefusal     RouterEventKind = "refusal"
	EventRateLimited RouterEventKind = "rate_limited"
	EventBudgetStop  RouterEventKind = "budget_stop"
	EventRestart     RouterEventKind = "restart"
	// EventUpstreamFailing and EventUpstreamRecovered are the Anthropic path's own kinds: the
	// path Claude Code's `claude-*` requests take failing and recovering.
	EventUpstreamFailing   RouterEventKind = "upstream_failing"
	EventUpstreamRecovered RouterEventKind = "upstream_recovered"
)

// RouterEventKinds lists every router health event kind.
var RouterEventKinds = []RouterEventKind{
	EventFallback, EventRefusal, EventRateLimited, EventBudgetStop, EventRestart,
	EventUpstreamFailing, EventUpstreamRecovered,
}

// RouterEventRecord is one router health event a provider plugin's router appended to the
// spool (never a key, never a body).
type RouterEventRecord struct {
	Ts     int64           `json:"ts"`
	Plugin string          `json:"plugin"`
	Event  RouterEventKind `json:"event"`
	Reason string          `json:"reason"`
	Model  *string         `json:"model"`
}

// EventRecord is one timeline event the store keeps.
type EventRecord struct {
	Seq       int64   `json:"seq"`
	Ts        int64   `json:"ts"`
	Kind      string  `json:"kind"`
	SessionID *string `json:"sessionId"`
	AgentID   *string `json:"agentId"`
	Label     *string `json:"label"`
	Payload   any     `json:"payload"`
}

// AgentKind is which kind of agent a view's row is.
type AgentKind string

const (
	KindMain     AgentKind = "main"
	KindSubagent AgentKind = "subagent"
	KindExternal AgentKind = "external"
)

// SessionStatus is whether a live session is working or idle, from Claude Code's registry.
type SessionStatus string

const (
	StatusWorking SessionStatus = "working"
	StatusIdle    SessionStatus = "idle"
)

// AgentView is one agent's row in a session card.
type AgentView struct {
	ID        string    `json:"id"`
	SessionID string    `json:"sessionId"`
	ParentID  *string   `json:"parentId"`
	Kind      AgentKind `json:"kind"`
	Name      *string   `json:"name"`
	// AgentType is what kind of agent ran (general-purpose, fork, …), from its subagents
	// meta file; null when unknown.
	AgentType *string `json:"agentType"`
	// Description is the agent's task, from the same meta file; null when unknown.
	Description *string `json:"description"`
	Model       *string `json:"model"`
	Requests    int64   `json:"requests"`
	Errors      int64   `json:"errors"`
	Tools       int64   `json:"tools"`
	Tokens      Tokens  `json:"tokens"`
	// CostUsd is estimated USD at list price; null when none of its models is priced.
	CostUsd *USD   `json:"costUsd"`
	Live    bool   `json:"live"`
	LastAt  *int64 `json:"lastAt"`
	// LatencyP95 is the agent's request latency at the 95th percentile; null under five
	// timed requests — too few for a percentile to say anything.
	LatencyP95 *int64 `json:"latencyP95"`
}

// SessionView is one session's card in the dashboard.
type SessionView struct {
	ID      string  `json:"id"`
	Cwd     *string `json:"cwd"`
	Project *string `json:"project"`
	// Name is the session's name: its custom title, agent name, AI title or slug; a job's
	// brief title.
	Name *string `json:"name"`
	// Branch is the git branch the session works on; a job's workspace branch.
	Branch *string `json:"branch"`
	// Repo is the repository the session works in; a job's workspace repo root.
	Repo *string `json:"repo"`
	// ParentSessionID is the session a job was submitted from; null for a plain session.
	ParentSessionID *string `json:"parentSessionId"`
	StartedAt       *int64  `json:"startedAt"`
	EndedAt         *int64  `json:"endedAt"`
	Live            bool    `json:"live"`
	// Status is whether the session is working or idle, from Claude Code's registry; null
	// when it cannot tell.
	Status *SessionStatus `json:"status"`
	// Activity is request activity in 48 buckets: a live session's last 15 minutes, an
	// ended one's whole span.
	Activity     Activity    `json:"activity"`
	Model        *string     `json:"model"`
	Upstream     string      `json:"upstream"`
	CcVersion    *string     `json:"ccVersion"`
	RequestCount int64       `json:"requestCount"`
	ErrorCount   int64       `json:"errorCount"`
	ToolCount    int64       `json:"toolCount"`
	Tokens       Tokens      `json:"tokens"`
	Agents       []AgentView `json:"agents"`
	// LiveAgentCount is how many of those agents are live right now: the same rule the
	// agents carry — a live parent and work (or a start) under five minutes old. Zero
	// hides the card's live badge.
	LiveAgentCount int64 `json:"liveAgentCount"`
	External       bool  `json:"external"`
}

// Summary is the dashboard's totals row.
type Summary struct {
	Sessions     int64  `json:"sessions"`
	LiveSessions int64  `json:"liveSessions"`
	Agents       int64  `json:"agents"`
	Requests     int64  `json:"requests"`
	Tokens       Tokens `json:"tokens"`
	Errors       int64  `json:"errors"`
	ToolCalls    int64  `json:"toolCalls"`
	LatencyP50   *int64 `json:"latencyP50"`
	LatencyP95   *int64 `json:"latencyP95"`
	StartedAt    *int64 `json:"startedAt"`
	Now          int64  `json:"now"`
	// CostUsd is estimated USD at list price for everything in view; null when nothing in
	// it is priced.
	CostUsd *USD `json:"costUsd"`
	// Utilization is Anthropic's unified 5-hour and 7-day limits as the newest route line
	// that carried them reported; nil until one did.
	Utilization *domain.UnifiedUtilization `json:"utilization,omitempty"`
}

// ModelRow is one model's row in the models table.
type ModelRow struct {
	Model      string `json:"model"`
	Provider   string `json:"provider"`
	Requests   int64  `json:"requests"`
	Errors     int64  `json:"errors"`
	Tokens     Tokens `json:"tokens"`
	LatencyP50 *int64 `json:"latencyP50"`
	CostUsd    *USD   `json:"costUsd"`
}

// UpstreamRow is one upstream's row in the upstreams table.
type UpstreamRow struct {
	Upstream string `json:"upstream"`
	Host     string `json:"host"`
	Requests int64  `json:"requests"`
	Tokens   Tokens `json:"tokens"`
	// CostUsd is estimated USD at list price; null when nothing sent there is priced.
	CostUsd *USD `json:"costUsd"`
}

// SpoolUsage is the usage block a route line carries, copied verbatim off the provider
// stream; anything past the four counters rides along unparsed, read as defensively as
// the transcript's own usage.
type SpoolUsage struct {
	InputTokens              *int64                     `json:"input_tokens,omitempty"`
	OutputTokens             *int64                     `json:"output_tokens,omitempty"`
	CacheCreationInputTokens *int64                     `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     *int64                     `json:"cache_read_input_tokens,omitempty"`
	Extra                    map[string]json.RawMessage `json:"-"`
}

// SpoolLine is a parsed spool line, as the hook wrote it. Unknown fields ride along in
// Extra, and the decoded line rides along whole: MarshalJSON hands back its bytes, so the
// payload the API serves is the line as read, every key in its own order.
type SpoolLine struct {
	Ts             string            `json:"ts"`
	Event          string            `json:"event"`
	SessionID      *string           `json:"session_id,omitempty"`
	Message        *string           `json:"message,omitempty"`
	TranscriptPath *string           `json:"transcript_path,omitempty"`
	Cwd            *string           `json:"cwd,omitempty"`
	AgentID        *string           `json:"agent_id,omitempty"`
	AgentType      *string           `json:"agent_type,omitempty"`
	ToolName       *string           `json:"tool_name,omitempty"`
	ToolUseID      *string           `json:"tool_use_id,omitempty"`
	ToolInput      any               `json:"tool_input,omitempty"`
	ToolResponse   any               `json:"tool_response,omitempty"`
	Prompt         *string           `json:"prompt,omitempty"`
	Source         *string           `json:"source,omitempty"`
	Reason         *string           `json:"reason,omitempty"`
	Trigger        *string           `json:"trigger,omitempty"`
	PID            *int64            `json:"pid,omitempty"`
	Ppid           *int64            `json:"ppid,omitempty"`
	BaseURL        *string           `json:"base_url,omitempty"`
	ModelEnv       map[string]string `json:"model_env,omitempty"`
	CcVersion      *string           `json:"cc_version,omitempty"`
	// Model is the model a router health line names; null when it said none.
	Model    *string `json:"model,omitempty"`
	Upstream *string `json:"upstream,omitempty"`
	Status   *string `json:"status,omitempty"`
	// Error is why an error answer or a dropped connection failed, as the route line's
	// reason carries it.
	Error *string `json:"error,omitempty"`
	// ParentAgentID is the agent chain the calling agent hangs under, as Claude Code
	// stamped the forwarded request.
	ParentAgentID *string `json:"parent_agent_id,omitempty"`
	// Fallback is the model the request asked for when the router's lane sent it to Model
	// instead.
	Fallback *string `json:"fallback,omitempty"`
	// PromptHash is the hash a route line names (its capture sits under it); a capture
	// line carries the hash again with the gzipped content.
	PromptHash *string  `json:"prompt_hash,omitempty"`
	Headers    *Headers `json:"headers,omitempty"`
	// BodyID is the id the router keys the request's kept body under; Body says whether it
	// kept or dropped the body ("kept", "dropped").
	BodyID    *string `json:"body_id,omitempty"`
	Body      *string `json:"body,omitempty"`
	Gz        *string `json:"gz,omitempty"`
	LatencyMs *int64  `json:"latency_ms,omitempty"`
	// TtfbMs is time to the answer's head and TtftMs time to its first token, both as the
	// route line carried them.
	TtfbMs *int64      `json:"ttfb_ms,omitempty"`
	TtftMs *int64      `json:"ttft_ms,omitempty"`
	Usage  *SpoolUsage `json:"usage,omitempty"`
	// Utilization is the unified limits a route line read from Anthropic's answer headers.
	Utilization *domain.UnifiedUtilization `json:"utilization,omitempty"`
	Extra       map[string]json.RawMessage `json:"-"`
	// source is the line as decoded: every key it arrived with, in arrival order. A line
	// built in code carries none and marshals its declared fields instead.
	source *jsfmt.Value
}

// RouteEvent is one request a router's route line recorded.
type RouteEvent struct {
	Ts        int64   `json:"ts"`
	SessionID *string `json:"sessionId"`
	Model     string  `json:"model"`
	Upstream  string  `json:"upstream"`
	Status    *string `json:"status"`
	LatencyMs *int64  `json:"latencyMs"`
	Tokens    Tokens  `json:"tokens"`
}

// MarshalJSON prints the figure exactly as JavaScript's String(n), so the dashboard's
// JavaScript reads back the same number.
func (u USD) MarshalJSON() ([]byte, error) {
	return []byte(jsscalar.JSNumber(float64(u))), nil
}

// UnmarshalJSON fills the line, parking every key its fields do not name in Extra and
// keeping the decoded line for MarshalJSON. Each declared field decodes on its own: one
// field of another shape reads as absent and never costs the line, the way every reader
// guards the value it reads.
func (l *SpoolLine) UnmarshalJSON(data []byte) error {
	value, err := jsfmt.DecodeValue(data)
	if err != nil {
		return err
	}
	if value.Kind() == jsfmt.KindNull {
		*l = SpoolLine{}
		return nil
	}
	if value.Kind() != jsfmt.KindObject {
		return &json.UnmarshalTypeError{Value: jsonKindName(value), Type: reflect.TypeOf(SpoolLine{})}
	}
	line := SpoolLine{source: value}
	if err := fillDeclaredFields(&line, value); err != nil {
		return err
	}
	*l = line
	return nil
}

// MarshalJSON returns the line's own bytes when it came off the spool — every key it
// carried, in the order it carried them, numbers as they printed — and its declared
// fields, in the line's field order, when it was built in code.
func (l SpoolLine) MarshalJSON() ([]byte, error) {
	if l.source != nil {
		return []byte(l.source.EncodeJSON()), nil
	}
	type plain SpoolLine
	return domain.MarshalCompactJSON(plain(l))
}

// Field is the line's own value for a key, as read: the shape a declared field of another
// kind keeps in the payload while its typed field reads absent. The key's last value is the
// one a JSON parse keeps; nil for a key the line did not carry and for a line built in code.
func (l SpoolLine) Field(key string) *jsfmt.Value {
	if l.source == nil {
		return nil
	}
	keys, fields := l.source.Keys(), l.source.Fields()
	for i := len(keys) - 1; i >= 0; i-- {
		if keys[i] == key {
			return fields[i]
		}
	}
	return nil
}

// fillDeclaredFields reads the object into the line's declared fields, each on its own,
// and parks every key the line does not name in Extra, re-encoded the way the line itself
// re-encodes.
func fillDeclaredFields(line *SpoolLine, object *jsfmt.Value) error {
	target := reflect.ValueOf(line).Elem()
	structType := target.Type()
	declared := make(map[string]reflect.Value, structType.NumField())
	for i := range structType.NumField() {
		field := structType.Field(i)
		if !field.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		declared[name] = target.Field(i)
	}
	keys, values := object.Keys(), object.Fields()
	var extra map[string]json.RawMessage
	for i, key := range keys {
		if dest, ok := declared[key]; ok {
			if err := decodeField(values[i], dest); err != nil {
				return err
			}
			continue
		}
		if extra == nil {
			extra = make(map[string]json.RawMessage)
		}
		extra[key] = json.RawMessage(values[i].EncodeJSON())
	}
	line.Extra = extra
	return nil
}

// decodeField reads one of the object's values into its field; a value of another shape
// leaves the field unset (the decoder allocates the field before it fails, so the failed
// decode is undone here).
func decodeField(value *jsfmt.Value, dest reflect.Value) error {
	err := json.Unmarshal([]byte(value.EncodeJSON()), dest.Addr().Interface())
	if err == nil {
		return nil
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		dest.Set(reflect.Zero(dest.Type()))
		return nil
	}
	return err
}

// jsonKindName names a value's shape the way encoding/json's own errors do.
func jsonKindName(value *jsfmt.Value) string {
	switch value.Kind() {
	case jsfmt.KindBool:
		return "bool"
	case jsfmt.KindNumber:
		return "number"
	case jsfmt.KindString:
		return "string"
	case jsfmt.KindArray:
		return "array"
	default:
		return "value"
	}
}

// UnmarshalJSON fills the usage block one counter at a time, the way the readers on the other
// side each guard their own: a JSON number whose value is whole sets its counter (12.0 and 1e3
// are 12 and 1000), anything else — a string, an object, null, a fraction — leaves that one
// counter unset while the rest still decode. Every key the block does not name rides along in
// Extra.
func (u *SpoolUsage) UnmarshalJSON(data []byte) error {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return err
	}
	*u = SpoolUsage{
		InputTokens:              wholeNumber(all["input_tokens"]),
		OutputTokens:             wholeNumber(all["output_tokens"]),
		CacheCreationInputTokens: wholeNumber(all["cache_creation_input_tokens"]),
		CacheReadInputTokens:     wholeNumber(all["cache_read_input_tokens"]),
	}
	for _, key := range usageCounterKeys {
		delete(all, key)
	}
	if len(all) == 0 {
		return nil
	}
	u.Extra = all
	return nil
}

// usageCounterKeys are the four usage keys the block names itself.
var usageCounterKeys = []string{
	"input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens",
}

// wholeNumber reads one usage counter: a JSON number whose value is whole sets it, anything
// else leaves it unset. The number decodes as the double JavaScript reads it as, so a whole
// float (12.0) or an exponent (1e3) counts as the whole value it prints as.
func wholeNumber(raw json.RawMessage) *int64 {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	number, ok := value.(float64)
	if !ok || number != math.Trunc(number) || number < math.MinInt64 || number >= math.MaxInt64 {
		return nil
	}
	counter := int64(number)
	return &counter
}
