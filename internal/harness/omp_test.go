package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// ompTestEntry answers the entry omp's setup gets: the fake binary and a router gateway on a
// port outside the reserved range. Never dialled: the files are compared as text.
func ompTestEntry() Entry {
	entry := NewEntry("omp", "/users/home/bin/huddle")
	entry.Gateway = "http://127.0.0.1:28786/gateway"
	return entry
}

// Setup writes omp's whole wiring into its agent directory: the MCP server in mcp.json, which
// omp reads before every imported source, and an override of the built-in anthropic provider
// in models.yml that points its base URL at the router's gateway and names omp on every
// request. Remove takes both files away again.
func TestOmpSetupWritesItsWiring(t *testing.T) {
	env, home := testEnv(t, map[string]bool{"omp": true})
	hudHome := t.TempDir()
	agent := filepath.Join(home, ".omp", "agent")
	got, err := Install(context.Background(), hudHome, env, "omp", ompTestEntry())
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if got.Path != filepath.Join(agent, "mcp.json") || got.Routing != filepath.Join(agent, "models.yml") {
		t.Fatalf("install wrote %q and %q, want mcp.json and models.yml under %s", got.Path, got.Routing, agent)
	}
	wantMCP := `{
  "mcpServers": {
    "huddle": {
      "type": "stdio",
      "command": "/users/home/bin/huddle",
      "args": [
        "mcp"
      ],
      "env": {
        "HUDDLE_HARNESS": "omp"
      },
      "timeout": 120000
    }
  }
}
`
	if raw := readConfig(t, got.Path); string(raw) != wantMCP {
		t.Errorf("mcp.json =\n%s\nwant\n%s", raw, wantMCP)
	}
	wantModels := `# huddle:begin
providers:
  anthropic:
    baseUrl: "http://127.0.0.1:28786/gateway"
    headers:
      X-Huddle-Harness: "omp"
# huddle:end
`
	if raw := readConfig(t, got.Routing); string(raw) != wantModels {
		t.Errorf("models.yml =\n%s\nwant\n%s", raw, wantModels)
	}
	var doc struct {
		Providers map[string]struct {
			BaseURL string            `yaml:"baseUrl"`
			Headers map[string]string `yaml:"headers"`
		} `yaml:"providers"`
	}
	if err := yaml.Unmarshal([]byte(wantModels), &doc); err != nil || doc.Providers["anthropic"].BaseURL != ompTestEntry().Gateway {
		t.Errorf("models.yml parses as %+v, %v; want the anthropic base URL at the gateway", doc, err)
	}
	removed, err := Remove(context.Background(), hudHome, env, "omp", ompTestEntry())
	if err != nil || !removed.FileGone {
		t.Fatalf("remove = %+v, %v; want the created mcp.json gone", removed, err)
	}
	if _, err := os.Stat(got.Routing); !os.IsNotExist(err) {
		t.Errorf("remove left the created models.yml behind: %v", err)
	}
}

// A models.yml the user already keeps gets huddle's block, at the top level when the file has
// no providers key and nested under the key when it has one; a second install changes nothing,
// and remove gives the file back byte for byte.
func TestOmpRoutingKeepsTheUsersModelsFile(t *testing.T) {
	for _, mine := range []string{
		"# my models\nmodelRoles:\n  default: anthropic/claude-sonnet-4-5\n",
		"providers:\n    zai:\n        apiKey: ZAI_API_KEY\ntheme: dark\n",
		"providers:\n",
	} {
		env, home := testEnv(t, nil)
		hudHome := t.TempDir()
		path := filepath.Join(home, ".omp", "agent", "models.yml")
		writeConfig(t, path, mine)
		if _, err := Install(context.Background(), hudHome, env, "omp", ompTestEntry()); err != nil {
			t.Fatalf("install over %q: %v", mine, err)
		}
		once := readConfig(t, path)
		var doc struct {
			Providers map[string]struct {
				BaseURL string `yaml:"baseUrl"`
			} `yaml:"providers"`
		}
		if err := yaml.Unmarshal(once, &doc); err != nil || doc.Providers["anthropic"].BaseURL != ompTestEntry().Gateway {
			t.Fatalf("install over %q gave %+v, %v:\n%s", mine, doc, err, once)
		}
		if _, err := Install(context.Background(), hudHome, env, "omp", ompTestEntry()); err != nil {
			t.Fatalf("second install: %v", err)
		}
		if twice := readConfig(t, path); string(twice) != string(once) {
			t.Errorf("the second install rewrote models.yml:\nfirst  %q\nsecond %q", once, twice)
		}
		if _, err := Remove(context.Background(), hudHome, env, "omp", ompTestEntry()); err != nil {
			t.Fatalf("remove: %v", err)
		}
		if back := readConfig(t, path); string(back) != mine {
			t.Errorf("remove left %q, want the user's file %q back", back, mine)
		}
	}
}

// A models.yml that already configures the anthropic provider is the user's: install refuses
// it and the bytes stay as they were.
func TestOmpRoutingLeavesTheUsersAnthropicProvider(t *testing.T) {
	mine := "providers:\n  anthropic:\n    baseUrl: https://my.gateway/anthropic\n"
	out, err := ompRouting.Install([]byte(mine), ompTestEntry())
	if !errors.Is(err, ErrProviderTaken) || out != nil {
		t.Errorf("install = %q, %v; want ErrProviderTaken", out, err)
	}
}

// omp's agent directory moves with PI_CODING_AGENT_DIR, else with PI_CONFIG_DIR's root name.
func TestOmpAgentDirFollowsItsVariables(t *testing.T) {
	vars := map[string]string{"HOME": "/users/home"}
	env := Env{Lookup: func(key string) string { return vars[key] }}
	if got := omp.ConfigPath(env); got != filepath.Join("/users/home", ".omp", "agent", "mcp.json") {
		t.Errorf("ConfigPath = %q, want the default agent dir", got)
	}
	vars["PI_CONFIG_DIR"] = ".omp-work"
	if got := ompRouting.ConfigPath(env); got != filepath.Join("/users/home", ".omp-work", "agent", "models.yml") {
		t.Errorf("routing path = %q, want it under PI_CONFIG_DIR", got)
	}
	vars["PI_CODING_AGENT_DIR"] = "/users/agent"
	if got := omp.ConfigPath(env); got != filepath.Join("/users/agent", "mcp.json") {
		t.Errorf("ConfigPath = %q, want it under PI_CODING_AGENT_DIR", got)
	}
}

// An entry with no gateway names no routing: install writes the MCP server only.
func TestOmpWithoutARouterWritesNoRouting(t *testing.T) {
	env, home := testEnv(t, nil)
	got, err := Install(context.Background(), t.TempDir(), env, "omp", NewEntry("omp", "/users/home/bin/huddle"))
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if got.Routing != "" {
		t.Errorf("routing = %q, want none", got.Routing)
	}
	if _, err := os.Stat(filepath.Join(home, ".omp", "agent", "models.yml")); !os.IsNotExist(err) {
		t.Errorf("install wrote a models.yml with no gateway to name: %v", err)
	}
}
