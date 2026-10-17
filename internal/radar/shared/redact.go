package shared

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// Redaction and truncation: nothing the hook or the server writes may carry a secret.
// The rules, in order:
//   1. any object key naming secret material has its whole value replaced, at any depth,
//      in objects and arrays;
//   2. inside strings, `Bearer <token>` headers and sk-…-style API keys are masked;
//   3. when a *.env file path appears anywhere in a tool input object, that object's
//      content-bearing keys are replaced (the transcript stays the source of truth for
//      what was really read or written);
//   4. every string is truncated to the per-field budget with a note of what was cut.
//
// Pure and total: any input yields output; unknown shapes pass through the same rules.

// FieldLimit is the default per-field budget, in UTF-16 code units.
const FieldLimit = 2048

// redacted is what a replaced value shows as.
const redacted = "[redacted]"

// jsSpaceClass is one char of JavaScript's \s: Go's space set minus NEL, plus the BOM.
const jsSpaceClass = `[\t\n\v\f\r \x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]`

var (
	secretKey = regexp.MustCompile(`(?i)key|token|secret|password|authorization|cookie`)
	bearerKey = regexp.MustCompile(`\bBearer` + jsSpaceClass + `+[A-Za-z0-9._-]+`)
	skKey     = regexp.MustCompile(`\bsk-[A-Za-z0-9][A-Za-z0-9_-]{7,}\b`)
	// envFilePath: a basename that is or ends in .env, with an optional dot-suffix.
	envFilePath = regexp.MustCompile(`(^|/)(\.env(\.[^/]+)?|[^/]+\.env)$`)
	// contentKeys carry file contents next to a path (Write/Edit inputs, Bash commands
	// that cat an env file).
	contentKeys = regexp.MustCompile(`^(content|contents|command|text|body|new_str|newString|edits|value)$`)
)

// IsSecretKey reports whether an object key names secret material, at any casing.
func IsSecretKey(key string) bool {
	return secretKey.MatchString(key)
}

// IsEnvFilePath reports whether a value is an .env-style file path: a basename that is or
// ends in .env, with an optional dot-suffix, with or without a directory.
func IsEnvFilePath(value string) bool {
	return envFilePath.MatchString(strings.TrimFunc(value, isJSSpace))
}

// maskString hides `Bearer <token>` headers and sk-… keys inside free text.
func maskString(text string) string {
	masked := bearerKey.ReplaceAllString(text, "Bearer "+redacted)
	return skKey.ReplaceAllString(masked, redacted)
}

// Truncate cuts text at limit UTF-16 code units and notes what was dropped; text within
// the limit stands as it is.
func Truncate(text string, limit int) string {
	length := jsfmt.UTF16Len(text)
	if length <= limit {
		return text
	}
	cut := strconv.Itoa(length - limit)
	return jsfmt.UTF16Slice(text, 0, limit) + "…[truncated " + cut + " chars]"
}

// Scrub redacts and truncates a value recursively; scalars other than strings pass
// through untouched.
func Scrub(value any, limit int) any {
	switch v := value.(type) {
	case string:
		return Truncate(maskString(v), limit)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = Scrub(item, limit)
		}
		return out
	case map[string]any:
		return scrubObject(v, limit)
	default:
		return value
	}
}

// scrubObject applies the key rules: a secret key, or a content-bearing key in an object
// that mentions an env file, loses its whole value; everything else goes back through
// Scrub. The input map is never touched — a fresh object comes back.
func scrubObject(record map[string]any, limit int) map[string]any {
	envAdjacent := mentionsEnvFile(record)
	out := make(map[string]any, len(record))
	for key, value := range record {
		switch {
		case IsSecretKey(key):
			out[key] = redacted
		case envAdjacent && contentKeys.MatchString(key):
			out[key] = redacted
		default:
			out[key] = Scrub(value, limit)
		}
	}
	return out
}

// mentionsEnvFile reports whether any of the object's direct values is an env file path.
func mentionsEnvFile(record map[string]any) bool {
	for _, value := range record {
		if text, ok := value.(string); ok && IsEnvFilePath(text) {
			return true
		}
	}
	return false
}

// ScrubText masks then truncates a string field (prompt text and the like); a value that
// is not a string reports ok false.
func ScrubText(value any, limit int) (string, bool) {
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	return Truncate(maskString(text), limit), true
}

// ScrubValue applies the redaction rules to a decoded value and returns the redacted
// copy; the input value is never modified. Secret keys and, in an object that mentions
// an env file, content-bearing keys lose their whole value; strings are masked and
// truncated; every other scalar rides along as it came.
func ScrubValue(v *jsfmt.Value, limit int) *jsfmt.Value {
	if v == nil {
		return nil
	}
	switch v.Kind() {
	case jsfmt.KindString:
		return jsfmt.String(Truncate(maskString(v.Text()), limit))
	case jsfmt.KindObject:
		envAdjacent := objectMentionsEnvFile(v)
		keys := v.Keys()
		fields := v.Fields()
		outKeys := make([]string, 0, len(keys))
		outFields := make([]*jsfmt.Value, 0, len(fields))
		for i, key := range keys {
			var field *jsfmt.Value
			switch {
			case IsSecretKey(key), envAdjacent && contentKeys.MatchString(key):
				field = jsfmt.String(redacted)
			default:
				field = ScrubValue(fields[i], limit)
			}
			outKeys = append(outKeys, key)
			outFields = append(outFields, field)
		}
		return jsfmt.Object(outKeys, outFields)
	case jsfmt.KindArray:
		items := v.Items()
		out := make([]*jsfmt.Value, 0, len(items))
		for _, item := range items {
			out = append(out, ScrubValue(item, limit))
		}
		return jsfmt.Array(out)
	default:
		return v
	}
}

// objectMentionsEnvFile reports whether any of the object's direct fields is an env file
// path; the rule never crosses into a nested object or array.
func objectMentionsEnvFile(object *jsfmt.Value) bool {
	for _, field := range object.Fields() {
		if field.Kind() == jsfmt.KindString && IsEnvFilePath(field.Text()) {
			return true
		}
	}
	return false
}

// ScrubJSON redacts and truncates one JSON value and returns the JSON text
// JSON.stringify writes for it: JSON.stringify(scrub(JSON.parse(text), limit)). The hook
// writes the result to disk, so the bytes must not drift.
func ScrubJSON(data []byte, limit int) (string, error) {
	value, err := jsfmt.DecodeValue(data)
	if err != nil {
		return "", err
	}
	return ScrubValue(value, limit).EncodeJSON(), nil
}
