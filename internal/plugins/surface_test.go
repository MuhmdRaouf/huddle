package plugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/MuhmdRaouf/huddle/internal/core/providers"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// The plugin surface every provider plugin ships, checked the same way for all five: the command
// and agent markdown (front matter, the shipped Sonnet default, colors, triggering examples), the
// hooks, every launcher invocation against the huddle binary's documented synopsis (its --help),
// no argument-hint or $ARGUMENTS, the launcher references, and the setup menu and its engine
// commands. Ported from the plugins' shared describePluginSurface suite.

// The five commands the plugin ships, the built-in setup alone (setup/claude) and one setup
// command per delegation engine; the CLI keeps the rest of them for internal use.
var surfaceCommands = []string{
	"board", "remove", "review", "setup", "usage",
	"setup/claude", "setup/omp", "setup/opencode", "setup/pi", "setup/zcode",
}

// surfaceEngines are the delegation engines one setup command and one agent each.
var surfaceEngines = []string{"omp", "opencode", "pi", "zcode"}

// agentColors are the colors an agent file may name.
var agentColors = []string{"blue", "cyan", "green", "yellow", "magenta", "red"}

// claudeEfforts are the effort levels an agent file may name; the model agents ship max.
var claudeEfforts = []string{"low", "high", "max"}

// globalFlags are documented once in the help's header line, beside no command.
var globalFlags = []string{"--json"}

// internalCommands are CLI commands no surface file may invoke.
var internalCommands = []string{"drive"}

// flagsTheHelpOmits are flags the CLI accepts that the help's one-line synopses leave out; the
// hooks run --hook, the engine agents name run --engine.
var flagsTheHelpOmits = map[string][]string{
	"run":   {"--engine"},
	"board": {"--hook"},
	"setup": {"--hook"},
	"usage": {"--hook"},
}

// documentedCommands is the command set the CLI's help documents.
var documentedCommands = []string{
	"accept", "batch", "board", "brief", "discard", "return", "review",
	"run", "setup", "show", "stop", "usage", "wait",
}

// documentedRunFlags is the run synopsis's flags.
var documentedRunFlags = []string{"--bg", "--flash", "--mode", "--wait"}

// modelAgentTools is the tools line both model agents carry exactly.
var modelAgentTools = []string{
	"Agent", "Bash", "Edit", "Glob", "Grep", "LSP", "ListMcpResourcesTool", "Monitor",
	"NotebookEdit", "Read", "ReadMcpResourceTool", "Skill", "TaskStop", "TodoWrite",
	"WebFetch", "WebSearch", "Write",
}

// maxDescriptionWords is how long an agent description may be: it is read on every turn.
const maxDescriptionWords = 120

// agentFrontMatter is one agent file's front matter, nothing else on the page.
type agentFrontMatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Model       string `yaml:"model"`
	Effort      string `yaml:"effort"`
	Color       string `yaml:"color"`
	Tools       any    `yaml:"tools"`
}

// commandFrontMatter is one command file's front matter, nothing else on the page.
type commandFrontMatter struct {
	Description            string `yaml:"description"`
	AllowedTools           any    `yaml:"allowed-tools"`
	ArgumentHint           string `yaml:"argument-hint"`
	DisableModelInvocation bool   `yaml:"disable-model-invocation"`
}

// decodeStrict decodes YAML into out, failing on a key the struct does not name.
func decodeStrict(t *testing.T, text string, out any) {
	t.Helper()
	decoder := yaml.NewDecoder(strings.NewReader(text))
	decoder.KnownFields(true)
	if err := decoder.Decode(out); err != nil {
		t.Fatalf("decode front matter: %v", err)
	}
}

// toolList is a tools field's names: a comma-separated string or a YAML list.
func toolList(t *testing.T, value any) []string {
	t.Helper()
	switch tools := value.(type) {
	case nil:
		return nil
	case string:
		var names []string
		for _, name := range strings.Split(tools, ",") {
			names = append(names, strings.TrimSpace(name))
		}
		return names
	case []any:
		var names []string
		for _, name := range tools {
			text, ok := name.(string)
			if !ok {
				t.Fatalf("tool %v is not a name", name)
			}
			names = append(names, text)
		}
		return names
	default:
		t.Fatalf("tools is %T, want a string or a list", value)
		return nil
	}
}

// childEnv is the environment a plugin process runs under: a temp home and Claude config dir, the
// provider's state root inside it, and nothing of the machine's own provider state (its keys, its
// state dirs, its huddle binary home) inherited.
// browserGuard writes the fake openers a plugin run finds first on its PATH: scripts that record
// their arguments and exit 0, so no run under test can ever reach the real browser, and a check
// can prove a normal run opens nothing.
func browserGuard(t *testing.T, home string) string {
	t.Helper()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("make the bin dir: %v", err)
	}
	script := "#!/bin/sh\nfor arg in \"$@\"; do echo \"$arg\" >> " + strconv.Quote(filepath.Join(home, "opened.log")) + "; done\nexit 0\n"
	for _, opener := range []string{"open", "xdg-open"} {
		if err := os.WriteFile(filepath.Join(bin, opener), []byte(script), 0o755); err != nil {
			t.Fatalf("write the fake %s: %v", opener, err)
		}
	}
	return bin
}

