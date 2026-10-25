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
	UsageSource                          string   // "router" | "otel" | "transcript"
	CostUSD                              float64  // Radar's price, as the caller priced the usage; 0 when unpriced
	CostReportedUSD                      *float64 // OTel's own cost_usd, shown beside Radar's when known
	Ratelimit5h                          *float64
	Ratelimit7d                          *float64
	PromptHash                           string
	InTranscript                         bool
}

// RouteFacts is what the router's route event says about the request.
type RouteFacts struct {
	Event   domain.RouteEvent `json:"event"`    // as the spool line decodes
	CostUSD float64           `json:"cost_usd"` // Radar's price for the event's usage, 0 when the caller has none
}

// TranscriptFacts is what Claude Code's transcript says about the same request.
type TranscriptFacts struct {
	RequestID   string            `json:"request_id"` // requestId, joins on Row.AnthropicID
	PromptID    string            `json:"prompt_id"`
	QuerySource string            `json:"query_source"`
	StopReason  string            `json:"stop_reason"`
	Usage       domain.RouteUsage `json:"usage"` // the assistant line's final usage
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
