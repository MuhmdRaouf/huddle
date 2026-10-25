// opencode keeps its MCP servers under mcp in opencode.json, the command the whole argv array
// and the env under "environment". Its model requests reach huddle-router from the same file:
// provider.anthropic's options name the router's /gateway Anthropic route as baseURL, plain
// HTTP on loopback, so no proxy and no CA are involved, and a header names opencode on radar's
// route line. The gateway forwards each request with the client's own key to Anthropic.
package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
)

// opencode is the opencode adapter.
var opencode = jsonAdapter{
	name:   "opencode",
	parent: "mcp",
	path:   openCodeConfigPath,
	detect: func(env Env) bool {
		return env.which("opencode") || dirThere(xdgDir(env, "opencode"))
	},
	build: openCodeBuild,
}

// openCodeConfigPath answers the global opencode.json.
func openCodeConfigPath(env Env) string {
	return filepath.Join(xdgDir(env, "opencode"), "opencode.json")
}

// openCodeEntry is the record opencode reads: a local server runs one argv. The timeout is
// opencode's per-request ms, 5000 when the entry names none — shorter than a wait's long
// poll, so the entry always names bridgeRequestTimeoutMS.
type openCodeEntry struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command"`
	Environment map[string]string `json:"environment"`
	Timeout     int64             `json:"timeout"`
}

// openCodeBuild marshals the opencode shape.
func openCodeBuild(e Entry) ([]byte, error) {
	env := e.Env
	if env == nil {
		env = map[string]string{}
	}
	return json.Marshal(openCodeEntry{
		Type:        "local",
		Command:     append([]string{e.Command}, e.Args...),
		Environment: env,
		Timeout:     bridgeRequestTimeoutMS,
	})
}

// openCodeRouting is opencode's routing: provider.anthropic in the same opencode.json.
var openCodeRouting = openCodeProviderEdit{}

// openCodeProvider is the provider record huddle writes: the AI SDK's Anthropic options.
type openCodeProvider struct {
	Options openCodeProviderOptions `json:"options"`
}

// openCodeProviderOptions names the base URL and the harness header. The AI SDK posts to
// baseURL + "/messages", so the base URL carries the gateway's /v1.
type openCodeProviderOptions struct {
	BaseURL string            `json:"baseURL"`
	Headers map[string]string `json:"headers"`
}

// openCodeProviderEdit splices provider.anthropic into opencode.json and back out, every
// other byte where it was.
type openCodeProviderEdit struct{}

// ConfigPath answers opencode.json, the file the MCP server is in too.
func (openCodeProviderEdit) ConfigPath(env Env) string { return openCodeConfigPath(env) }

func (openCodeProviderEdit) ledgerName() string { return "opencode-routing" }

// Install answers the config with huddle's anthropic provider in it: added when there is none,
// rewritten when it is ours on another gateway. An anthropic provider of the user's own is
// ErrProviderTaken, and nothing is written.
func (openCodeProviderEdit) Install(cfg []byte, entry Entry) ([]byte, error) {
	if cfg == nil {
		cfg = []byte("{}\n")
	}
	if err := jsonCheck(cfg); err != nil {
		return nil, err
	}
	want, err := openCodeProviderBuild(entry)
	if err != nil {
		return nil, err
	}
	provider, err := jsonFind(cfg, 0, "provider")
	if err != nil {
		return nil, err
	}
	if provider == nil {
		return jsonInsertMember(cfg, 0, fmt.Sprintf(`"provider":{"anthropic":%s}`, want))
	}
	members, _, err := jsonMembers(cfg, provider.value.start)
	if err != nil {
		return nil, errors.New("the provider member is not a JSON object")
	}
	mine := jsonMemberNamed(members, "anthropic")
	switch {
	case mine == nil:
		return jsonInsertMember(cfg, provider.value.start, `"anthropic":`+string(want))
	case jsonEqual(cfg[mine.value.start:mine.value.end], want):
		return cfg, nil
	case openCodeProviderOurs(cfg[mine.value.start:mine.value.end], entry):
		return jsonReplaceValue(cfg, *mine, string(want)), nil
	}
	return nil, ErrProviderTaken
}

// Remove answers the config with huddle's anthropic provider gone, and the provider object
// with it when nothing else is left in it. A provider that is not ours is ErrEntryChanged,
// and the bytes come back untouched.
func (openCodeProviderEdit) Remove(cfg []byte, entry Entry) ([]byte, error) {
	if err := jsonCheck(cfg); err != nil {
		return nil, err
	}
	provider, err := jsonFind(cfg, 0, "provider")
	if err != nil {
		return nil, err
	}
	if provider == nil {
		return cfg, ErrEntryChanged
	}
	members, _, err := jsonMembers(cfg, provider.value.start)
	if err != nil {
		return nil, errors.New("the provider member is not a JSON object")
	}
	want, err := openCodeProviderBuild(entry)
	if err != nil {
		return nil, err
	}
	mine := jsonMemberNamed(members, "anthropic")
	if mine == nil || !jsonEqual(cfg[mine.value.start:mine.value.end], want) {
		return cfg, ErrEntryChanged
	}
	next := jsonCutMember(cfg, *mine)
	if inner, _, err := jsonMembers(next, provider.value.start); err == nil && len(inner) == 0 {
		if table, err := jsonFind(next, 0, "provider"); err == nil && table != nil {
			next = jsonCutMember(next, *table)
		}
	}
	return next, nil
}

// openCodeProviderBuild marshals the provider record for the entry's gateway.
func openCodeProviderBuild(entry Entry) ([]byte, error) {
	return json.Marshal(openCodeProvider{Options: openCodeProviderOptions{
		BaseURL: entry.Gateway + "/v1",
		Headers: map[string]string{harnessHeaderName: entry.Env["HUDDLE_HARNESS"]},
	}})
}

// openCodeProviderOurs answers whether a provider record is one huddle wrote, whatever gateway
// it names: exactly the options huddle writes, the header naming this harness.
func openCodeProviderOurs(raw []byte, entry Entry) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var found openCodeProvider
	if dec.Decode(&found) != nil {
		return false
	}
	return len(found.Options.Headers) == 1 && found.Options.Headers[harnessHeaderName] == entry.Env["HUDDLE_HARNESS"]
}