// openedCalls is what a run's fake openers were handed: empty when the run opened nothing.
func openedCalls(t *testing.T, home string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, "opened.log"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// childEnv is the environment a plugin process runs under: a temp home and Claude config dir, the
// provider's state root inside it, fake openers first on its PATH, and nothing of the machine's
// own provider state (its keys, its state dirs, its huddle binary home) inherited.
func childEnv(t *testing.T, provider domain.Provider, home string) []string {
	t.Helper()
	set := map[string]string{
		"HOME":                            home,
		"CLAUDE_CONFIG_DIR":               filepath.Join(home, "claude-config"),
		"PATH":                            browserGuard(t, home) + ":" + os.Getenv("PATH"),
		provider.EnvPrefix + "_STATE_DIR": filepath.Join(home, "state"),
		// the launcher sits inside this repository: a source root that names no commit keeps it on
		// the installed binary these tests are about, instead of building the working tree
		"HUDDLE_SOURCE": filepath.Join(home, "no-source"),
	}
	drop := func(name string) bool {
		switch name {
		case "HOME", "PATH", "CLAUDE_CONFIG_DIR", "AGENTS_HUDDLE_HOME", "HUDDLE_SOURCE":
			return true
		}
		return strings.HasPrefix(name, provider.EnvPrefix+"_")
	}
	env := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !drop(name) {
			env = append(env, entry)
		}
	}
	for name, value := range set {
		env = append(env, name+"="+value)
	}
	return env
}

// binaryHelp is one run of the built binary's help: its stdout, the exit code 0 and the empty
// stderr a clean temp home gives.
func binaryHelp(t *testing.T, provider domain.Provider) string {
	t.Helper()
	cmd := exec.Command(huddleBin, "provider", provider.Name, "cli", "--help")
	cmd.Env = childEnv(t, provider, t.TempDir())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("huddle provider %s cli --help: %v: %s", provider.Name, err, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("the help wrote stderr: %q", stderr.String())
	}
	return stdout.String()
}

// cliSynopsis parses the binary's --help into command → documented flags: every two-space-indented
// synopsis line names one command (its first word) and the flags its line shows. --json is global
// (the header line says so), so it belongs to no command's set.
func cliSynopsis(t *testing.T, provider domain.Provider) map[string][]string {
	t.Helper()
	lineRe := regexp.MustCompile(`^ {2}([a-z]+) `)
	synopsis := map[string][]string{}
	for _, line := range strings.Split(binaryHelp(t, provider), "\n") {
		match := lineRe.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		command := match[1]
		// A command whose only flag is the global --json still documents as a command (its flag
		// set comes out empty), so the key exists however its line reads.
		if _, seen := synopsis[command]; !seen {
			synopsis[command] = nil
		}
		for _, flag := range flagWord.FindAllString(line, -1) {
			if slices.Contains(globalFlags, flag) {
				continue
			}
			if !slices.Contains(synopsis[command], flag) {
				synopsis[command] = append(synopsis[command], flag)
			}
		}
	}
	return synopsis
}

