package store

import (
	"slices"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/radar/cost"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// MatchMs is how far apart the two clocks may read for one call: both stamp the moment
// the answer is written.
const MatchMs = 10_000

// BareAgent strips the transcript's agent- prefix: "agent-a31cd…" (a transcript file
// name) and "a31cd…" (the router's header) name the same agent.
func BareAgent(id string) string {
	if bare, ok := strings.CutPrefix(id, "agent-"); ok {
		return bare
	}
	return id
}

// FromRouter reports whether a record came from a router's spool line.
func FromRouter(record shared.RequestRecord) bool {
	return record.Route != nil
}

// FillUpstream fills an empty upstream with the host the model's own provider serving
// names (glm-… → https://api.z.ai): a transcript copy alone never says where its call
// went, and the provider's own host is the honest reading. Anthropic and unknown ids stay
// empty — a claude call with no recorded upstream went direct.
func FillUpstream(record shared.RequestRecord) shared.RequestRecord {
	if record.Upstream != "" {
		return record
	}
	host, ok := cost.OfferingHostOf(record.Model)
	if !ok {
		return record
	}
	record.Upstream = host
	return record
}

// MatchDistance says how well a transcript call fits a spool line: nil when it cannot be
// the same call, else a distance where smaller is closer (time apart, plus any difference
// in measured latency).
func MatchDistance(transcript, route shared.RequestRecord, routeAgent string) *int64 {
	if transcript.SessionID != route.SessionID || transcript.Model != route.Model {
		return nil
	}
	if routeAgent != "" && BareAgent(routeAgent) != BareAgent(transcript.AgentID) {
		return nil
	}
	apart := transcript.Ts - route.Ts
	if apart < 0 {
		apart = -apart
	}
	if apart > MatchMs {
		return nil
	}
	var latencyGap int64
	if transcript.LatencyMs != nil && route.LatencyMs != nil {
		latencyGap = *transcript.LatencyMs - *route.LatencyMs
		if latencyGap < 0 {
			latencyGap = -latencyGap
		}
	}
	distance := apart + latencyGap
	return &distance
}

// Combine builds the single record for a call both sources saw, stored under id. The
// route line's usage wins when it carries any: the router tracked the provider's own
// stream before any rewrite, while the transcript's copy repeats what the (possibly
// rewritten) stream opened with.
func Combine(transcript, route shared.RequestRecord, id string) shared.RequestRecord {
	merged := transcript
	merged.ID = id
	if route.Upstream != "" {
		merged.Upstream = route.Upstream
	}
	if route.LatencyMs != nil {
		merged.LatencyMs = route.LatencyMs
	}
	if shared.TotalTokens(route.Tokens) > 0 {
		merged.Tokens = route.Tokens
	}
	if transcript.StopReason == nil {
		merged.StopReason = route.StopReason
	}
	// only the route line knows how the wire answered; the transcript never carries a status
	merged.Status = route.Status
	merged.Provider = route.Provider
	if route.Route != nil {
		merged.Route = route.Route
	}
	if route.Via != nil {
		merged.Via = route.Via
	}
	// the capture the route line names must survive the pairing, or the request loses its prompt
	if route.PromptHash != nil {
		merged.PromptHash = route.PromptHash
	}
	if route.Headers != nil {
		merged.Headers = route.Headers
	}
	// only the route line knows the lane sent the call to another model
	if route.Fallback != nil {
		merged.Fallback = route.Fallback
	}
	// the transcript's own tail keeps its places; what the route line added lands after it
	merged.EarlyKeys = nil
	merged.TailKeys = tailKeysOf(transcript.TailKeys, route.Status != nil, route.Route != nil, route.Via != nil,
		route.Fallback != nil, route.PromptHash != nil, route.Headers != nil)
	return FillUpstream(merged)
}

// tailKeysOf lays the merged record's tail: the transcript's keys as they stand, then the
// route line's status, route, via, fallback, prompt hash and headers, each only when it is new
// to the tail.
func tailKeysOf(transcript []string, status, route, via, fallback, promptHash, headers bool) []string {
	tail := make([]string, len(transcript), len(transcript)+6)
	copy(tail, transcript)
	add := func(key string, present bool) {
		if !present || slices.Contains(tail, key) {
			return
		}
		tail = append(tail, key)
	}
	add("status", status)
	add("route", route)
	add("via", via)
	add("fallback", fallback)
	add("promptHash", promptHash)
	add("headers", headers)
	return tail
}

// Refresh merges a later copy of a record already held (streaming writes one answer
// several times). A routed record keeps what its route line measured: the later copy only
// adds what the stream finished with.
func Refresh(existing, record shared.RequestRecord) shared.RequestRecord {
	routed := FromRouter(existing)
	merged := existing
	if record.Model != "" {
		merged.Model = record.Model
	}
	if !routed && record.LatencyMs != nil {
		merged.LatencyMs = record.LatencyMs
	}
	if !routed && record.Upstream != "" {
		merged.Upstream = record.Upstream
	}
	if shared.TotalTokens(record.Tokens) > 0 {
		merged.Tokens = record.Tokens
	}
	if record.StopReason != nil {
		merged.StopReason = record.StopReason
	}
	if record.Status != nil {
		merged.Status = record.Status
		merged.TailKeys = appendTailKey(merged.TailKeys, "status")
	}
	// the result event that totals a provider-only stream lands as a later copy of the request it fills
	if record.Totals {
		merged.Totals = true
		merged.TailKeys = appendTailKey(merged.TailKeys, "totals")
	}
	// a streamed answer's later copies carry the fuller what (the tools the message ended with)
	if record.What != nil {
		merged.What = record.What
		merged.TailKeys = appendTailKey(merged.TailKeys, "what")
	}
	return FillUpstream(merged)
}

// appendTailKey adds one key to a record's tail, after the keys already there.
func appendTailKey(tail []string, key string) []string {
	if slices.Contains(tail, key) {
		return tail
	}
	if tail == nil {
		tail = []string{}
	}
	return append(tail, key)
}
