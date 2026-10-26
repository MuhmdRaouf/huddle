package isoenv

// The port guard: an environment that does not name $HUDDLE_ROUTER_PORT falls back to the
// canonical port (domain.HuddleRouterPort), and on a developer machine the owner's live
// huddle-router answers there — a health probe reports up, a start is skipped. The same holds
// one range over: a hub start with no port named walks 18860-18869, the live hub's own range.
// Every test environment names its own port (ports.Free), and a new test cannot forget.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// liveRouterPort is the canonical port literal a test file must not hold.
const liveRouterPort = "18786"

// hubLiteralPattern matches the hub range's ports, as a number or inside a string: on a
// developer machine 18860-18869 is where the owner's live hub serves.
var hubLiteralPattern = regexp.MustCompile(`\b1886[0-9]\b`)

// literalAllowed lists the *_test.go files outside this package that may hold the canonical
// port literal, each with the reason it is data and never an address a test dials or serves.
var literalAllowed = map[string]string{
	"cmd/huddle/hooks_providers_e2e_test.go":          "a comment names the reserved 18786-18879 range the suite's ports live outside",
	"internal/core/cli/cli_test.go":                   "the literal is a golden status line's display text the test compares against",
	"internal/core/cli/commands_test.go":              "the literals are golden status lines' display text the tests compare against",
	"internal/core/cli/pins_test.go":                  "a comment names the canonical port to say why the test pins its own",
	"internal/core/testkit/fakedeps/fakedeps_test.go": "the literal is the fake host's canned router line, asserted as text",
	"internal/domain/provider_env_test.go":            "the default base URL is the subject of the test",
	"internal/domain/router_address_test.go":          "the default port is the subject of the test",
	"internal/harness/codex_test.go":                  "the literal is the gateway base URL the golden config.toml block writes and compares as text, never dialled",
}

// hubLiteralAllowed lists the *_test.go files outside this package that may hold a hub range
// literal, each with the reason its literals are data or display text and never a port a test
// serves or probes on the owner's machine.
var hubLiteralAllowed = map[string]string{
	"cmd/huddle/alone_test.go":                   "a comment says why pinFreePort saves a port: the 18860-18869 walk is where parallel packages race",
	"internal/huddle/auth/pair_test.go":          "the literals are WebAuthn origins the fake passkey ceremonies compare against, never URLs a request dials",
	"internal/huddle/auth/stepup_test.go":        "the literal is a WebAuthn origin the step-up ceremony compares against, never a URL a request dials",
	"internal/huddle/auth/totp_test.go":          "the literals are WebAuthn origins the ceremonies compare against, never URLs a request dials",
	"internal/huddle/cli/huddle_test.go":         "the literal is a foreign dashboard url the test asserts dashboardBase leaves untouched",
	"internal/huddle/cli/identity_cover_test.go": "the literal is a SavePort value a temp home round-trips",
	"internal/huddle/cli/launcher_test.go":       "a comment says why pinFreePort saves a port: the 18860-18869 walk is where parallel packages race",
	"internal/huddle/cli/pair_cover_test.go":     "the literal is a SavePort value a temp home round-trips",
	"internal/huddle/cli/serve_cover_test.go":    "the literals are run-file and config content a temp home round-trips",
	"internal/huddle/cli/serve_env_test.go":      "the literal is an env entry the proxy filter's kept list asserts on as text",
	"internal/huddle/cli/serve_test.go":          "comments name the walk 18860-18869 to say why the tests pin their own ports",
	"internal/huddle/cli/setup_cover_test.go":    "the literal is a legacy session entry's url the import reads back as data",
	"internal/huddle/cli/setup_test.go":          "the literal is golden status text the output compares against",
	"internal/huddle/home/config_cover_test.go":  "the literals are SaveConfig and SavePort values temp homes round-trip",
	"internal/huddle/home/config_test.go":        "the literals are config file content temp homes round-trip",
	"internal/huddle/home/satsecret_test.go":     "the literal is a hub url the satellite secret's account name is derived from, never dialled",
	"internal/huddle/hooks/hooks_test.go":        "a comment names the shared 18860-18869 range to say why the test names its own port",
	"internal/huddle/outside/lifecycle_test.go":  "the literals are run-file and config content a temp home round-trips",
	"internal/huddle/server/port_test.go":        "the literals are the port walk's own range: the tests hold only what the range leaves free and skip when it is all busy",
	"internal/huddle/server/serve_port_test.go":  "the literal is a skip message naming the range the test found fully busy",
}