func TestPluginSurface(t *testing.T) {
	for _, name := range providers.Names {
		t.Run(name, func(t *testing.T) {
			provider, _ := providerOf(t, name)
			synopsis := cliSynopsis(t, provider)

			t.Run("is installed as "+name+"-plugin-cc, named "+name+", and ships its licence", func(t *testing.T) {
				newSurface(t, name).checkInstall(provider)
			})
			t.Run("declares huddle as a dependency", func(t *testing.T) {
				newSurface(t, name).checkDependency()
			})
			t.Run("reads the documented command set from the huddle binary's help", func(t *testing.T) {
				newSurface(t, name).checkSynopsis(provider, synopsis)
			})
			t.Run("every agent and command markdown file has YAML front matter (a mapping) and a non-empty body", func(t *testing.T) {
				newSurface(t, name).checkFrontMatter()
			})
			t.Run("model agents "+provider.Agents[domain.ModelTierMain]+" and "+provider.Agents[domain.ModelTierFlash]+
				": name matches the file, the shipped sonnet default, effort max, distinct colors, triggering examples",
				func(t *testing.T) {
					newSurface(t, name).checkModelAgents(provider)
				})
			t.Run("agents do the work themselves: no CLI dispatch, no plugin CLI at all, and never commit or read secrets", func(t *testing.T) {
				newSurface(t, name).checkAgentsDoTheWork(provider)
			})
			t.Run("ships exactly board, remove, review, setup, usage and setup/{claude,omp,opencode,pi,zcode}", func(t *testing.T) {
				newSurface(t, name).checkCommandFiles()
			})
			t.Run("every command declares a description and allowed-tools Bash(sh:*) (the launcher), and invokes its own CLI command", func(t *testing.T) {
				newSurface(t, name).checkCommandFrontMatter()
			})
			t.Run("only review takes an argument (a job id), and no command says it takes none", func(t *testing.T) {
				newSurface(t, name).checkArguments()
			})
			t.Run("no --route anywhere on the surface", func(t *testing.T) {
				newSurface(t, name).checkNoRoute()
			})
			t.Run("every CLI invocation, hooks included, names a documented command with documented flags", func(t *testing.T) {
				newSurface(t, name).checkInvocations(provider, synopsis)
			})
			t.Run("every CLAUDE_PLUGIN_ROOT reference is the quoted launcher starting "+
				provider.Name+".js or "+provider.Name+"-router.js", func(t *testing.T) {
				newSurface(t, name).checkLauncherReferences(provider)
			})
			t.Run("ships no hooks: huddle's hooks.json runs the session start, the subagent stop and "+
				"the ensure work for every provider", func(t *testing.T) {
				newSurface(t, name).checkHooks(provider)
			})
			t.Run("setup.md always asks what to set up: built-in only, or built-in plus the tools it then asks for", func(t *testing.T) {
				newSurface(t, name).checkSetupMenu(provider)
			})
			t.Run("setup/claude.md is the built-in setup alone: plain setup, no question", func(t *testing.T) {
				newSurface(t, name).checkSetupClaude(provider)
			})
			for _, engine := range surfaceEngines {
				t.Run("setup/"+engine+".md checks the engine, always asks who watches with both options, then enables it", func(t *testing.T) {
					newSurface(t, name).checkEngineSetup(provider, engine)
				})
				t.Run("agent "+engine+": ships on Sonnet, names its setup command, runs the job on its engine and never decides", func(t *testing.T) {
					newSurface(t, name).checkEngineAgent(provider, engine)
				})
			}
			t.Run("the engine agents have three distinct colors, none shared with the main and flash agents", func(t *testing.T) {
				newSurface(t, name).checkAgentColors(provider)
			})
			t.Run("every slash command the surface or the core's messages name is one the plugin ships", func(t *testing.T) {
				newSurface(t, name).checkSlashCommands(provider)
			})
			t.Run("remove runs setup --remove and says to uninstall after it; review never offers accept on a crashed job", func(t *testing.T) {
				newSurface(t, name).checkRemoveAndReview(provider)
			})
		})
	}
}

// pluginManifest is .claude-plugin/plugin.json.
type pluginManifest struct {
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Dependencies []string `json:"dependencies"`
	Description  string   `json:"description"`
	License      string   `json:"license"`
}

// marketplaceFile is the repository's marketplace listing.
type marketplaceFile struct {
	Name    string `json:"name"`
	Plugins []struct {
		Name   string `json:"name"`
		Source string `json:"source"`
	} `json:"plugins"`
}

// checkInstall is the marketplace and manifest names, and the licence.
func (s *surface) checkInstall(provider domain.Provider) {
	var manifest pluginManifest
	decodeJSON(s.t, s.read(".claude-plugin/plugin.json"), &manifest)
	var market marketplaceFile
	decodeJSON(s.t, readFile(s.t, filepath.Join(repoRoot, ".claude-plugin", "marketplace.json")), &market)
	abs, err := filepath.Abs(s.dir)
	if err != nil {
		s.t.Fatalf("resolve %s: %v", s.dir, err)
	}
	var entry string
	for _, listed := range market.Plugins {
		source := filepath.Clean(filepath.Join(repoRoot, listed.Source))
		if resolved, err := filepath.Abs(source); err == nil && resolved == abs {
			entry = listed.Name
			break
		}
	}
	// The slash namespace and the data dir prefix (`data/<entry>-<marketplace>`) both rest on these names.
	if manifest.Name != provider.Name {
		s.t.Errorf("plugin.json names %q, want %q", manifest.Name, provider.Name)
	}
	if entry != provider.Name+"-plugin-cc" {
		s.t.Errorf("the marketplace entry for %s is %q, want %q", provider.Name, entry, provider.Name+"-plugin-cc")
	}
	if manifest.License != "GPL-3.0-or-later" {
		s.t.Errorf("plugin.json licence is %q, want GPL-3.0-or-later", manifest.License)
	}
	if s.read("LICENSE") != readFile(s.t, filepath.Join(repoRoot, "LICENSE")) {
		s.t.Errorf("%s: the shipped LICENSE is not the repository's", provider.Name)
	}
}

// checkDependency is plugin.json's dependencies: huddle alone.
func (s *surface) checkDependency() {
	var manifest pluginManifest
	decodeJSON(s.t, s.read(".claude-plugin/plugin.json"), &manifest)
	if !reflect.DeepEqual(manifest.Dependencies, []string{"huddle"}) {
		s.t.Errorf("dependencies = %v, want [huddle]", manifest.Dependencies)
	}
}

