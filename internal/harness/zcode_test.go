package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// zcodeTestEntry answers the entry zcode's setup gets: the fake binary and a router gateway on
// a port outside the reserved range. Never dialled: the files are compared as text.
func zcodeTestEntry() Entry {
	entry := NewEntry("zcode", "/users/home/bin/huddle")
	entry.Gateway = "http://127.0.0.1:28786/gateway"
	return entry
}

// zcodeOverrides is the two provider rules install adds to providerRules, as one array's items.
const zcodeOverrides = `{"providerId":"account:zai-individual-coding-plan","config":{"api":{"baseUrl":"http://127.0.0.1:28786/gateway","headers":{"X-Huddle-Harness":"zcode"}}}},` +
	`{"providerId":"account:zai-team-coding-plan","config":{"api":{"baseUrl":"http://127.0.0.1:28786/gateway","headers":{"X-Huddle-Harness":"zcode"}}}}`

// Setup writes zcode's whole wiring: the MCP server under mcp.servers in its user config, and
// a personal override of the two Z.ai coding plan providers whose base URL is the router's
// /gateway, naming zcode on every request. Remove takes both created files away again.
func TestZcodeSetupWritesItsWiring(t *testing.T) {
	env, home := testEnv(t, map[string]bool{"zcode": true})
	hudHome := t.TempDir()
	got, err := Install(context.Background(), hudHome, env, "zcode", zcodeTestEntry())
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	wantPath := filepath.Join(home, ".zcode", "cli", "config.json")
	wantRouting := filepath.Join(home, ".zcode", "v2", "provider_config.json")
	if got.Path != wantPath || got.Routing != wantRouting {
		t.Fatalf("install wrote %q and %q, want %s and %s", got.Path, got.Routing, wantPath, wantRouting)
	}
	wantMCP := `{
  "mcp": {
    "servers": {
      "huddle": {
        "type": "stdio",
        "command": "/users/home/bin/huddle",
        "args": [
          "mcp"
        ],
        "env": {
          "HUDDLE_HARNESS": "zcode"
        },
        "timeoutMs": 120000
      }
    }
  }
}
`
	if raw := readConfig(t, got.Path); string(raw) != wantMCP {
		t.Errorf("config.json =\n%s\nwant\n%s", raw, wantMCP)
	}
	wantProviders := `{
  "schemaVersion": 1,
  "config": {
    "providerConfigRules": {
      "providerRules": [` + zcodeOverrides + `]
    },
    "modelConfigRules": {
      "providerModelRules": [],
      "manualProviderModelRules": []
    }
  }
}
`
	if raw := readConfig(t, got.Routing); string(raw) != wantProviders {
		t.Errorf("provider_config.json =\n%s\nwant\n%s", raw, wantProviders)
	}
	removed, err := Remove(context.Background(), hudHome, env, "zcode", zcodeTestEntry())
	if err != nil || !removed.FileGone {
		t.Fatalf("remove = %+v, %v; want the created config.json gone", removed, err)
	}
	if _, err := os.Stat(got.Routing); !os.IsNotExist(err) {
		t.Errorf("remove left the created provider_config.json behind: %v", err)
	}
}

// zcodeOwnFile is a provider_config.json the way zcode itself writes it: indented, with the
// default model the user picked.
const zcodeOwnFile = `{
  "schemaVersion": 1,
  "config": {
    "providerOrder": [],
    "providerConfigRules": {
      "providerRules": []
    },
    "modelConfigRules": {
      "providerModelRules": [],
      "manualProviderModelRules": []
    },
    "defaultModelSelection": {
      "providerId": "account:zai-individual-coding-plan",
      "modelId": "GLM-5.3"
    }
  }
}
`

// A provider_config.json zcode already keeps gets the two overrides in its providerRules; a
// second install changes nothing, and remove gives the file back byte for byte, whatever other
// rule the user keeps there.
func TestZcodeRoutingKeepsZcodesOwnFile(t *testing.T) {
	theirs := strings.Replace(zcodeOwnFile, `"providerRules": []`,
		`"providerRules": [{"providerId":"mine","config":{"group":"standard-personal"}}]`, 1)
	for _, mine := range []string{zcodeOwnFile, theirs} {
		env, home := testEnv(t, nil)
		hudHome := t.TempDir()
		path := filepath.Join(home, ".zcode", "v2", "provider_config.json")
		writeConfig(t, path, mine)
		if _, err := Install(context.Background(), hudHome, env, "zcode", zcodeTestEntry()); err != nil {
			t.Fatalf("install over %q: %v", mine, err)
		}
		once := readConfig(t, path)
		if !strings.Contains(string(once), zcodeOverrides) || !strings.Contains(string(once), `"defaultModelSelection"`) {
			t.Fatalf("install over the user's file gave:\n%s", once)
		}
		if _, err := Install(context.Background(), hudHome, env, "zcode", zcodeTestEntry()); err != nil {
			t.Fatalf("second install: %v", err)
		}
		if twice := readConfig(t, path); string(twice) != string(once) {
			t.Errorf("the second install rewrote provider_config.json:\nfirst  %q\nsecond %q", once, twice)
		}
		if _, err := Remove(context.Background(), hudHome, env, "zcode", zcodeTestEntry()); err != nil {
			t.Fatalf("remove: %v", err)
		}
		if back := readConfig(t, path); string(back) != mine {
			t.Errorf("remove left %q, want the user's file %q back", back, mine)
		}
	}
}

