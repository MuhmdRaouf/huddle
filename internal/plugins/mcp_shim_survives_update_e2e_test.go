package plugins

// The MCP server outlives the plugin directory it was loaded from. Claude Code expands
// ${CLAUDE_PLUGIN_ROOT} in .mcp.json once for a session's life, and an update replaces that cache
// directory: a server line that runs a file under it fails from then on. The .mcp.json line runs
// the plugin's bin/huddle-mcp while the directory is there, so the launcher builds a new plugin
// commit and repoints the stable $HOME/.agents/huddle-bin/current, and runs current itself once the
// directory is gone. The plugin is installed into a temp CLAUDE_CONFIG_DIR's cache and the server
// line is started three times, each answering JSON-RPC initialize on stdio: the first ever start
// builds the checkout's commit, a start with a stale current repoints it, and a start after the
// cache directory is deleted runs current.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/core/testkit"
	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
)

// mcpServerLine is the huddle server .mcp.json gives Claude Code, with ${CLAUDE_PLUGIN_ROOT}
// expanded to root the way Claude Code expands it.
func mcpServerLine(t *testing.T, root string) (command string, args []string) {
	t.Helper()
	var file struct {
		McpServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(repoRoot, "plugin", ".mcp.json"))), &file); err != nil {
		t.Fatalf("parse .mcp.json: %v", err)
	}
	server, ok := file.McpServers["huddle"]
	if !ok {
		t.Fatal(".mcp.json names no huddle server")
	}
	for _, arg := range server.Args {
		args = append(args, strings.ReplaceAll(arg, "${CLAUDE_PLUGIN_ROOT}", root))
	}
	return server.Command, args
}

// initializeAnswer starts the server line, sends initialize and answers the server name the
// reply carries. stdin closes once the reply is read, which ends the bridge.
func initializeAnswer(t *testing.T, command string, args, env []string, dir string, within time.Duration) (string, error) {
	t.Helper()
	cmd := exec.Command(command, args...)
	cmd.Env = env
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("pipe the server: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("pipe the server: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the server: %v", err)
	}
	defer func() {
		_ = stdin.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}()
	if _, err := io.WriteString(stdin, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"claude-code","version":"0"}}}`+"\n"); err != nil {
		return "", fmt.Errorf("write initialize: %w", err)
	}
	type answer struct {
		name string
		err  error
	}
	answers := make(chan answer, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			var reply struct {
				ID     *int `json:"id"`
				Result struct {
					ServerInfo struct {
						Name string `json:"name"`
					} `json:"serverInfo"`
				} `json:"result"`
			}
			if json.Unmarshal(scanner.Bytes(), &reply) == nil && reply.ID != nil && *reply.ID == 1 {
				answers <- answer{name: reply.Result.ServerInfo.Name}
				return
			}
		}
		answers <- answer{err: fmt.Errorf("the server closed stdout before it answered initialize")}
	}()
	select {
	case got := <-answers:
		if got.err != nil {
			return "", fmt.Errorf("%w (stderr %q)", got.err, stderr.String())
		}
		return got.name, nil
	case <-time.After(within):
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("no initialize answer within %s (stderr %q)", within, stderr.String())
	}
}

func TestTheMCPShimSurvivesAPluginUpdate(t *testing.T) {
	home := testkit.TempDir(t, "huddle-mcp-update-")
	claudeConfig := filepath.Join(home, "claude-config")
	pluginCopy := filepath.Join(claudeConfig, "plugins", "cache", "muhmdraouf", "huddle", "first")
	if err := os.CopyFS(pluginCopy, os.DirFS(filepath.Join(repoRoot, "plugin"))); err != nil {
		t.Fatalf("install the plugin into the temp cache: %v", err)
	}
	source, err := filepath.Abs(repoRoot)
	if err != nil {
		t.Fatalf("resolve the repository root: %v", err)
	}
	binHome := filepath.Join(home, ".agents", "huddle-bin")
	current := filepath.Join(binHome, "current")
	_, hubURL := startUpdateHub(t)
	huddleHome := filepath.Join(home, "huddle-home")
	if err := os.MkdirAll(huddleHome, 0o700); err != nil {
		t.Fatal(err)
	}
	// HOME is the temp home, so the line's default $HOME/.agents/huddle-bin is this test's own;
	// the router watchdog stays off, so no bridge probes the live router's port
	env := isoenv.Environ(t, home,
		"CLAUDE_CONFIG_DIR="+claudeConfig,
		"CLAUDE_PLUGIN_ROOT="+pluginCopy,
		"HUDDLE_SOURCE="+source,
		"HUDDLE_DOWNLOAD_BASE=http://127.0.0.1:1",
		"HUDDLE_HOME="+huddleHome,
		"HUDDLE_URL="+hubURL,
		"HUDDLE_TOKEN=mcp-update-token",
		"HUDDLE_NO_PROJECT_CRED=1",
		"HUDDLE_AUTOSTART=0",
		"HUDDLE_ROUTER_WATCH=0",
		"RADAR_HOME="+filepath.Join(home, "radar"),
		"RADAR_AUTOSTART=0",
	)
	command, args := mcpServerLine(t, pluginCopy)
	var commitBuild string

	t.Run("the first ever start, with no current yet, builds through the launcher and answers initialize", func(t *testing.T) {
		name, err := initializeAnswer(t, command, args, env, home, 5*time.Minute)
		if err != nil || name != "huddle" {
			t.Fatalf("the first start answered server %q, %v; want huddle", name, err)
		}
		commitBuild, err = filepath.EvalSymlinks(current)
		if err != nil {
			t.Fatalf("the first start left no current: %v", err)
		}
		if filepath.Base(filepath.Dir(filepath.Dir(commitBuild))) != "src" {
			t.Fatalf("current names %s, want the commit's build under src/", commitBuild)
		}
	})

	t.Run("a start with its plugin directory in place runs the launcher, which repoints a stale current", func(t *testing.T) {
		stale := filepath.Join(binHome, "stale", "huddle")
		if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(stale, []byte(readFile(t, commitBuild)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(current); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(stale, current); err != nil {
			t.Fatal(err)
		}
		name, err := initializeAnswer(t, command, args, env, home, time.Minute)
		if err != nil || name != "huddle" {
			t.Fatalf("the start answered server %q, %v; want huddle", name, err)
		}
		if got, _ := filepath.EvalSymlinks(current); got != commitBuild {
			t.Fatalf("current names %s after the start, want the commit's build %s", got, commitBuild)
		}
	})

	t.Run("the same server line after the update deletes its plugin directory runs current and answers initialize", func(t *testing.T) {
		if err := os.RemoveAll(pluginCopy); err != nil {
			t.Fatalf("delete the plugin copy: %v", err)
		}
		name, err := initializeAnswer(t, command, args, env, home, time.Minute)
		if err != nil || name != "huddle" {
			t.Fatalf("the start after the update answered server %q, %v; want huddle", name, err)
		}
	})
}