// checkSynopsis is the command set and the run flags, as the built binary documents them.
func (s *surface) checkSynopsis(provider domain.Provider, synopsis map[string][]string) {
	got := make([]string, 0, len(synopsis))
	for command := range synopsis {
		got = append(got, command)
	}
	slices.Sort(got)
	want := append([]string{}, documentedCommands...)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		s.t.Errorf("the help documents %v, want %v", got, want)
	}
	flags := append([]string{}, synopsis["run"]...)
	slices.Sort(flags)
	if !slices.Equal(flags, documentedRunFlags) {
		s.t.Errorf("run's documented flags = %v, want %v", flags, documentedRunFlags)
	}
}

// checkFrontMatter is every markdown file's front matter: a mapping, and something to read below it.
func (s *surface) checkFrontMatter() {
	for _, file := range append(s.markdownFiles("agents"), s.commandFiles()...) {
		doc, ok := splitFrontMatter(s.read(file))
		if !ok {
			s.t.Errorf("%s: no front matter", file)
			continue
		}
		var mapping map[string]any
		if err := yaml.Unmarshal([]byte(doc.data), &mapping); err != nil {
			s.t.Errorf("%s: front matter is not a mapping: %v", file, err)
			continue
		}
		if strings.TrimSpace(doc.body) == "" {
			s.t.Errorf("%s: empty body", file)
		}
	}
}

// modelAgentFiles is the agent files the plugin ships: the two model agents, then the engines, sorted.
func modelAgentFiles(provider domain.Provider) []string {
	files := []string{
		"agents/" + provider.Agents[domain.ModelTierMain] + ".md",
		"agents/" + provider.Agents[domain.ModelTierFlash] + ".md",
	}
	for _, engine := range surfaceEngines {
		files = append(files, "agents/"+engine+".md")
	}
	slices.Sort(files)
	return files
}

// checkModelAgents is the two model agents' front matter and description.
func (s *surface) checkModelAgents(provider domain.Provider) {
	if !slices.Equal(s.markdownFiles("agents"), modelAgentFiles(provider)) {
		s.t.Errorf("agents = %v, want %v", s.markdownFiles("agents"), modelAgentFiles(provider))
	}
	colors := map[string]bool{}
	for _, agent := range []domain.ModelTier{domain.ModelTierMain, domain.ModelTierFlash} {
		name := provider.Agents[agent]
		file := "agents/" + name + ".md"
		var fm agentFrontMatter
		decodeStrict(s.t, s.frontMatterOf(file).data, &fm)

		if fm.Name != name {
			s.t.Errorf("%s: name = %q, want %q", file, fm.Name, name)
		}
		if fm.Model != "sonnet" {
			s.t.Errorf("%s: model = %q, want sonnet (the shipped default; setup rewrites it)", file, fm.Model)
		}
		if fm.Effort != "max" {
			s.t.Errorf("%s: effort = %q, want max", file, fm.Effort)
		}
		if !slices.Contains(claudeEfforts, fm.Effort) {
			s.t.Errorf("%s: effort %q is none of %v", file, fm.Effort, claudeEfforts)
		}
		if !slices.Contains(agentColors, fm.Color) {
			s.t.Errorf("%s: color %q is none of %v", file, fm.Color, agentColors)
		}
		if tools := toolList(s.t, fm.Tools); tools != nil && !slices.Equal(tools, modelAgentTools) {
			s.t.Errorf("%s: tools = %v, want %v", file, tools, modelAgentTools)
		}
		if !strings.HasPrefix(fm.Description, "Use this agent") {
			s.t.Errorf("%s: description must start \"Use this agent\"", file)
		}
		if !strings.Contains(fm.Description, "Sonnet") {
			s.t.Errorf("%s: description never says it falls back to Sonnet", file)
		}
		// Says when it runs on its own model, naming the setup command whole (a broken substitution
		// once left "once /zai Code at the zai router").
		runsOn := "It runs on " + name + " once " + provider.Slash + "setup has pointed Claude Code at the " +
			provider.Name + " router, and on Sonnet before that or while the router is down."
		if !strings.Contains(fm.Description, runsOn) {
			s.t.Errorf("%s: description must name the setup sentence whole, want it to hold %q", file, runsOn)
		}
		if regexp.MustCompile(`/[a-z]+ Code at the`).MatchString(fm.Description) {
			s.t.Errorf("%s: description carries the broken substitution \"/… Code at the\"", file)
		}
		if words := len(strings.Fields(fm.Description)); words >= maxDescriptionWords {
			s.t.Errorf("%s: description is %d words, want under %d", file, words, maxDescriptionWords)
		}
		if !strings.Contains(fm.Description, provider.Display) {
			s.t.Errorf("%s: description never names %s", file, provider.Display)
		}
		if got := strings.Count(fm.Description, "<example>"); got < 2 {
			s.t.Errorf("%s: description carries %d <example> blocks, want at least 2", file, got)
		}
		colors[fm.Color] = true
	}
	if len(colors) != 2 {
		s.t.Errorf("the two model agents share a color: %v", colors)
	}
}

