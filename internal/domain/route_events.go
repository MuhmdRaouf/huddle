// What a plugin's router writes to Radar's spool: one event per forwarded request, one per router start or
// stop, a health event when something went other than plainly, and one capture event per distinct prompt —
// the request's own system prompt and tool definitions, gzipped. A route event stays metadata (model, route,
// status, latency, usage, the three ids Claude Code sends, the hash its prompt capture sits under, the
// allow-listed response headers and the unified limits read from them); no other header value and no message body
// ever lands here.
package domain

import "strconv"

// RouteUsage is the usage counters an answer carries, as the provider or Anthropic reports them.
type RouteUsage map[string]any

// The `event` field each spool line carries.
const (
	EventRoute   = "route"
	EventCapture = "capture"
	EventRouter  = "router"
)

// RouteEvent is one forwarded request, appended after it completes.
type RouteEvent struct {
	Ts       string `json:"ts"`
	Event    string `json:"event"`
	Plugin   string `json:"plugin"`
	Model    string `json:"model"`
	Upstream string `json:"upstream"`
	Route    string `json:"route"`
	Status   int64  `json:"status"`

	// Error is why the upstream answered an error, when it did and the router read it (provider routes only): one
	// line, the provider's own `type: message` (or `code: msg`) or the first 300 characters of its text, credentials
	// redacted. Never a header value, a request byte, or a success answer's anything.
	Error     *string `json:"error,omitempty"`
	LatencyMs int64   `json:"latency_ms"`
	// TtfbMs and TtftMs are how long the answer's head and its first token took from the moment the
	// router received the request: nil when the answer never came, and TtftMs nil as well when the
	// answer streamed no content_block_delta (a JSON answer, an error, an empty stream).
	TtfbMs *int64 `json:"ttfb_ms,omitempty"`
	TtftMs *int64 `json:"ttft_ms,omitempty"`
	// SessionID, AgentID and ParentAgentID are the ids Claude Code sends with the request.
	SessionID     *string            `json:"session_id,omitempty"`
	AgentID       *string            `json:"agent_id,omitempty"`
	ParentAgentID *string            `json:"parent_agent_id,omitempty"`
	Usage         *RouteUsage        `json:"usage,omitempty"`
	Headers       *map[string]string `json:"headers,omitempty"`
	PromptHash    *string            `json:"prompt_hash,omitempty"`
	// PromptID, PrevRequest and TurnIndex are the billing block's cc_prompt_id, cc_prev_req and
	// cc_turn_index: the block opens Claude Code's system prompt, changes on every request, and so
	// stays out of the prompt hash.
	PromptID    *string `json:"prompt_id,omitempty"`
	PrevRequest *string `json:"prev_request,omitempty"`
	TurnIndex   *int64  `json:"turn_index,omitempty"`
	// Harness names the harness that sent the request as its own header said; only the huddle
	// gateway writes it, a router has no way to know.
	Harness string `json:"harness,omitempty"`
	// Via names the forwarder on the line itself; the routers leave it unset and radar reads
	// their plugin as the via.
	Via string `json:"via,omitempty"`
	// Fallback is the model the request asked for when the lane sent it to Model instead,
	// because the model asked for answered three failures in a row; nil for a request served
	// as it asked.
	Fallback *string `json:"fallback,omitempty"`
	// Utilization is Anthropic's unified 5-hour and 7-day limits as a claude-* answer's headers
	// reported them; nil when the answer carried neither window.
	Utilization *UnifiedUtilization `json:"utilization,omitempty"`
}

// UnifiedUtilization is the two windows of Anthropic's unified subscription limits, each nil
// when the answer did not report it.
type UnifiedUtilization struct {
	FiveHour *UnifiedWindow `json:"five_hour,omitempty"`
	SevenDay *UnifiedWindow `json:"seven_day,omitempty"`
}

// UnifiedWindow is one window: the share used, 0 to 1 as the header carries it, and when the
// window resets, unix seconds; nil when the answer named no reset.
type UnifiedWindow struct {
	Used     float64 `json:"used"`
	ResetsAt *int64  `json:"resets_at,omitempty"`
}

// UnifiedUtilizationOf reads the unified windows from an answer's captured headers (names
// lowercased); nil when neither window's share is a number.
func UnifiedUtilizationOf(headers map[string]string) *UnifiedUtilization {
	window := func(name string) *UnifiedWindow {
		used, err := strconv.ParseFloat(headers["anthropic-ratelimit-unified-"+name+"-utilization"], 64)
		if err != nil {
			return nil
		}
		out := &UnifiedWindow{Used: used}
		if reset, err := strconv.ParseInt(headers["anthropic-ratelimit-unified-"+name+"-reset"], 10, 64); err == nil {
			out.ResetsAt = &reset
		}
		return out
	}
	utilization := UnifiedUtilization{FiveHour: window("5h"), SevenDay: window("7d")}
	if utilization.FiveHour == nil && utilization.SevenDay == nil {
		return nil
	}
	return &utilization
}

