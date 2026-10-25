// zcode (the CLI inside ZCode.app) reads its MCP servers from mcp.servers in its user config,
// ~/.zcode/cli/config.json. Its model requests reach huddle-router through its personal provider
// file, ~/.zcode/v2/provider_config.json: zcode lays a personal provider rule over the built-in
// one of the same id, so a rule naming only api.baseUrl and api.headers points the two Z.ai
// coding plan providers (individual and team, whose own upstream is Z.ai's Anthropic route) at
// the router's /gateway, plain HTTP on loopback, so no proxy and no CA are involved. zcode posts
// to baseUrl + "/v1/messages" with the plan's own key, the gateway's built-in GLM route forwards
// it to Z.ai, and the harness header names zcode on radar's route line. The start, off-peak,
// highspeed and BigModel plans speak other upstreams and keep their own.
package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
)

// zcode is the zcode adapter: the MCP server under mcp.servers in the user config.
var zcode = jsonAdapter{
	name:   "zcode",
	parent: "mcp",
	child:  "servers",
	path: func(env Env) string {
		return filepath.Join(homeDir(env, ".zcode"), "cli", "config.json")
	},
	detect: func(env Env) bool {
		return env.which("zcode") || dirThere(homeDir(env, ".zcode"))
	},
	build: zcodeBuild,
}

// zcodeEntry is the stdio record zcode's config schema reads. timeoutMs is zcode's per-request
// bound; the entry names bridgeRequestTimeoutMS so a wait outlasts its long poll.
type zcodeEntry struct {
	Type      string            `json:"type"`
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	Env       map[string]string `json:"env"`
	TimeoutMs int64             `json:"timeoutMs"`
}

// zcodeBuild marshals the zcode shape.
func zcodeBuild(e Entry) ([]byte, error) {
	env := e.Env
	if env == nil {
		env = map[string]string{}
	}
	return json.Marshal(zcodeEntry{
		Type: "stdio", Command: e.Command, Args: e.Args, Env: env, TimeoutMs: bridgeRequestTimeoutMS,
	})
}

// zcodePlans are the providers huddle routes: the Z.ai coding plans whose built-in upstream is
// Z.ai's Anthropic route, the one the gateway's GLM route forwards to.
var zcodePlans = [...]string{"account:zai-individual-coding-plan", "account:zai-team-coding-plan"}

// ErrZcodeProviderTaken is the routing install's answer when the user's provider_config.json
// already holds a rule of their own for a coding plan provider: theirs stays, and huddle writes
// nothing of its routing.
var ErrZcodeProviderTaken = errors.New("a Z.ai coding plan provider is already configured here; huddle leaves it as it is")

// zcodeRouting is zcode's routing file: the personal provider_config.json.
var zcodeRouting = zcodeProviderEdit{}

// zcodeProviderEdit splices huddle's provider rules into providerRules and back out, every other
// byte where it was.
type zcodeProviderEdit struct{}

// ConfigPath answers the personal provider file: $ZCODE_PERSONAL_PROVIDER_CONFIG_FILE, else the
// one under ~/.zcode/v2.
func (zcodeProviderEdit) ConfigPath(env Env) string {
	if path := env.lookup("ZCODE_PERSONAL_PROVIDER_CONFIG_FILE"); path != "" {
		return path
	}
	return filepath.Join(homeDir(env, ".zcode"), "v2", "provider_config.json")
}

func (zcodeProviderEdit) ledgerName() string { return "zcode-routing" }

// zcodeRulesPath answers the objects the providerRules array sits in.
func zcodeRulesPath() []string { return []string{"config", "providerConfigRules"} }

// zcodeSkeleton is the file install creates when zcode has written none: the parts zcode needs
// to read a personal file, with no rule yet. Remove compares against it to know the file holds
// nothing but what install put there.
const zcodeSkeleton = `{
  "schemaVersion": 1,
  "config": {
    "providerConfigRules": {
      "providerRules": []
    },
    "modelConfigRules": {
      "providerModelRules": [],
      "manualProviderModelRules": []
    }
  }
}
`