// checkAgentsDoTheWork is the model agents' rules: no plugin CLI, never commit, never secrets.
func (s *surface) checkAgentsDoTheWork(provider domain.Provider) {
	for _, tier := range []domain.ModelTier{domain.ModelTierMain, domain.ModelTierFlash} {
		name := provider.Agents[tier]
		file := "agents/" + name + ".md"
		text := s.read(file)
		if calls := s.invocationsIn(file); len(calls) != 0 {
			s.t.Errorf("%s: invokes the plugin CLI at %v", file, calls)
		}
		if !regexp.MustCompile(`[Nn]ever invoke the ` + provider.Name + ` plugin`).MatchString(text) {
			s.t.Errorf("%s: never says \"never invoke the %s plugin\"", file, provider.Name)
		}
		if !regexp.MustCompile(`[Nn]ever (commit|change anything)`).MatchString(text) {
			s.t.Errorf("%s: never says it must not commit or change anything", file)
		}
		if !strings.Contains(text, "secrets") {
			s.t.Errorf("%s: never says to leave secrets alone", file)
		}
	}
}

// checkCommandFiles is the shipped command set.
func (s *surface) checkCommandFiles() {
	want := make([]string, 0, len(surfaceCommands))
	for _, command := range surfaceCommands {
		want = append(want, "commands/"+command+".md")
	}
	slices.Sort(want)
	got := s.commandFiles()
	slices.Sort(got)
	if !slices.Equal(got, want) {
		s.t.Errorf("commands = %v, want %v", got, want)
	}
}

// checkCommandFrontMatter is every command's description, allowed-tools and its own CLI command.
func (s *surface) checkCommandFrontMatter() {
	for _, file := range s.commandFiles() {
		var fm commandFrontMatter
		decodeStrict(s.t, s.frontMatterOf(file).data, &fm)
		if fm.Description == "" {
			s.t.Errorf("%s: no description", file)
		}
		// commands/setup/<engine>.md is `/<plugin>:setup:<engine>`, and remove is `setup --remove`:
		// they run setup.
		command := strings.TrimSuffix(filepath.Base(file), ".md")
		if strings.HasPrefix(file, "commands/setup/") || file == "commands/remove.md" {
			command = "setup"
		}
		if tools := toolList(s.t, fm.AllowedTools); !slices.Equal(tools, []string{"Bash(sh:*)"}) {
			s.t.Errorf("%s: allowed-tools = %v, want [Bash(sh:*)]", file, tools)
		}
		runsIt := false
		for _, call := range s.invocationsIn(file) {
			if call.command == command {
				runsIt = true
				break
			}
		}
		if !runsIt {
			s.t.Errorf("%s never invokes %s", file, command)
		}
	}
}

// checkArguments is who takes an argument: review alone.
func (s *surface) checkArguments() {
	for _, file := range s.commandFiles() {
		var fm commandFrontMatter
		decodeStrict(s.t, s.frontMatterOf(file).data, &fm)
		takes := file == "commands/review.md"
		if takes && fm.ArgumentHint != "[job id]" {
			s.t.Errorf("%s: argument-hint = %q, want \"[job id]\"", file, fm.ArgumentHint)
		}
		if !takes && fm.ArgumentHint != "" {
			s.t.Errorf("%s: argument-hint = %q, want none", file, fm.ArgumentHint)
		}
		if strings.Contains(s.read(file), "$ARGUMENTS") != takes {
			s.t.Errorf("%s: $ARGUMENTS presence = %t, want %t", file, strings.Contains(s.read(file), "$ARGUMENTS"), takes)
		}
	}
	// `setup` has flags (--remove); the engine setups are worded elsewhere.
	for _, file := range s.commandFiles() {
		if strings.HasPrefix(file, "commands/setup/") {
			continue
		}
		if strings.Contains(s.read(file), "takes no arguments") {
			s.t.Errorf("%s: says it takes no arguments, but setup's flags say otherwise", file)
		}
	}
}

// checkNoRoute is --route: no surface file may name it.
func (s *surface) checkNoRoute() {
	for _, file := range s.allSurfaceFiles() {
		if strings.Contains(s.surfaceText(file), "--route") {
			s.t.Errorf("%s: names --route", file)
		}
	}
}

// checkInvocations is every launcher call against the CLI's documented synopsis.
func (s *surface) checkInvocations(provider domain.Provider, synopsis map[string][]string) {
	total := 0
	for _, file := range s.allSurfaceFiles() {
		for _, call := range s.invocationsIn(file) {
			total++
			documented, known := synopsis[call.command]
			allowed := append([]string{}, globalFlags...)
			if known {
				allowed = append(allowed, documented...)
			}
			allowed = append(allowed, flagsTheHelpOmits[call.command]...)
			if !known {
				s.t.Errorf("%s: %s is not a CLI command the help documents", file, call.command)
				continue
			}
			if slices.Contains(internalCommands, call.command) {
				s.t.Errorf("%s: %s is an internal command", file, call.command)
			}
			for _, flag := range call.flags {
				if !slices.Contains(allowed, flag) {
					s.t.Errorf("%s: %s %s is not documented for %s", file, call.command, flag, call.command)
				}
			}
		}
	}
	if total == 0 {
		s.t.Errorf("no surface file invokes the plugin CLI at all")
	}
}