// startPattern matches the code that starts or probes the huddle router: the routerd entries,
// the ensure hook and the plugin's Wire, and the daemon's words as the argv a test hands a
// child (`"router", "start"`). The words as one phrase match comments and golden strings only,
// so they name no code path and are not matched.
var startPattern = regexp.MustCompile(`routerd\.(Start|Health|Run|Wake)\(|EnsureRouter\(|cli\.Wire\(|"router", "(serve|start)"`)

// namesPort lists the builders that set HUDDLE_ROUTER_PORT for every environment they answer,
// so a file building its environments through one of them names the port.
var namesPort = []string{"isoenv.Environ(", "wireSandbox("}

// startAllowed lists the files whose router-start matches were inspected and cannot reach the
// live router, each with the reason.
var startAllowed = map[string]string{
	"internal/core/cli/route_test.go":                 "the start match reads the fake host's recorded line back; the ensure call dials nothing",
	"internal/core/testkit/fakedeps/fakedeps_test.go": "the host under test is the fake, whose EnsureRouter answers a canned line and dials nothing",
}

// hubOptions and hubServe find the hub server's Options literals and starts in a test's
// source. The import alias some tests give the hub server ends in server, so both match those
// starts too.
var (
	hubOptions    = regexp.MustCompile(`server\.Options\{`)
	hubServe      = regexp.MustCompile(`server\.Serve\(`)
	portZero      = regexp.MustCompile(`(?:^|[,{\n])\s*Port:\s*0\b`)
	portField     = regexp.MustCompile(`\bPort:`)
	portNamedTrue = regexp.MustCompile(`\bPortNamed:\s*true\b`)
)

