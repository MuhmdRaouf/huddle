package domain

import "regexp"

// capturedHeaderName is the response header names a route event may carry, case-insensitively;
// nothing else is ever stored. A compiled regexp is read-only state.
var capturedHeaderName = regexp.MustCompile(`^(?i:request-id|x-request-id|retry-after|content-type|anthropic-ratelimit-.+|x-ratelimit-.+)$`)

// CapturedHeader reports whether a response header may be kept: the request id, retry-after,
// the content type and the rate-limit counters, and nothing else, so no credential, cookie or
// token is ever stored.
func CapturedHeader(name string) bool {
	return capturedHeaderName.MatchString(name)
}