// checkLauncherReferences is every CLAUDE_PLUGIN_ROOT mention: the quoted launcher, nothing else.
func (s *surface) checkLauncherReferences(provider domain.Provider) {
	references := regexp.MustCompile(`\$\{?CLAUDE_PLUGIN_ROOT\}?`)
	canonical := regexp.MustCompile(`"` + regexp.QuoteMeta("${CLAUDE_PLUGIN_ROOT}") + `/dist/run" ` +
		regexp.QuoteMeta(provider.Name) + `(-router)?\.js`)
	total := 0
	for _, file := range s.allSurfaceFiles() {
		text := s.surfaceText(file)
		want := len(references.FindAllString(text, -1))
		got := len(canonical.FindAllString(text, -1))
		if got != want {
			s.t.Errorf("%s: %d of %d CLAUDE_PLUGIN_ROOT references are the quoted launcher", file, got, want)
		}
		total += want
	}
	if total == 0 {
		s.t.Errorf("no surface file names the launcher at all")
	}
}

// hookEntry is one hook's command line and timeout.
type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// hookGroup is one matcher's hooks.
type hookGroup struct {
	Matcher *string     `json:"matcher"`
	Hooks   []hookEntry `json:"hooks"`
}

// hooksDocument is hooks/hooks.json.
type hooksDocument struct {
	Description string                 `json:"description"`
	Hooks       map[string][]hookGroup `json:"hooks"`
}

// jsonKeys is a JSON object's keys in file order, for the hook events' pinned order: the string
// tokens directly inside the named field's object.
func jsonKeys(t *testing.T, text, field string) []string {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(text))
	var keys []string
	depth := 0
	inField := false
	for {
		token, err := decoder.Token()
		if err != nil {
			t.Fatalf("read %s: %v", field, err)
		}
		switch token := token.(type) {
		case json.Delim:
			if token == '{' || token == '[' {
				depth++
			} else {
				depth--
			}
		case string:
			if inField && depth == 2 {
				keys = append(keys, token)
			}
			if depth == 1 && token == field {
				inField = true
			}
		}
		if depth == 0 {
			return keys
		}
	}
}

// checkHooks is what the plugin ships instead of hooks of its own: no hooks directory — huddle's
// merged hooks.json covers the session start, the subagent stop and the ensure work for every
// provider plugin.
func (s *surface) checkHooks(provider domain.Provider) {
	if _, err := os.Stat(filepath.Join(s.dir, "hooks")); err == nil {
		s.t.Errorf("the plugin ships a hooks directory, want none: huddle's hooks run the providers' work")
	} else if !errors.Is(err, os.ErrNotExist) {
		s.t.Errorf("stat the hooks directory: %v", err)
	}
	raw := readFile(s.t, filepath.Join(repoRoot, "plugin", "hooks", "hooks.json"))
	var hooks hooksDocument
	decodeStrictJSON(s.t, raw, &hooks)
	events := jsonKeys(s.t, raw, "hooks")
	for _, event := range []string{"SessionStart", "SubagentStop"} {
		if !slices.Contains(events, event) {
			s.t.Errorf("huddle's hooks.json serves %v, want an entry for %s", events, event)
		}
	}
}

// checkSetupMenu is setup.md: the questions and the lines it must carry.
func (s *surface) checkSetupMenu(provider domain.Provider) {
	file := "commands/setup.md"
	text := s.read(file)
	calls := s.invocationsIn(file)

	if len(calls) < 2 {
		s.t.Fatalf("setup.md makes %d launcher calls, want at least the menu read and the setup", len(calls))
	}
	wantFirst := []invocation{
		{file: file, command: "setup", flags: []string{"--engines", "--json"}},
		{file: file, command: "setup", flags: nil},
	}
	if !reflect.DeepEqual(calls[:2], wantFirst) {
		s.t.Errorf("setup.md opens with %v, want %v", calls[:2], wantFirst)
	}
	engineCheck := invocation{file: file, command: "setup", flags: []string{"--engine-check", "--json"}}
	if !invocationsContain(calls, engineCheck) {
		s.t.Errorf("setup.md never runs %v", engineCheck)
	}
	for _, want := range []string{
		"AskUserQuestion every time it runs",
		"never skip one as already answered",
		`"What should ` + provider.Display + ` set up?"`,
		"`Built-in only (Claude Code agents and /model)`",
		"`Built-in plus other tools (omp, opencode, pi, zcode)`",
		"multiSelect, one option per tool in this order: `omp`, `opencode`, `pi`, `zcode`",
		"`(installed)`",
		"`(not found)`",
		"`(current)`",
		"setup --engine-enable <tool> --watcher provider",
		"setup --engine-enable <tool> --watcher sonnet",
		"`" + provider.Catalog[domain.ModelTierMain].Label + " (" + provider.Display + ")`",
		"never disables an engine",
	} {
		if !strings.Contains(text, want) {
			s.t.Errorf("setup.md never says %s", strconv.Quote(want))
		}
	}
	for _, tier := range []domain.ModelTier{domain.ModelTierMain, domain.ModelTierFlash} {
		agent := provider.AgentPrefix + provider.Agents[tier]
		if !strings.Contains(text, agent) {
			s.t.Errorf("setup.md never names the %s agent", agent)
		}
	}
}