// Install answers the file with huddle's two rules in providerRules: added when there are none,
// rewritten when they are ours on another gateway. A rule of the user's own for either plan is
// ErrZcodeProviderTaken, and nothing is written.
func (zcodeProviderEdit) Install(cfg []byte, entry Entry) ([]byte, error) {
	if cfg == nil {
		cfg = []byte(zcodeSkeleton)
	}
	if err := jsonCheck(cfg); err != nil {
		return nil, err
	}
	out := cfg
	for _, plan := range zcodePlans {
		want, err := zcodeRuleBuild(plan, entry)
		if err != nil {
			return nil, err
		}
		rules, err := zcodeRulesArray(out)
		if err != nil {
			return nil, err
		}
		if rules < 0 {
			table, missing, err := jsonFindTable(out, zcodeRulesPath())
			if err != nil {
				return nil, err
			}
			member := jsonNested(missing, fmt.Sprintf(`"providerRules":[%s]`, want))
			if out, err = jsonInsertMember(out, table, member); err != nil {
				return nil, err
			}
			continue
		}
		item, found, err := zcodeRuleOf(out, rules, plan)
		if err != nil {
			return nil, err
		}
		switch {
		case !found:
			if out, err = jsonInsertArrayItem(out, rules, string(want)); err != nil {
				return nil, err
			}
		case jsonEqual(out[item.start:item.end], want):
		case zcodeRuleOurs(out[item.start:item.end], plan, entry):
			out = jsonReplaceValue(out, jsonMember{value: item}, string(want))
		default:
			return nil, ErrZcodeProviderTaken
		}
	}
	return out, nil
}

// Remove answers the file with huddle's two rules gone. A rule that is not there, or is no
// longer ours, is ErrEntryChanged, and the bytes come back untouched. A file holding nothing
// but the skeleton install created comes back empty, so remove takes the created file away.
func (zcodeProviderEdit) Remove(cfg []byte, entry Entry) ([]byte, error) {
	if err := jsonCheck(cfg); err != nil {
		return nil, err
	}
	out := cfg
	for _, plan := range zcodePlans {
		want, err := zcodeRuleBuild(plan, entry)
		if err != nil {
			return nil, err
		}
		rules, err := zcodeRulesArray(out)
		if err != nil {
			return nil, err
		}
		if rules < 0 {
			return cfg, ErrEntryChanged
		}
		item, found, err := zcodeRuleOf(out, rules, plan)
		if err != nil {
			return nil, err
		}
		if !found || !jsonEqual(out[item.start:item.end], want) {
			return cfg, ErrEntryChanged
		}
		out = jsonCutMember(out, jsonMember{span: item})
	}
	if bytes.Equal(out, []byte(zcodeSkeleton)) {
		return nil, nil
	}
	return out, nil
}

// zcodeRulesArray answers where the providerRules array opens, -1 when the file has none yet.
func zcodeRulesArray(cfg []byte) (int, error) {
	table, missing, err := jsonFindTable(cfg, zcodeRulesPath())
	if err != nil || len(missing) > 0 {
		return -1, err
	}
	member, err := jsonFind(cfg, table, "providerRules")
	if err != nil || member == nil {
		return -1, err
	}
	if _, _, err := jsonArrayItems(cfg, member.value.start); err != nil {
		return 0, errors.New("the providerRules member is not a JSON array")
	}
	return member.value.start, nil
}

// zcodeRuleOf answers the providerRules item whose providerId is the plan.
func zcodeRuleOf(cfg []byte, rules int, plan string) (span, bool, error) {
	items, _, err := jsonArrayItems(cfg, rules)
	if err != nil {
		return span{}, false, err
	}
	for _, item := range items {
		var rule struct {
			ProviderID string `json:"providerId"`
		}
		if json.Unmarshal(cfg[item.start:item.end], &rule) == nil && rule.ProviderID == plan {
			return item, true, nil
		}
	}
	return span{}, false, nil
}

// zcodeRule is the provider rule huddle writes: the plan's id and its api override.
type zcodeRule struct {
	ProviderID string          `json:"providerId"`
	Config     zcodeRuleConfig `json:"config"`
}

// zcodeRuleConfig holds only the api override; the plan's access, group and models stay the
// built-in config's.
type zcodeRuleConfig struct {
	API zcodeRuleAPI `json:"api"`
}

// zcodeRuleAPI names the base URL and the harness header. zcode posts to baseUrl +
// "/v1/messages", so the base URL is the gateway itself.
type zcodeRuleAPI struct {
	BaseURL string            `json:"baseUrl"`
	Headers map[string]string `json:"headers"`
}

// zcodeRuleBuild marshals the rule for one plan on the entry's gateway.
func zcodeRuleBuild(plan string, entry Entry) ([]byte, error) {
	return json.Marshal(zcodeRule{ProviderID: plan, Config: zcodeRuleConfig{API: zcodeRuleAPI{
		BaseURL: entry.Gateway,
		Headers: map[string]string{harnessHeaderName: entry.Env["HUDDLE_HARNESS"]},
	}}})
}

// zcodeRuleOurs answers whether a rule is one huddle wrote, whatever gateway it names: exactly
// the fields huddle writes, the header naming this harness.
func zcodeRuleOurs(raw []byte, plan string, entry Entry) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var found zcodeRule
	if dec.Decode(&found) != nil {
		return false
	}
	headers := found.Config.API.Headers
	return found.ProviderID == plan && len(headers) == 1 && headers[harnessHeaderName] == entry.Env["HUDDLE_HARNESS"]
}
