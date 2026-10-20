package isoenv

// The port guard: an environment that does not name $HUDDLE_ROUTER_PORT falls back to the
// canonical port (domain.HuddleRouterPort), and on a developer machine the owner's live
// huddle-router answers there — a health probe reports up, a start is skipped. Every test
// environment names its own port (ports.Free), and a new test cannot forget.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// liveRouterPort is the canonical port literal a test file must not hold.
const liveRouterPort = "18786"

// literalAllowed lists the *_test.go files outside this package that may hold the canonical
// port literal, each with the reason it is data and never an address a test dials or serves.
var literalAllowed = map[string]string{
	"cmd/huddle/hooks_providers_e2e_test.go":          "a comment names the reserved 18786-18879 range the suite's ports live outside",
	"internal/core/cli/cli_test.go":                   "the literal is a golden status line's display text the test compares against",
	"internal/core/cli/commands_test.go":              "the literals are golden status lines' display text the tests compare against",
	"internal/core/cli/pins_test.go":                  "a comment names the canonical port to say why the test pins its own",
	"internal/core/cli/route_test.go":                 "the canonical URL is settings and display data the comparisons read, never dialled",
	"internal/core/testkit/fakedeps/fakedeps_test.go": "the literal is the fake host's canned router line, asserted as text",
	"internal/domain/plugin_routers_test.go":          "the plugin-router port table and the URL parsing data are the default itself",
	"internal/domain/provider_env_test.go":            "the default base URL is the subject of the test",
	"internal/domain/router_address_test.go":          "the default port is the subject of the test",
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
		if line := firstLine(data, liveRouterPort); line != 0 {
			if _, allowed := literalAllowed[rel]; !allowed {
				t.Errorf("%s:%d holds the canonical port literal %s: name the router port with ports.Free: the default %s is the owner's live router",
					rel, line, liveRouterPort, liveRouterPort)
			}
		}
		if !strings.Contains(string(data), "HUDDLE_ROUTER_PORT") && !buildsThrough(data, namesPort) {
			if _, allowed := startAllowed[rel]; !allowed {
				if line := firstMatchLine(data, startPattern); line != 0 {
					t.Errorf("%s:%d starts or probes the huddle router without naming its port: name the router port with ports.Free: the default %s is the owner's live router",
						rel, line, liveRouterPort)
				}
			}
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
	for path, reason := range startAllowed {
		if !seen[path] {
			t.Errorf("the start exception for %s is stale (%s): drop it from the allow list", path, reason)
		}
	}
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