// checkSetupClaude is the built-in setup alone: plain setup, no question.
func (s *surface) checkSetupClaude(provider domain.Provider) {
	file := "commands/setup/claude.md"
	want := []invocation{{file: file, command: "setup", flags: nil}}
	if got := s.invocationsIn(file); !reflect.DeepEqual(got, want) {
		s.t.Errorf("setup/claude.md invokes %v, want %v", got, want)
	}
	text := s.read(file)
	if strings.Contains(text, "AskUserQuestion") {
		s.t.Errorf("setup/claude.md asks a question; the built-in setup asks nothing")
	}
	if !strings.Contains(text, "`"+provider.Slash+"setup` asks what to set up") {
		s.t.Errorf("setup/claude.md never points at %sshop's questions", provider.Slash)
	}
}

// checkEngineSetup is one engine's setup command.
func (s *surface) checkEngineSetup(provider domain.Provider, engine string) {
	file := "commands/setup/" + engine + ".md"
	text := s.read(file)
	engineCheck := invocation{file: file, command: "setup", flags: []string{"--engine-check", "--json"}}
	if !invocationsContain(s.invocationsIn(file), engineCheck) {
		s.t.Errorf("%s never runs %v", file, engineCheck)
	}
	for _, want := range []string{
		"setup --engine-check " + engine + " --json",
		"setup --engine-enable " + engine + " --watcher provider",
		"setup --engine-enable " + engine + " --watcher sonnet",
		"AskUserQuestion",
		`"Who should watch ` + engine + ` runs?"`,
		provider.Catalog[domain.ModelTierMain].Label + " (" + provider.Display + ")",
		"`Claude Sonnet`",
		"`(current)`",
	} {
		if !strings.Contains(text, want) {
			s.t.Errorf("%s never says %s", file, strconv.Quote(want))
		}
	}
	if !regexp.MustCompile(`always, every time this command runs`).MatchString(text) {
		s.t.Errorf("%s: the watcher question is not asked every run", file)
	}
}

// checkEngineAgent is one engine's agent.
func (s *surface) checkEngineAgent(provider domain.Provider, engine string) {
	file := "agents/" + engine + ".md"
	doc := s.frontMatterOf(file)
	var fm agentFrontMatter
	decodeStrict(s.t, doc.data, &fm)
	calls := s.invocationsIn(file)

	if fm.Name != engine {
		s.t.Errorf("%s: name = %q, want %q", file, fm.Name, engine)
	}
	if fm.Model != "sonnet" {
		s.t.Errorf("%s: model = %q, want sonnet", file, fm.Model)
	}
	if tools := toolList(s.t, fm.Tools); !slices.Equal(tools, []string{"Bash", "Read", "Grep", "Glob"}) {
		s.t.Errorf("%s: tools = %v, want [Bash Read Grep Glob]", file, tools)
	}
	if !strings.HasPrefix(fm.Description, "Use this agent") {
		s.t.Errorf("%s: description must start \"Use this agent\"", file)
	}
	if !strings.Contains(fm.Description, provider.Slash+"setup:"+engine) {
		s.t.Errorf("%s: description never names %s", file, provider.Slash+"setup:"+engine)
	}
	if !strings.Contains(fm.Description, provider.Display) {
		s.t.Errorf("%s: description never names %s", file, provider.Display)
	}
	if got := strings.Count(fm.Description, "<example>"); got < 2 {
		s.t.Errorf("%s: description carries %d <example> blocks, want at least 2", file, got)
	}
	runCall := invocation{file: file, command: "run", flags: []string{"--engine", "--bg"}}
	if !invocationsContain(calls, runCall) {
		s.t.Errorf("%s never runs %v", file, runCall)
	}
	if !strings.Contains(doc.body, "run - --engine "+engine+" --bg") {
		s.t.Errorf("%s: the body never shows the brief handed over on stdin with --engine %s --bg", file, engine)
	}
	for _, command := range []string{"run", "wait", "review", "accept", "return", "discard"} {
		found := false
		for _, call := range calls {
			if call.command == command {
				found = true
				break
			}
		}
		if !found {
			s.t.Errorf("%s never invokes %s", file, command)
		}
	}
	if !strings.Contains(doc.body, "run_in_background") {
		s.t.Errorf("%s: the body never says to wait with run_in_background", file)
	}
	if !regexp.MustCompile(`Never accept, return or discard the job yourself`).MatchString(doc.body) {
		s.t.Errorf("%s: the body never says the decision stays with the orchestrator", file)
	}
	if !strings.Contains(doc.body, provider.Slash+"setup:"+engine) {
		s.t.Errorf("%s: the body never names %s", file, provider.Slash+"setup:"+engine)
	}
}

