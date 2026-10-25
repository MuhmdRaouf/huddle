// omp (oh-my-pi) reads its MCP servers from mcp.json in its agent directory, ahead of every
// source it imports from other tools, so huddle's entry there is the one omp runs, naming omp,
// whatever Claude Code's plugin or Codex's config also declare under the same name. omp's
// model requests reach huddle-router through models.yml in the same directory: an
// override-only entry for the built-in anthropic provider points its baseUrl at the router's
// /gateway, plain HTTP on loopback, so no proxy and no CA are involved (omp's provider fetches
// take a proxy and a CA from the process environment only, which plug and play never sets).
// The gateway forwards each request with the client's own key to Anthropic, and the harness
// header names omp on radar's route line. huddle keeps its lines between marker comments and
// never rewrites anything outside them.
package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// omp is the oh-my-pi adapter: the MCP server in the agent directory's mcp.json.
var omp = jsonAdapter{
	name:   "omp",
	parent: "mcpServers",
	path: func(env Env) string {
		return filepath.Join(ompAgentDir(env), "mcp.json")
	},
	detect: func(env Env) bool {
		return env.which("omp") || dirThere(homeDir(env, ompRootName(env)))
	},
	build: ompBuild,
}

// ompRouting is omp's routing file: the agent directory's models.yml.
var ompRouting = modelsBlock{
	ledger: "omp-routing",
	path: func(env Env) string {
		return filepath.Join(ompAgentDir(env), "models.yml")
	},
}

// ompRootName answers omp's config root under the home: $PI_CONFIG_DIR, else .omp.
func ompRootName(env Env) string {
	if name := env.lookup("PI_CONFIG_DIR"); name != "" {
		return name
	}
	return ".omp"
}

// ompAgentDir answers the default profile's agent directory: $PI_CODING_AGENT_DIR, else the
// root's agent folder.
func ompAgentDir(env Env) string {
	if dir := env.lookup("PI_CODING_AGENT_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(homeDir(env, ompRootName(env)), "agent")
}

// ompEntry is the stdio record omp's mcp.json schema reads. The timeout is omp's per-request
// ms, 30000 when the entry names none — shorter than a wait's long poll.
type ompEntry struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Timeout int64             `json:"timeout"`
}

// ompBuild marshals the omp shape.
func ompBuild(e Entry) ([]byte, error) {
	env := e.Env
	if env == nil {
		env = map[string]string{}
	}
	return json.Marshal(ompEntry{
		Type: "stdio", Command: e.Command, Args: e.Args, Env: env, Timeout: bridgeRequestTimeoutMS,
	})
}

// ErrProviderTaken is the routing install's answer when the user's config already configures
// the anthropic provider: theirs stays, and huddle writes nothing of its routing.
var ErrProviderTaken = errors.New("the anthropic provider is already configured here; huddle leaves it as it is")

// modelsBlock is a models.yml huddle adds its anthropic provider override to, between the
// same marker lines the Codex block uses (comments to YAML). A file with no top-level
// providers key gets the whole key in the block; a file with one gets the anthropic entry
// nested under it.
type modelsBlock struct {
	ledger string // the ledger record's name
	path   func(Env) string
}

// ConfigPath answers the models.yml file.
func (m modelsBlock) ConfigPath(env Env) string { return m.path(env) }

func (m modelsBlock) ledgerName() string { return m.ledger }

// providersLine matches the top-level providers key in block style.
var providersLine = regexp.MustCompile(`^providers:[ \t]*(#.*)?$`)

// Install answers the file with huddle's block in it: added when there is none, replaced when
// the block is there but no longer ours. The answer must parse as YAML, or nothing is written.
func (m modelsBlock) Install(cfg []byte, entry Entry) ([]byte, error) {
	if err := yamlCheck(cfg); err != nil {
		return nil, err
	}
	var out []byte
	if block, found := findMarked(cfg, tomlBegin, tomlEnd); found {
		want := m.innerLike(block.inner, entry)
		if block.inner == want {
			return cfg, nil
		}
		out = append(append(append([]byte{}, cfg[:block.innerStart]...), want...), cfg[block.innerEnd:]...)
	} else {
		var err error
		if out, err = m.add(cfg, entry); err != nil {
			return nil, err
		}
	}
	if err := yamlCheck(out); err != nil {
		return nil, err
	}
	return out, nil
}