// CaptureEvent is one distinct prompt's content, appended once per hash per router process: gzip of the JSON the
// request carried at its top level — its `system` and `tools` and nothing else — base64. Written off the request's
// turn of the event loop, so the answer never waits on it.
type CaptureEvent struct {
	Ts         string `json:"ts"`
	Event      string `json:"event"`
	Plugin     string `json:"plugin"`
	PromptHash string `json:"prompt_hash"`
	Gz         string `json:"gz"`
}

// RouterState is the state a router event reports: coming up, or shutting down cleanly.
type RouterState string

const (
	RouterStateStart RouterState = "start"
	RouterStateStop  RouterState = "stop"
)

// RouterEvent is a router coming up or shutting down cleanly.
type RouterEvent struct {
	Ts      string      `json:"ts"`
	Event   string      `json:"event"`
	Plugin  string      `json:"plugin"`
	Port    int64       `json:"port"`
	State   RouterState `json:"state"`
	Version int64       `json:"version"`
}

// RouterHealth is what a router's health event reports: a request the passthrough served instead of the router, a
// provider request the router refused (no key, a refused key, no balance, a stopped budget), a provider rate limit, a
// budget turning a route off, or a worker the front had to replace (and the router retiring: the last line a retired
// router writes), or the Anthropic path failing and recovering.
type RouterHealth string

const (
	HealthFallback    RouterHealth = "fallback"
	HealthRefusal     RouterHealth = "refusal"
	HealthRateLimited RouterHealth = "rate_limited"
	HealthBudgetStop  RouterHealth = "budget_stop"
	HealthRestart     RouterHealth = "restart"
	// HealthUpstreamFailing and HealthUpstreamRecovered mark the Anthropic path (`claude-*`
	// requests): consecutive upstream failures raise one failing event, the first success after
	// it raises the recovery. The reason carries the failure code (ECERT, ECONNREFUSED, …).
	HealthUpstreamFailing   RouterHealth = "upstream_failing"
	HealthUpstreamRecovered RouterHealth = "upstream_recovered"
)

// RouterHealthEvents lists the health events a router reports.
var RouterHealthEvents = []RouterHealth{
	HealthFallback,
	HealthRefusal,
	HealthRateLimited,
	HealthBudgetStop,
	HealthRestart,
	HealthUpstreamFailing,
	HealthUpstreamRecovered,
}

// FailHint is the fix a failure code's health event and alert name: what to check first.
func FailHint(code string) string {
	switch code {
	case "ECERT":
		return "a certificate check failed; huddle-router never routes through another router, so restart sessions started before an update"
	case "ECONNREFUSED":
		return "the upstream refused the connection; check HTTPS_PROXY"
	case "ETIMEDOUT":
		return "the upstream timed out; check the network"
	default:
		return "see the router log"
	}
}

// HealthEventKind marks a router health line (Radar's `router.event`).
const HealthEventKind = "router.event"

// RouterHealthEvent is one router health event: what happened and why, the model the request named when there was
// one, epoch milliseconds. Never a key, a header value or a byte of any body.
type RouterHealthEvent struct {
	Kind   string       `json:"kind"`
	Plugin string       `json:"plugin"`
	Event  RouterHealth `json:"event"`
	Reason string       `json:"reason"`
	Model  *string      `json:"model"`
	Ts     int64        `json:"ts"`
}

// HealthEvent builds a health event, a model of "" (none named) recorded as null.
func HealthEvent(plugin string, event RouterHealth, reason string, model string, ts int64) RouterHealthEvent {
	health := RouterHealthEvent{
		Kind:   HealthEventKind,
		Plugin: plugin,
		Event:  event,
		Reason: reason,
		Ts:     ts,
	}
	if model != "" {
		health.Model = &model
	}
	return health
}

// SpoolEvent is any event a plugin's router appends to Radar's spool.
type SpoolEvent interface {
	spoolEvent()
}

func (RouteEvent) spoolEvent() {}

func (CaptureEvent) spoolEvent() {}

func (RouterEvent) spoolEvent() {}

func (RouterHealthEvent) spoolEvent() {}

// IsHealthEvent reports whether the event is a router health event; route and router events are told apart by their
// `event` field.
func IsHealthEvent(event SpoolEvent) bool {
	switch event.(type) {
	case RouterHealthEvent, *RouterHealthEvent:
		return true
	}
	return false
}
