package plugins

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/core/providers"
	"github.com/MuhmdRaouf/huddle/internal/core/testkit/fakeclaude"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
)

// Shared helpers for the plugin-directory checks: the repository's plugin dirs, the built huddle
// binary every CLI-reading check runs, and the readers over one plugin's files that the surface
// and launcher suites share.

// repoRoot is the repository root, two levels above this package's directory.
const repoRoot = "../.."

// pluginsRoot is where the provider plugins sit, under the repository root.
var pluginsRoot = filepath.Join(repoRoot, "plugins")

// huddleBin is the binary TestMain builds once: the help the surface checks read and the launcher
// smoke's every run come from it.
var huddleBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "huddle-plugins-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	huddleBin = filepath.Join(dir, "huddle")
	build := exec.Command("go", "build", "-o", huddleBin, "github.com/MuhmdRaouf/huddle/cmd/huddle")
	build.Dir = repoRoot
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building the huddle binary:", err)
		os.Exit(1)
	}
	// Run runs the suite and removes the fake claude binary's build dir (Build compiles it on
	// first use); this suite's own build dir goes after it: os.Exit would skip a defer.
	// Code these tests run in process reads os.Environ: the sandbox keeps it off the owner's home.
	unsandbox, err := isoenv.Sandbox()
	if err != nil {
		fmt.Fprintln(os.Stderr, "isoenv:", err)
		os.Exit(1)
	}
	code := fakeclaude.Run(m)
	unsandbox()
	os.RemoveAll(dir)
	os.Exit(code)
}

// pluginDirOf is one provider's plugin directory.
func pluginDirOf(name string) string {
	return filepath.Join(pluginsRoot, name+"-plugin-cc")
}

// providerOf is the provider a plugin directory ships.
func providerOf(t *testing.T, name string) (domain.Provider, string) {
	t.Helper()
	provider, ok := providers.ByName(name)
	if !ok {
		t.Fatalf("%s: providers has no such provider", name)
	}
	return provider, pluginDirOf(name)
}

// launcherVersion reads the version a launcher installs out of its `bin=$home/<version>/huddle` line.
var launcherVersionLine = regexp.MustCompile(`(?m)^bin=\$home/([^/]+)/huddle$`)

// launcherVersionOf is the huddle version one provider's launcher pins.
func launcherVersionOf(t *testing.T, pluginDir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(pluginDir, "dist", "run"))
	if err != nil {
		t.Fatalf("read the launcher: %v", err)
	}
	match := launcherVersionLine.FindSubmatch(raw)
	if match == nil {
		t.Fatalf("%s: the launcher names no versioned huddle binary", pluginDir)
	}
	return string(match[1])
}

// installBinary copies the built huddle binary where the launcher looks it up:
// $AGENTS_HUDDLE_HOME/<version>/huddle.
func installBinary(t *testing.T, pluginDir, binHome string) string {
	t.Helper()
	version := launcherVersionOf(t, pluginDir)
	dir := filepath.Join(binHome, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("make the binary home: %v", err)
	}
	bin := filepath.Join(dir, "huddle")
	raw, err := os.ReadFile(huddleBin)
	if err != nil {
		t.Fatalf("read the built binary: %v", err)
	}
	if err := os.WriteFile(bin, raw, 0o755); err != nil {
		t.Fatalf("install the huddle binary: %v", err)
	}
	return bin
}

// frontMatterDoc is one markdown file's front matter and body.
type frontMatterDoc struct {
	data string
	body string
}

// splitFrontMatter splits `---\n…\n---\n…`: the YAML between the fences and what follows.
func splitFrontMatter(text string) (frontMatterDoc, bool) {
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return frontMatterDoc{}, false
	}
	front, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return frontMatterDoc{}, false
	}
	return frontMatterDoc{data: front, body: body}, true
}

// surface is the reader over one plugin's files: what the surface and launcher checks read.
type surface struct {
	t    *testing.T
	dir  string
	name string
}

// newSurface opens one plugin's directory.
func newSurface(t *testing.T, name string) *surface {
	t.Helper()
	dir := pluginDirOf(name)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("%s plugin dir: %v", name, err)
	}
	return &surface{t: t, dir: dir, name: name}
}

// read is one plugin file's text.
func (s *surface) read(relative string) string {
	s.t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.dir, relative))
	if err != nil {
		s.t.Fatalf("%s: %v", relative, err)
	}
	return string(raw)
}

// surfaceText is a file's text as its consumer sees it: JSON string escapes undone.
func (s *surface) surfaceText(relative string) string {
	s.t.Helper()
	text := s.read(relative)
	if strings.HasSuffix(relative, ".json") {
		return strings.ReplaceAll(text, `\"`, `"`)
	}
	return text
}

// markdownFiles lists a directory's markdown files, as `<dir>/<file>`, sorted.
func (s *surface) markdownFiles(dir string) []string {
	s.t.Helper()
	entries, err := os.ReadDir(filepath.Join(s.dir, dir))
	if err != nil {
		s.t.Fatalf("%s: %v", dir, err)
	}
	var files []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".md") {
			files = append(files, dir+"/"+entry.Name())
		}
	}
	slices.Sort(files)
	return files
}

// commandFiles is commands/*.md plus the engine setup commands in commands/setup/.
func (s *surface) commandFiles() []string {
	return append(s.markdownFiles("commands"), s.markdownFiles("commands/setup")...)
}

// allSurfaceFiles is every file a user-facing surface lives on: the agents and the commands. The
// plugins ship no hooks file; huddle's merged hooks run their work.
func (s *surface) allSurfaceFiles() []string {
	return append(s.markdownFiles("agents"), s.commandFiles()...)
}

// frontMatterOf is one markdown file's front matter, failing the test when the file has none.
func (s *surface) frontMatterOf(relative string) frontMatterDoc {
	s.t.Helper()
	doc, ok := splitFrontMatter(s.read(relative))
	if !ok {
		s.t.Fatalf("%s: no front matter", relative)
	}
	return doc
}

// invocation is one launcher call a surface file makes: the command word and the flags on its line.
type invocation struct {
	file    string
	command string
	flags   []string
}

// invocationsContain is whether one file's calls include the given call.
func invocationsContain(calls []invocation, want invocation) bool {
	for _, call := range calls {
		if call.file == want.file && call.command == want.command && slices.Equal(call.flags, want.flags) {
			return true
		}
	}
	return false
}

// invocationLine matches `sh "${CLAUDE_PLUGIN_ROOT}/dist/run" <name>.js <command><rest of line>`.
var invocationLine = func(name string) *regexp.Regexp {
	return regexp.MustCompile(`sh "\$\{CLAUDE_PLUGIN_ROOT\}/dist/run" ` + regexp.QuoteMeta(name) + `\.js ([a-z]+)([^\x60\n]*)`)
}

// flagWord is one `--flag` on an invocation line.
var flagWord = regexp.MustCompile(`--[a-z][a-z-]*`)

// invocationsIn lists the launcher calls one file makes.
func (s *surface) invocationsIn(file string) []invocation {
	var calls []invocation
	for _, match := range invocationLine(s.name).FindAllStringSubmatch(s.surfaceText(file), -1) {
		calls = append(calls, invocation{file: file, command: match[1], flags: flagWord.FindAllString(match[2], -1)})
	}
	return calls
}