// add places a new block: nested right under the user's providers key, or, with none, the
// whole key appended after one newline the remove's cut takes back.
func (m modelsBlock) add(cfg []byte, entry Entry) ([]byte, error) {
	lines := strings.SplitAfter(string(cfg), "\n")
	for i, line := range lines {
		if !providersLine.MatchString(strings.TrimRight(line, "\r\n")) {
			continue
		}
		indent, taken := providerChildren(lines[i+1:])
		if taken {
			return nil, ErrProviderTaken
		}
		head := strings.Join(lines[:i+1], "")
		if !strings.HasSuffix(head, "\n") {
			head += "\n"
		}
		block := tomlBegin + "\n" + nestedInner(indent, entry) + tomlEnd + "\n"
		return []byte(head + block + strings.Join(lines[i+1:], "")), nil
	}
	lead := ""
	if len(cfg) > 0 {
		lead = "\n"
	}
	return append(append([]byte{}, cfg...), lead+tomlBegin+"\n"+ownedInner(entry)+tomlEnd+"\n"...), nil
}

// Remove answers the file with huddle's block gone. A block that is not there, or is no longer
// ours, is ErrEntryChanged, and the bytes come back untouched.
func (m modelsBlock) Remove(cfg []byte, entry Entry) ([]byte, error) {
	block, found := findMarked(cfg, tomlBegin, tomlEnd)
	if !found || block.inner != m.innerLike(block.inner, entry) {
		return cfg, ErrEntryChanged
	}
	start := block.cutStart
	if !strings.HasPrefix(block.inner, "providers:") && start < len(cfg) && cfg[start] == '\n' {
		// a nested block sits right after the providers line, whose newline stays
		start++
	}
	return append(append([]byte{}, cfg[:start]...), cfg[block.cutEnd:]...), nil
}

// innerLike answers the inner text install writes in the shape the block found has: the whole
// providers key, or the anthropic entry nested at the indent the block uses.
func (modelsBlock) innerLike(found string, entry Entry) string {
	if strings.HasPrefix(found, "providers:") {
		return ownedInner(entry)
	}
	indent := found[:len(found)-len(strings.TrimLeft(found, " "))]
	if indent == "" {
		indent = "  "
	}
	return nestedInner(indent, entry)
}

// ownedInner is the block of a file with no providers key: the key and the entry.
func ownedInner(entry Entry) string {
	return "providers:\n" + nestedInner("  ", entry)
}

// nestedInner is the anthropic entry one indent deep, its fields one more. Values ride as JSON
// strings, which YAML reads as double-quoted scalars.
func nestedInner(indent string, entry Entry) string {
	return indent + "anthropic:\n" +
		indent + indent + "baseUrl: " + jsonString(entry.Gateway) + "\n" +
		indent + indent + "headers:\n" +
		indent + indent + indent + harnessHeaderName + ": " + jsonString(entry.Env["HUDDLE_HARNESS"]) + "\n"
}

// harnessHeaderName is the header the router's gateway names a client by.
const harnessHeaderName = "X-Huddle-Harness"

// providerChildren reads the providers key's children: the indent they use ("  " when there
// are none) and whether one of them is anthropic.
func providerChildren(rest []string) (indent string, taken bool) {
	for _, line := range rest {
		text := strings.TrimRight(line, "\r\n")
		trimmed := strings.TrimLeft(text, " ")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if len(trimmed) == len(text) {
			break // back at the top level
		}
		lead := text[:len(text)-len(trimmed)]
		if indent == "" {
			indent = lead
		}
		if lead == indent && (strings.HasPrefix(trimmed, "anthropic:") || strings.HasPrefix(trimmed, `"anthropic":`)) {
			taken = true
		}
	}
	if indent == "" {
		indent = "  "
	}
	return indent, taken
}

// yamlCheck parses the file far enough to know huddle is not editing garbage.
func yamlCheck(cfg []byte) error {
	var doc yaml.Node
	if err := yaml.NewDecoder(bytes.NewReader(cfg)).Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
