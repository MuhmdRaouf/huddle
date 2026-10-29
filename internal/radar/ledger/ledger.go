// Package ledger keeps one record per model request, whichever lane served it: the router's
// route event is the complete source, the transcript gives the request its place in a turn,
// and OTel is a second opinion on timing and cost. Each field reads from the first source
// that has it.
package ledger

import "github.com/MuhmdRaouf/huddle/internal/domain"

// Row is one request's record, the shape of the ledger_requests table.
type Row struct {
	ID                                   string // the router's id for the request: the request-id header when the answer carried one, else the prompt hash joined to the ts
	AnthropicID                          string // the request-id header (req_…), "" when the answer never carried one
	StartedAt                            int64  // ms since the epoch, the moment the router received the request
	TtfbMs                               *int64
	TtftMs                               *int64
	DurationMs                           int64
	Lane                                 string // "anthropic" | "provider:<plugin>" | "gateway:<harness>"
	Harness                              string // "claude" when the route event names none
	Model                                string
	FallbackFrom                         string // the model asked for when the lane served another; "" otherwise
	Upstream                             string
	SessionID                            string
	AgentID                              string
	ParentAgentID                        string
	PromptID                             string // the route billing block's prompt id, else the transcript's promptId
	TurnIndex                            *int64
	PrevRequest                          string
	QuerySource                          string // the transcript's api-request query source; "" when no transcript saw the request
	Outcome                              string // "ok" | "http_error" | "upstream_failed" | "cancelled" | "refused"
	Status                               int64
	ErrorCode                            string
	StopReason                           string
	Input, Output, CacheRead, CacheWrite int64
	CacheWrite1h                         *int64            // the part of CacheWrite the 1-hour cache holds, from the usage block's cache_creation.ephemeral_1h_input_tokens; nil when it said nothing
	Speed                                *string           // how fast the provider ran it, from the usage block's speed; nil when it said nothing
	Geo                                  *string           // the provider's inference region, from the usage block's inference_geo; nil when it said nothing
	ServiceTier                          *string           // the provider's service tier, from the usage block's service_tier; nil when it said nothing
	Provider                             string            // the provider the model belongs to, the route's plugin naming it when the model is unknown; "" when no source names one
	Via                                  *string           // the router that served it: the route line's plugin, else "router"; nil when no route line saw the request
	What                                 *string           // one line on what the call was, as the transcript computed it at ingest; nil when no transcript captured one
	Thinking                             *int64            // the reasoning tokens the chosen usage's output_tokens_details breaks out; nil when it said nothing
	Headers                              map[string]string // the allow-listed response headers the route line recorded; nil when it recorded none
	Totals                               bool              // the tokens are the run's total from its result event, not one call's context
	NodeID                               string            // the node the request lands on, as history spells it: the transcript's naming when a transcript saw the request, else the route's; "" when no source could name it
	UsageSource                          string            // "router" | "otel" | "transcript"
	CostUSD                              float64           // Radar's price, as the caller priced the usage; 0 when unpriced
	CostReportedUSD                      *float64          // OTel's own cost_usd, shown beside Radar's when known
	Ratelimit5h                          *float64
	Ratelimit7d                          *float64
	PromptHash                           string
	InTranscript                         bool
}

// RouteFacts is what the router's route event says about the request.
type RouteFacts struct {
	Event   domain.RouteEvent `json:"event"`             // as the spool line decodes
	CostUSD float64           `json:"cost_usd"`          // Radar's price for the event's usage, 0 when the caller has none
	NodeID  string            `json:"node_id,omitempty"` // the node the watcher computed at ingest, by the history writer's rule
}

// TranscriptFacts is what Claude Code's transcript says about the same request.
type TranscriptFacts struct {
	RequestID   string            `json:"request_id"` // requestId, joins on Row.AnthropicID
	Model       string            `json:"model"`      // the assistant line's model, the provider read off it when no route saw the request
	PromptID    string            `json:"prompt_id"`
	QuerySource string            `json:"query_source"`
	StopReason  string            `json:"stop_reason"`
	Usage       domain.RouteUsage `json:"usage"`             // the assistant line's final usage
	What        *string           `json:"what,omitempty"`    // the what line the transcript computed at ingest
	Totals      bool              `json:"totals,omitempty"`  // the tokens are the run's total from its result event
	NodeID      string            `json:"node_id,omitempty"` // the node the watcher computed at ingest, by the history writer's rule
	Via         *string           `json:"via,omitempty"`     // the way the call left, when the session's hooks reported a loopback base URL for it
}

// OTelFacts is what an OTel api_request or api_error event says about it.
type OTelFacts struct {
	RequestID  string
	DurationMs int64
	TtftMs     *int64
	CostUSD    *float64
	Usage      domain.RouteUsage
	ErrorCode  string // api_error only
}

// Sources is everything known about one request; a nil source knows nothing.
type Sources struct {
	Route      *RouteFacts
	Transcript *TranscriptFacts
	OTel       *OTelFacts
}
