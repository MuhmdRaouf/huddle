package shared

import "regexp"

// ContextSmall is the context window the session cards and the compaction alert assume
// until something bigger shows.
const ContextSmall = 200_000

// ContextLarge is the window a 1M model id or a request over the small window reveals.
const ContextLarge = 1_000_000

// millionContext matches the model ids that run in a 1M window ("[1m]", "-1m").
var millionContext = regexp.MustCompile(`(?i)\[1m\]|-1m\b`)

// ContextOf is one call's input plus cache tokens: how full a session's context window
// that call found.
func ContextOf(r *RequestRecord) int64 {
	return r.Tokens.Input + r.Tokens.CacheRead + r.Tokens.CacheWrite
}

// IsRunTotal reports whether a request's tokens are the whole run's total from its
// result event, not one call's context.
func IsRunTotal(r *RequestRecord) bool {
	return r.Totals
}

// ContextWindow is the window a session's agents work in: 1M once a model id or a
// request shows it, else 200k. A run total says nothing about any one call's context, so
// those requests stay out of the decision.
func ContextWindow(requests []*RequestRecord) int64 {
	for _, r := range requests {
		if IsRunTotal(r) {
			continue
		}
		if millionContext.MatchString(r.Model) || ContextOf(r) > ContextSmall {
			return ContextLarge
		}
	}
	return ContextSmall
}