func TestNoTestReachesTheLiveRouterPort(t *testing.T) {
	seen := map[string]bool{}
	err := filepath.WalkDir(moduleRoot(t), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			// .agents holds the worktrees and scratch of other cards: their sources are this
			// module's copies, not this run's.
			case ".git", ".agents", "node_modules", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(moduleRoot(t), path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "internal/testkit/isoenv/") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		seen[rel] = true
		for _, finding := range portFindings(rel, data) {
			t.Error(finding)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the module's tests: %v", err)
	}
	for path, reason := range literalAllowed {
		if !seen[path] {
			t.Errorf("the literal exception for %s is stale (%s): drop it from the allow list", path, reason)
		}
	}
	for path, reason := range hubLiteralAllowed {
		if !seen[path] {
			t.Errorf("the hub literal exception for %s is stale (%s): drop it from the allow list", path, reason)
		}
	}
	for path, reason := range startAllowed {
		if !seen[path] {
			t.Errorf("the start exception for %s is stale (%s): drop it from the allow list", path, reason)
		}
	}
}

// portFindings answers every guard finding in one test file's source, each the full
// "rel:line message" the walk reports, in the order the checks run. The walk calls it per
// file; the table test reads it directly.
func portFindings(rel string, data []byte) []string {
	text := string(data)
	var findings []string
	if line := firstLine(data, liveRouterPort); line != 0 {
		if _, allowed := literalAllowed[rel]; !allowed {
			findings = append(findings, fmt.Sprintf("%s:%d holds the canonical port literal %s: name the router port with ports.Free: the default %s is the owner's live router",
				rel, line, liveRouterPort, liveRouterPort))
		}
	}
	if line := firstMatchLine(data, hubLiteralPattern); line != 0 {
		if _, allowed := hubLiteralAllowed[rel]; !allowed {
			findings = append(findings, fmt.Sprintf("%s:%d holds the hub port literal %s: name the port with ports.Free: 18860-18869 is the owner's live hub",
				rel, line, hubLiteralPattern.FindString(text)))
		}
	}
	if !strings.Contains(text, "HUDDLE_ROUTER_PORT") && !buildsThrough(data, namesPort) {
		if _, allowed := startAllowed[rel]; !allowed {
			if line := firstMatchLine(data, startPattern); line != 0 {
				findings = append(findings, fmt.Sprintf("%s:%d starts or probes the huddle router without naming its port: name the router port with ports.Free: the default %s is the owner's live router",
					rel, line, liveRouterPort))
			}
		}
	}
	if line := unnamedHubPort(text); line != 0 {
		findings = append(findings, fmt.Sprintf("%s:%d starts a hub without a named port: pass Port: ports.Free(t), PortNamed: true", rel, line))
	}
	return findings
}

// unnamedHubPort answers the line of the first hub start the guard refuses: a server.Options
// literal holding Port: 0 — with it Serve walks the live hub's 18860-18869 range — or a
// server.Serve whose Options sets neither Port: nor PortNamed: true. NewApp runs no listener
// and is never refused unless its options hold Port: 0.
func unnamedHubPort(text string) int {
	blocks := optionsBlocks(text)
	for _, block := range blocks {
		if portZero.MatchString(block.body) {
			return block.line
		}
	}
	for _, at := range hubServe.FindAllStringIndex(text, -1) {
		for _, block := range blocks {
			if block.start <= at[1] {
				continue
			}
			if !portField.MatchString(block.body) && !portNamedTrue.MatchString(block.body) {
				return block.line
			}
			break
		}
	}
	return 0
}

// optionsBlock is one server.Options literal in a test's source.
type optionsBlock struct {
	start int    // the byte offset of the literal's name
	line  int    // the literal's 1-based line
	body  string // the source between the literal's braces
}

// optionsBlocks answers every server.Options literal in the source, in order. Braces are
// counted with strings, runes and comments skipped, so none of them can hand back a brace
// that is not the block's own; a literal whose block never closes is left out.
func optionsBlocks(text string) []optionsBlock {
	var blocks []optionsBlock
	for _, at := range hubOptions.FindAllStringIndex(text, -1) {
		body, ok := braceBlock(text, at[1]-1)
		if !ok {
			continue
		}
		blocks = append(blocks, optionsBlock{
			start: at[0],
			line:  strings.Count(text[:at[0]], "\n") + 1,
			body:  body,
		})
	}
	return blocks
}

// braceBlock answers the source between the '{' at open and its matching '}', and whether it
// closed before the source ran out.
func braceBlock(text string, open int) (string, bool) {
	depth := 0
	for i := open; i < len(text); i++ {
		switch c := text[i]; c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[open+1 : i], true
			}
		case '"', '\'', '`':
			i = skipQuoted(text, i, c)
		case '/':
			switch {
			case strings.HasPrefix(text[i:], "//"):
				if nl := strings.IndexByte(text[i:], '\n'); nl >= 0 {
					i += nl // the loop's i++ lands on the newline
				} else {
					i = len(text)
				}
			case strings.HasPrefix(text[i:], "/*"):
				if end := strings.Index(text[i+2:], "*/"); end >= 0 {
					i += 2 + end + 1 // the loop's i++ lands past the */
				} else {
					i = len(text)
				}
			}
		}
	}
	return "", false
}

// skipQuoted answers the index of the quote closing the one at open; a backslash hides the
// next byte in an interpreted string or rune. An unterminated quote runs to the source's end,
// where the block scan runs out with it.
func skipQuoted(text string, open int, quote byte) int {
	for i := open + 1; i < len(text); i++ {
		switch text[i] {
		case '\\':
			i++
		case quote:
			return i
		}
	}
	return len(text) - 1
}

// firstLine answers the first line holding the text given, 1-based; 0 when none does.
func firstLine(data []byte, text string) int {
	for number, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, text) {
			return number + 1
		}
	}
	return 0
}

// firstMatchLine answers the first line matching the pattern given, 1-based; 0 when none does.
func firstMatchLine(data []byte, pattern *regexp.Regexp) int {
	for number, line := range strings.Split(string(data), "\n") {
		if pattern.MatchString(line) {
			return number + 1
		}
	}
	return 0
}

// buildsThrough answers whether the source calls any builder that names the router port.
func buildsThrough(data []byte, builders []string) bool {
	for _, call := range builders {
		if strings.Contains(string(data), call) {
			return true
		}
	}
	return false
}
