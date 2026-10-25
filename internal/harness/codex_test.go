package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// The Codex adapter's edges: the config path CODEX_HOME moves, detection falls to the
// config file when the binary is away, a block that drifted from ours is replaced in
// place, a remove without a block of ours refuses, and a block with no end marker runs to
// the end of the file.

func TestCodexConfigPathFollowsCODEXHOME(t *testing.T) {
	given := Env{Lookup: func(key string) string {
		if key == "CODEX_HOME" {
			return "/users/codex-home"
		}
		return "/users/home"
	}}
	if got := codex.ConfigPath(given); got != filepath.Join("/users/codex-home", "config.toml") {
		t.Errorf("ConfigPath = %q, want it under CODEX_HOME", got)
	}
	plain := Env{Lookup: func(key string) string {
		if key == "HOME" {
			return "/users/home"
		}
		return ""
	}}
	if got := codex.ConfigPath(plain); got != filepath.Join("/users/home", ".codex", "config.toml") {
		t.Errorf("ConfigPath = %q, want it under the home's .codex", got)
	}
}

func TestCodexDetectFallsToTheConfigFile(t *testing.T) {
	home := t.TempDir()
	env := Env{
		Lookup: func(key string) string {
			if key == "HOME" {
				return home
			}
			return ""
		},
		Which: func(string) bool { return false },
	}
	if codex.Detect(env) {
		t.Error("codex detected with no binary and no config")
	}
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !codex.Detect(env) {
		t.Error("codex not detected through its config file")
	}
}

// A block that is no longer what install writes is replaced in place: one block left,
// ours, with the file's own bytes where they were.
func TestCodexInstallReplacesADeviatingBlock(t *testing.T) {
	entry := NewEntry("codex", "/users/home/bin/huddle")
	before := "model = \"gpt-5\"\n" + tomlBegin + "\n[mcp_servers.huddle]\ncommand = \"old\"\n" + tomlEnd + "\n"
	out, err := codex.Install([]byte(before), entry)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if got := strings.Count(string(out), tomlBegin); got != 1 {
		t.Fatalf("install left %d begin markers:\n%s", got, out)
	}
	var doc struct {
		Servers map[string]struct {
			Command string `toml:"command"`
		} `toml:"mcp_servers"`
	}
	if _, err := toml.Decode(string(out), &doc); err != nil {
		t.Fatalf("the config does not parse: %v\n%s", err, out)
	}
	if got := doc.Servers["huddle"].Command; got != entry.Command {
		t.Errorf("command = %q, want %q", got, entry.Command)
	}
	if !strings.HasPrefix(string(out), "model = \"gpt-5\"\n") {
		t.Errorf("install touched the file's own bytes:\n%s", out)
	}
	if !strings.Contains(string(out), tomlEnd) {
		t.Error("the end marker is gone")
	}
}

// A config with no block of ours refuses the remove, bytes untouched.
func TestCodexRemoveWithoutItsBlockRefuses(t *testing.T) {
	entry := NewEntry("codex", "/bin/huddle")
	cfg := "model = \"gpt-5\"\n"
	out, err := codex.Remove([]byte(cfg), entry)
	if !errors.Is(err, ErrEntryChanged) {
		t.Fatalf("remove = %v, want ErrEntryChanged", err)
	}
	if string(out) != cfg {
		t.Errorf("remove touched the bytes: %q", out)
	}
}

// A begin marker with no end ends the file as far as huddle reads: the block runs to the
// last line, and a remove cuts to there.
func TestCodexFindReadsABlockWithNoEndMarker(t *testing.T) {
	cfg := "model = \"gpt-5\"\n" + tomlBegin + "\n[mcp_servers.huddle]\ncommand = \"c\"\n"
	block, found := (tomlAdapter{}).find([]byte(cfg))
	if !found {
		t.Fatal("find saw no block")
	}
	if !strings.Contains(block.inner, "command = \"c\"") {
		t.Errorf("inner = %q, want the block's lines", block.inner)
	}
	if block.cutEnd != len(cfg) {
		t.Errorf("cutEnd = %d, want the end of the file (%d)", block.cutEnd, len(cfg))
	}
}

// The TOML quoting: every character TOML has no literal for rides as an escape, and what
// comes back out of a real parse is the string that went in.
func TestTomlStringEscapesWhatTOMLCannotHold(t *testing.T) {
	text := "a\"b\\c\bd\fe\nf\rg\th\x01i\x7fj plain"
	got := tomlString(text)
	var doc struct {
		Value string `toml:"value"`
	}
	if _, err := toml.Decode("value = "+got+"\n", &doc); err != nil {
		t.Fatalf("the quoting does not parse: %v\n%s", err, got)
	}
	if doc.Value != text {
		t.Errorf("round trip = %q, want %q", doc.Value, text)
	}
	if !strings.Contains(got, `\u0001`) || !strings.Contains(got, `\u007F`) {
		t.Errorf("%s does not name the control characters as \\u escapes", got)
	}
}

// sortedKeys sorts whatever order the map handed over, so the block never depends on it.
func TestSortedKeysSortsWhateverOrderTheMapGave(t *testing.T) {
	m := map[string]string{"c": "1", "a": "2", "b": "3"}
	for i := 0; i < 200; i++ {
		if got := strings.Join(sortedKeys(m), ""); got != "abc" {
			t.Fatalf("sortedKeys = %q, want abc in order", got)
		}
	}
}

// Setup writes codex's whole wiring in one block: the MCP server, and an opt-in model provider
// that sends the Responses API to the router's gateway over plain HTTP on loopback, naming
// itself on every request. The user's own provider and openai_base_url stay as they were.
func TestCodexSetupWritesItsWiring(t *testing.T) {
	env, _ := testEnv(t, map[string]bool{"codex": true})
	hudHome := t.TempDir()
	entry := NewEntry("codex", "/users/home/bin/huddle")
	entry.Gateway = "http://127.0.0.1:18786/gateway"
	got, err := Install(context.Background(), hudHome, env, "codex", entry)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	raw, err := os.ReadFile(got.Path)
	if err != nil {
		t.Fatal(err)
	}
	want := `# huddle:begin
[mcp_servers.huddle]
command = "/users/home/bin/huddle"
args = ["mcp"]

[mcp_servers.huddle.env]
HUDDLE_HARNESS = "codex"

[model_providers.huddle]
name = "huddle"
base_url = "http://127.0.0.1:18786/gateway/v1"
wire_api = "responses"
env_key = "OPENAI_API_KEY"
supports_websockets = false

[model_providers.huddle.http_headers]
X-Huddle-Harness = "codex"
# huddle:end
`
	hooks := string(tomlHookBlock(entry.Command, "codex", "\n"))
	if string(raw) != want+hooks {
		t.Errorf("config =\n%s\nwant\n%s", raw, want+hooks)
	}
	for _, never := range []string{"openai_base_url", "model_provider =", "[profiles", "CODEX_CA_CERTIFICATE"} {
		if strings.Contains(string(raw), never) {
			t.Errorf("config holds %q; setup never writes it", never)
		}
	}
	var doc struct {
		Providers map[string]struct {
			BaseURL string `toml:"base_url"`
		} `toml:"model_providers"`
	}
	if _, err := toml.Decode(string(raw), &doc); err != nil {
		t.Fatalf("the config does not parse: %v", err)
	}
	removed, err := Remove(context.Background(), hudHome, env, "codex", entry)
	if err != nil || !removed.FileGone {
		t.Errorf("remove = %+v, %v; want the created file gone", removed, err)
	}
}