// checkAgentColors is the six agents' colors: all distinct.
func (s *surface) checkAgentColors(provider domain.Provider) {
	var colors []string
	for _, name := range modelAgentFiles(provider) {
		var fm agentFrontMatter
		decodeStrict(s.t, s.frontMatterOf(name).data, &fm)
		colors = append(colors, fm.Color)
	}
	if len(colors) != 6 {
		s.t.Fatalf("read %d agent colors, want six", len(colors))
	}
	seen := map[string]bool{}
	for _, color := range colors {
		if seen[color] {
			s.t.Errorf("two agents share the color %q: %v", color, colors)
		}
		seen[color] = true
	}
}

// checkSlashCommands is every slash command the surface or the core's rendered messages name.
func (s *surface) checkSlashCommands(provider domain.Provider) {
	var shipped []string
	for _, command := range surfaceCommands {
		shipped = append(shipped, provider.Slash+strings.ReplaceAll(command, "/", ":"))
	}
	named := regexp.MustCompile(regexp.QuoteMeta(provider.Slash) + `[a-z]+(?::[a-z]+)?`)
	for _, file := range s.allSurfaceFiles() {
		for _, command := range named.FindAllString(s.surfaceText(file), -1) {
			if !slices.Contains(shipped, command) {
				s.t.Errorf("%s names %s, which the plugin does not ship", file, command)
			}
		}
	}
	// The Go core renders `${provider.Slash}<command>`: each must exist in every plugin.
	found := 0
	for _, dir := range []string{"render", "cli"} {
		root := filepath.Join(repoRoot, "internal", "core", dir)
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			for _, match := range regexp.MustCompile(`\.Slash\s*\+\s*"([a-z]+(?::[a-z]+)?)`).
				FindAllStringSubmatch(readFile(s.t, path), -1) {
				found++
				command := provider.Slash + match[1]
				if !slices.Contains(shipped, command) {
					s.t.Errorf("core (%s) renders %s, which the plugin does not ship", filepath.Base(path), command)
				}
			}
			return nil
		})
		if err != nil {
			s.t.Fatalf("walk %s: %v", root, err)
		}
	}
	if found == 0 {
		s.t.Errorf("the core renders no slash command at all; the scan found nothing to check")
	}
}

// checkRemoveAndReview is remove's undo line and review's crashed-job wording.
func (s *surface) checkRemoveAndReview(provider domain.Provider) {
	want := []invocation{{file: "commands/remove.md", command: "setup", flags: []string{"--remove"}}}
	if got := s.invocationsIn("commands/remove.md"); !reflect.DeepEqual(got, want) {
		s.t.Errorf("remove.md invokes %v, want %v", got, want)
	}
	if uninstall := "/plugin uninstall " + provider.Name + "-plugin-cc@muhmdraouf"; !strings.Contains(s.read("commands/remove.md"), uninstall) {
		s.t.Errorf("remove.md never says to run %s", uninstall)
	}
	for _, file := range []string{"commands/setup.md", "commands/setup/claude.md"} {
		text := s.read(file)
		if after := "run `" + provider.Slash + "remove`, then `/plugin uninstall"; !strings.Contains(text, after) {
			s.t.Errorf("%s never says to run %s", file, after)
		}
		if stale := "remove `env.ANTHROPIC_BASE_URL` first"; strings.Contains(text, stale) {
			s.t.Errorf("%s says %q, which remove no longer needs", file, stale)
		}
	}
	review := s.read("commands/review.md")
	var commands []string
	for _, call := range s.invocationsIn("commands/review.md") {
		commands = append(commands, call.command)
	}
	for _, command := range []string{"board", "review", "accept", "return", "discard"} {
		if !slices.Contains(commands, command) {
			s.t.Errorf("review.md never invokes %s", command)
		}
	}
	if !strings.Contains(review, "Never offer accept.") {
		s.t.Errorf("review.md never says \"Never offer accept.\"")
	}
	if !strings.Contains(review, "AskUserQuestion") {
		s.t.Errorf("review.md never asks with AskUserQuestion")
	}
}

// decodeJSON decodes one JSON document whose extra fields are none of the check's business.
func decodeJSON(t *testing.T, text string, out any) {
	t.Helper()
	if err := json.Unmarshal([]byte(text), out); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
}

// decodeStrictJSON decodes one JSON document, failing on a key the struct does not name.
func decodeStrictJSON(t *testing.T, text string, out any) {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
}
