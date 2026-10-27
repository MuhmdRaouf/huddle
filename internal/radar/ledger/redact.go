package ledger

import "regexp"

// The body store keeps no secret: redactBody runs before splitBody on every body PutBody
// writes, so no block, segment or row of router.db ever holds a known secret shape. The
// redaction list below is that one set of shapes; a match becomes [redacted:<name>], after
// any prefix the rule keeps.

// redaction is one secret shape: name labels the replacement, keep is how many leading
// capture groups stand verbatim before the label (0 or 1).
type redaction struct {
	name  string
	shape *regexp.Regexp
	keep  int
}

var (
	bearerShape     = regexp.MustCompile(`(Bearer[ \t]+)[A-Za-z0-9._~+/=-]{8,}`)
	skKeyShape      = regexp.MustCompile(`\bsk-[A-Za-z0-9][A-Za-z0-9_-]{7,}`)
	zaiKeyShape     = regexp.MustCompile(`\b[0-9a-f]{32}\.[A-Za-z0-9]{16}\b`)
	huddleCredShape = regexp.MustCompile(`\bh(cred|dev|sat)_[A-Za-z0-9_-]{16,}`)
	joinTokenShape  = regexp.MustCompile(`\b[a-z0-9]{6}\.[a-z0-9]{16}\b`)
	signInCodeShape = regexp.MustCompile(`([?&#]code=)[A-Za-z0-9_-]{24}\b`)
)

// redactions answers the ordered list the body store applies: bearer headers, API keys
// (Anthropic/OpenAI style and Z.ai style), huddle's own credentials, invites' join tokens
// and sign-in codes. The order matters: a rule runs before any whose shape could sit
// inside one of its matches, so no secret survives half-covered.
func redactions() []redaction {
	return []redaction{
		{name: "bearer", shape: bearerShape, keep: 1},
		{name: "api-key", shape: skKeyShape},
		{name: "api-key", shape: zaiKeyShape},
		{name: "huddle-credential", shape: huddleCredShape},
		{name: "join-token", shape: joinTokenShape},
		{name: "sign-in-code", shape: signInCodeShape, keep: 1},
	}
}

// redactBody replaces every known secret shape in body with its [redacted:<name>] label
// and answers the redacted body with how many replacements it made. Bytes outside a match
// stand exactly as they were; a body with no match comes back as the same bytes.
func redactBody(body []byte) ([]byte, int) {
	out := body
	count := 0
	for _, rule := range redactions() {
		count += len(rule.shape.FindAll(out, -1))
		repl := make([]byte, 0, len(rule.name)+len("[redacted:]")+1)
		if rule.keep > 0 {
			repl = append(repl, '$', '1')
		}
		repl = append(repl, "[redacted:"...)
		repl = append(repl, rule.name...)
		repl = append(repl, ']')
		out = rule.shape.ReplaceAll(out, repl)
	}
	return out, count
}
