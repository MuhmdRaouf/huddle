package server

import (
	"net/http"
	"strings"
)

// ActionHeader names the action a write performs. Its value is the route's own path, chosen by
// the client (the UI and the Go clients derive it from the URL they call), so a later audit can
// name what happened without trusting a body. The server demands the header be present; the
// route the request reaches stays the authority on what ran.
const ActionHeader = "x-huddle-action"

// ContentSecurityPolicy is the exact policy SPEC 7.5 fixes: everything from this origin only,
// images additionally as inline data, no framing, no base tag, no form posted elsewhere. It
// admits no inline script or style, so the dashboard loads its code and styles as files.
const ContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; " +
	"form-action 'self'"

// securityHeaders sets the headers every answer carries, before any route runs so a refusal
// carries them too.
func securityHeaders(w http.ResponseWriter) {
	header := w.Header()
	header.Set("Content-Security-Policy", ContentSecurityPolicy)
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
}

// writeMethod says whether a method changes state: anything a read never is.
func writeMethod(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

// mcpHTTPPath says the path speaks MCP over HTTP. Its client is the bridge, not a browser, so it
// names no action; every other guard still applies.
func mcpHTTPPath(path string) bool {
	return path == "/mcp" || strings.HasPrefix(path, "/mcp/")
}

// originHost strips the scheme an Origin header carries; a malformed one stays as it came and is
// refused by the host check that follows.
func originHost(origin string) string {
	host := strings.TrimPrefix(origin, "https://")
	return strings.TrimPrefix(host, "http://")
}

// guard wraps the router in the request guards of SPEC 7.5: the Host must be ours, a write
// carries JSON and names its action, a foreign Origin never writes, and every answer carries the
// security headers — a strict Content-Security-Policy first of all, so the dashboard runs no
// inline or third-party script. A refusal answers and returns; otherwise the security headers
// are already set and the route answers.
func (s *server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		securityHeaders(w)
		if !s.hostAllowed(r.Host) {
			writeText(w, 421, "wrong host", nil)
			return
		}
		if !writeMethod(r.Method) {
			next(w, r)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !s.hostAllowed(originHost(origin)) {
			writeText(w, 403, "cross-origin write refused", nil)
			return
		}
		// a delete carries no body, so it carries no content-type either
		if r.Method != http.MethodDelete && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			writeText(w, 403, "writes need content-type: application/json", nil)
			return
		}
		if !mcpHTTPPath(r.URL.Path) && r.Header.Get(ActionHeader) == "" {
			writeText(w, 403, "writes need "+ActionHeader+" naming the action", nil)
			return
		}
		next(w, r)
	}
}
