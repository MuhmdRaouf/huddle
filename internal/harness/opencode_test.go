package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// opencode cuts every MCP server request off after 5 s unless the entry names a longer
// timeout, and huddle's wait, ask and depend tools hold a call for up to a minute. The
// record install writes therefore names one itself, or every blocking tool times out
// before the channel answers.

func TestOpenCodeRecordCarriesATimeoutLongEnoughForWait(t *testing.T) {
	raw, err := openCodeBuild(NewEntry("opencode", "/users/home/bin/huddle"))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	value, err := jsfmt.DecodeValue(raw)
	if err != nil {
		t.Fatalf("the record does not parse: %v\n%s", err, raw)
	}
	timeout, ok := field(value, "timeout")
	if !ok || timeout.Kind() != jsfmt.KindNumber {
		t.Fatalf("the opencode record carries no timeout: %s", raw)
	}
	ms, perr := strconv.ParseInt(timeout.EncodeJSON(), 10, 64)
	if perr != nil || ms < 60000 {
		t.Fatalf("timeout = %s ms, want at least 60000 so a wait survives its long poll: %s", timeout.EncodeJSON(), raw)
	}
}

// openCodeConfig is the part of opencode.json the routing tests read back.
type openCodeConfig struct {
	Provider map[string]openCodeProvider `json:"provider"`
}

// openCodeTestEntry answers the entry opencode's setup gets: the fake binary and a router
// gateway on a port outside the reserved range. Never dialled: the files are compared as text.
func openCodeTestEntry(home string) Entry {
	entry := testEntry(home, "opencode")
	entry.Gateway = "http://127.0.0.1:28786/gateway"
	return entry
}

// Setup writes opencode's whole wiring into its one opencode.json: the MCP server under mcp,
// the session hooks' plugin, and an anthropic provider whose base URL is the router's /gateway
// Anthropic route, naming opencode on every request. Remove takes the created file away again.
func TestOpencodeSetupWritesItsWiring(t *testing.T) {
	env, home := testEnv(t, map[string]bool{"opencode": true})
	hudHome := t.TempDir()
	entry := openCodeTestEntry(home)
	got, err := Install(context.Background(), hudHome, env, "opencode", entry)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if got.Path != opencode.ConfigPath(env) || got.Routing != got.Path {
		t.Fatalf("install wrote %q and routed in %q, want both in %s", got.Path, got.Routing, opencode.ConfigPath(env))
	}
	want := fmt.Sprintf(`{
  "mcp": {
    "huddle": {
      "type": "local",
      "command": [
        %q,
        "mcp"
      ],
      "environment": {
        "HUDDLE_HARNESS": "opencode"
      },
      "timeout": 120000
    }
  },"plugin":[%q],"provider":{"anthropic":{"options":{"baseURL":"http://127.0.0.1:28786/gateway/v1","headers":{"X-Huddle-Harness":"opencode"}}}}
}
`, entry.Command, "file://"+shimPath(hudHome))
	if raw := readConfig(t, got.Path); string(raw) != want {
		t.Errorf("opencode.json =\n%s\nwant\n%s", raw, want)
	}
	removed, err := Remove(context.Background(), hudHome, env, "opencode", entry)
	if err != nil || !removed.FileGone {
		t.Fatalf("remove = %+v, %v; want the created opencode.json gone", removed, err)
	}
}

// An opencode.json the user already keeps gets the provider beside theirs; a second install
// changes nothing, a moved router port rewrites only the base URL, and remove gives the file
// back byte for byte.
func TestOpencodeRoutingKeepsTheUsersConfig(t *testing.T) {
	for _, mine := range []string{
		"{\n  \"theme\": \"dark\"\n}\n",
		"{\"provider\":{\"zai\":{\"options\":{\"apiKey\":\"{env:ZAI_API_KEY}\"}}},\"model\":\"zai/glm-4.6\"}\n",
	} {
		env, home := testEnv(t, nil)
		hudHome := t.TempDir()
		path := opencode.ConfigPath(env)
		writeConfig(t, path, mine)
		entry := openCodeTestEntry(home)
		if _, err := Install(context.Background(), hudHome, env, "opencode", entry); err != nil {
			t.Fatalf("install over %q: %v", mine, err)
		}
		once := readConfig(t, path)
		var doc openCodeConfig
		if err := json.Unmarshal(once, &doc); err != nil || doc.Provider["anthropic"].Options.BaseURL != entry.Gateway+"/v1" {
			t.Fatalf("install over %q gave %+v, %v:\n%s", mine, doc, err, once)
		}
		if _, err := Install(context.Background(), hudHome, env, "opencode", entry); err != nil {
			t.Fatalf("second install: %v", err)
		}
		if twice := readConfig(t, path); string(twice) != string(once) {
			t.Errorf("the second install rewrote opencode.json:\nfirst  %q\nsecond %q", once, twice)
		}
		moved := entry
		moved.Gateway = "http://127.0.0.1:28787/gateway"
		if _, err := Install(context.Background(), hudHome, env, "opencode", moved); err != nil {
			t.Fatalf("install on a moved port: %v", err)
		}
		if err := json.Unmarshal(readConfig(t, path), &doc); err != nil || doc.Provider["anthropic"].Options.BaseURL != moved.Gateway+"/v1" {
			t.Fatalf("a moved port gave %+v, %v", doc, err)
		}
		if _, err := Remove(context.Background(), hudHome, env, "opencode", moved); err != nil {
			t.Fatalf("remove: %v", err)
		}
		if back := readConfig(t, path); string(back) != mine {
			t.Errorf("remove left %q, want the user's file %q back", back, mine)
		}
	}
}

// An anthropic provider the user configured is theirs: install refuses it and the bytes stay.
func TestOpencodeRoutingLeavesTheUsersAnthropicProvider(t *testing.T) {
	mine := `{"provider":{"anthropic":{"options":{"baseURL":"https://my.gateway/v1"}}}}`
	out, err := openCodeRouting.Install([]byte(mine), openCodeTestEntry("/users/home"))
	if !errors.Is(err, ErrProviderTaken) || out != nil {
		t.Errorf("install = %q, %v; want ErrProviderTaken", out, err)
	}
	if _, err := openCodeRouting.Remove([]byte(mine), openCodeTestEntry("/users/home")); !errors.Is(err, ErrEntryChanged) {
		t.Errorf("remove = %v, want ErrEntryChanged", err)
	}
}

// An entry with no gateway names no routing: install writes the MCP server and hooks only.
func TestOpencodeWithoutARouterWritesNoRouting(t *testing.T) {
	env, home := testEnv(t, nil)
	got, err := Install(context.Background(), t.TempDir(), env, "opencode", testEntry(home, "opencode"))
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if got.Routing != "" || strings.Contains(string(readConfig(t, got.Path)), `"provider"`) {
		t.Errorf("routing = %q, want none:\n%s", got.Routing, readConfig(t, got.Path))
	}
}
