package harness

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// piTestEntry answers the entry pi's setup gets: the fake binary and a router gateway on a
// port outside the reserved range. Never dialled: the files are compared as text.
func piTestEntry() Entry {
	entry := NewEntry("pi", "/users/home/bin/huddle")
	entry.Gateway = "http://127.0.0.1:28786/gateway"
	return entry
}

// piModels is the part of models.json the routing tests read back.
type piModels struct {
	Providers map[string]piProvider `json:"providers"`
}

// Setup writes pi's whole wiring into its agent directory: the MCP server in mcp.json, with a
// request timeout in seconds long enough for a wait, and an override of the built-in
// anthropic provider in models.json that points its base URL at the router's gateway and
// names pi on every request. Remove takes both created files away again.
func TestPiSetupWritesItsWiring(t *testing.T) {
	env, home := testEnv(t, map[string]bool{"pi": true})
	hudHome := t.TempDir()
	agent := filepath.Join(home, ".pi", "agent")
	got, err := Install(context.Background(), hudHome, env, "pi", piTestEntry())
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if got.Path != filepath.Join(agent, "mcp.json") || got.Routing != filepath.Join(agent, "models.json") {
		t.Fatalf("install wrote %q and %q, want mcp.json and models.json under %s", got.Path, got.Routing, agent)
	}
	wantMCP := `{
  "mcpServers": {
    "huddle": {
      "command": "/users/home/bin/huddle",
      "args": [
        "mcp"
      ],
      "env": {
        "HUDDLE_HARNESS": "pi"
      },
      "timeout": 120
    }
  }
}
`
	if raw := readConfig(t, got.Path); string(raw) != wantMCP {
		t.Errorf("mcp.json =\n%s\nwant\n%s", raw, wantMCP)
	}
	wantModels := `{
  "providers": {
    "anthropic": {
      "baseUrl": "http://127.0.0.1:28786/gateway",
      "headers": {
        "X-Huddle-Harness": "pi"
      }
    }
  }
}
`
	if raw := readConfig(t, got.Routing); string(raw) != wantModels {
		t.Errorf("models.json =\n%s\nwant\n%s", raw, wantModels)
	}
	removed, err := Remove(context.Background(), hudHome, env, "pi", piTestEntry())
	if err != nil || !removed.FileGone {
		t.Fatalf("remove = %+v, %v; want the created mcp.json gone", removed, err)
	}
	if _, err := os.Stat(got.Routing); !os.IsNotExist(err) {
		t.Errorf("remove left the created models.json behind: %v", err)
	}
}

// A models.json the user already keeps gets huddle's anthropic provider beside theirs; a second
// install changes nothing, a moved router port rewrites only the base URL, and remove gives
// the file back byte for byte.
func TestPiRoutingKeepsTheUsersModelsFile(t *testing.T) {
	for _, mine := range []string{
		"{}\n",
		"{\n  \"providers\": {\n    \"ollama\": {\"baseUrl\": \"http://localhost:11434/v1\", \"api\": \"openai-completions\"}\n  }\n}\n",
		"{\"$schema\":\"https://pi.dev/schemas/models.schema.json\"}",
	} {
		env, home := testEnv(t, nil)
		hudHome := t.TempDir()
		path := filepath.Join(home, ".pi", "agent", "models.json")
		writeConfig(t, path, mine)
		entry := piTestEntry()
		if _, err := Install(context.Background(), hudHome, env, "pi", entry); err != nil {
			t.Fatalf("install over %q: %v", mine, err)
		}
		once := readConfig(t, path)
		var doc piModels
		if err := json.Unmarshal(once, &doc); err != nil || doc.Providers["anthropic"].BaseURL != entry.Gateway {
			t.Fatalf("install over %q gave %+v, %v:\n%s", mine, doc, err, once)
		}
		if _, err := Install(context.Background(), hudHome, env, "pi", entry); err != nil {
			t.Fatalf("second install: %v", err)
		}
		if twice := readConfig(t, path); string(twice) != string(once) {
			t.Errorf("the second install rewrote models.json:\nfirst  %q\nsecond %q", once, twice)
		}
		moved := entry
		moved.Gateway = "http://127.0.0.1:28787/gateway"
		if _, err := Install(context.Background(), hudHome, env, "pi", moved); err != nil {
			t.Fatalf("install on a moved port: %v", err)
		}
		if err := json.Unmarshal(readConfig(t, path), &doc); err != nil || doc.Providers["anthropic"].BaseURL != moved.Gateway {
			t.Fatalf("a moved port gave %+v, %v", doc, err)
		}
		if _, err := Remove(context.Background(), hudHome, env, "pi", moved); err != nil {
			t.Fatalf("remove: %v", err)
		}
		if back := readConfig(t, path); string(back) != mine {
			t.Errorf("remove left %q, want the user's file %q back", back, mine)
		}
	}
}

// An anthropic provider the user configured is theirs: install refuses it and the bytes stay.
func TestPiRoutingLeavesTheUsersAnthropicProvider(t *testing.T) {
	mine := `{"providers":{"anthropic":{"baseUrl":"https://my.gateway"}}}`
	out, err := piRouting.Install([]byte(mine), piTestEntry())
	if !errors.Is(err, ErrProviderTaken) || out != nil {
		t.Errorf("install = %q, %v; want ErrProviderTaken", out, err)
	}
	if _, err := piRouting.Remove([]byte(mine), piTestEntry()); !errors.Is(err, ErrEntryChanged) {
		t.Errorf("remove = %v, want ErrEntryChanged", err)
	}
}

// pi's agent directory moves with PI_CODING_AGENT_DIR, both files with it.
func TestPiAgentDirFollowsItsVariable(t *testing.T) {
	vars := map[string]string{"HOME": "/users/home"}
	env := Env{Lookup: func(key string) string { return vars[key] }}
	if got := pi.ConfigPath(env); got != filepath.Join("/users/home", ".pi", "agent", "mcp.json") {
		t.Errorf("ConfigPath = %q, want the default agent dir", got)
	}
	vars["PI_CODING_AGENT_DIR"] = "/users/agent"
	if got := pi.ConfigPath(env); got != filepath.Join("/users/agent", "mcp.json") {
		t.Errorf("ConfigPath = %q, want it under PI_CODING_AGENT_DIR", got)
	}
	if got := piRouting.ConfigPath(env); got != filepath.Join("/users/agent", "models.json") {
		t.Errorf("routing path = %q, want it under PI_CODING_AGENT_DIR", got)
	}
}

// An entry with no gateway names no routing: install writes the MCP server only.
func TestPiWithoutARouterWritesNoRouting(t *testing.T) {
	env, home := testEnv(t, nil)
	got, err := Install(context.Background(), t.TempDir(), env, "pi", NewEntry("pi", "/users/home/bin/huddle"))
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if got.Routing != "" {
		t.Errorf("routing = %q, want none", got.Routing)
	}
	if _, err := os.Stat(filepath.Join(home, ".pi", "agent", "models.json")); !os.IsNotExist(err) {
		t.Errorf("install wrote a models.json with no gateway to name: %v", err)
	}
}