// A rule the user wrote for a coding plan provider stays theirs: install refuses the routing
// and writes nothing of it.
func TestZcodeLeavesAPlanTheUserConfigured(t *testing.T) {
	env, home := testEnv(t, nil)
	path := filepath.Join(home, ".zcode", "v2", "provider_config.json")
	theirs := strings.Replace(zcodeOwnFile, `"providerRules": []`,
		`"providerRules": [{"providerId":"account:zai-team-coding-plan","config":{"visibility":"hidden"}}]`, 1)
	writeConfig(t, path, theirs)
	_, err := Install(context.Background(), t.TempDir(), env, "zcode", zcodeTestEntry())
	if !errors.Is(err, ErrZcodeProviderTaken) {
		t.Fatalf("install over the user's own plan rule = %v, want ErrZcodeProviderTaken", err)
	}
	if raw := readConfig(t, path); string(raw) != theirs {
		t.Errorf("install changed the user's provider_config.json:\n%s", raw)
	}
}

// zcode rewrites provider_config.json itself: an override still holding huddle's values is
// taken out wherever the rewrite put it, and one the user changed stays, the rest with it.
func TestZcodeRemoveAfterZcodeRewroteTheFile(t *testing.T) {
	env, home := testEnv(t, nil)
	hudHome := t.TempDir()
	path := filepath.Join(home, ".zcode", "v2", "provider_config.json")
	writeConfig(t, path, zcodeOwnFile)
	if _, err := Install(context.Background(), hudHome, env, "zcode", zcodeTestEntry()); err != nil {
		t.Fatalf("install: %v", err)
	}
	changed := strings.Replace(string(readConfig(t, path)), `"baseUrl":"http://127.0.0.1:28786/gateway"`,
		`"baseUrl":"https://my.proxy.example"`, 1)
	writeConfig(t, path, changed)
	if _, err := Remove(context.Background(), hudHome, env, "zcode", zcodeTestEntry()); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if raw := readConfig(t, path); string(raw) != changed {
		t.Errorf("remove touched a file whose override the user changed:\n%s", raw)
	}

	writeConfig(t, path, zcodeOwnFile)
	if _, err := Install(context.Background(), hudHome, env, "zcode", zcodeTestEntry()); err != nil {
		t.Fatalf("install: %v", err)
	}
	// the same rules, laid out the way zcode's own writer indents them
	reflowed := strings.Replace(string(readConfig(t, path)), "},{", "},\n        {", -1)
	writeConfig(t, path, reflowed)
	if _, err := Remove(context.Background(), hudHome, env, "zcode", zcodeTestEntry()); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if raw := readConfig(t, path); strings.Contains(string(raw), "X-Huddle-Harness") || !strings.Contains(string(raw), `"defaultModelSelection"`) {
		t.Errorf("remove after a rewrite left:\n%s", raw)
	}
}

// A provider_config.json without the rules array gets the levels it lacks around huddle's two
// rules, and the rest of the file stays.
func TestZcodeRoutingAddsTheRulesArrayItLacks(t *testing.T) {
	for _, mine := range []string{"{\"schemaVersion\": 1}\n", "{\"config\": {\"providerOrder\": []}}\n"} {
		out, err := zcodeRouting.Install([]byte(mine), zcodeTestEntry())
		if err != nil {
			t.Fatalf("install over %q: %v", mine, err)
		}
		if !strings.Contains(string(out), `"providerRules":[`+zcodeOverrides+`]`) {
			t.Errorf("install over %q gave %s, want the rules array with both overrides", mine, out)
		}
		if err := jsonCheck(out); err != nil {
			t.Errorf("install over %q gave JSON that does not parse: %v\n%s", mine, err, out)
		}
	}
}
