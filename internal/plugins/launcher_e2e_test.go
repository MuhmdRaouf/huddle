package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/core/providers"
	"github.com/MuhmdRaouf/huddle/internal/core/testkit"
	"github.com/MuhmdRaouf/huddle/internal/core/testkit/fakeclaude"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// The provider plugin as the plugin runs it: the huddle binary loads and prints its help, the
// dist/run launcher starts the installed binary with every argument, setup runs against a
// throwaway install and reports ready, and the hooks never fail a session. Ported from the
// plugins' shared bundle-smoke suite and fresh-install's first check.

func TestLauncherSmoke(t *testing.T) {
	for _, name := range providers.Names {
		t.Run(name, func(t *testing.T) {
			provider, pluginDir := providerOf(t, name)
			launcher, err := filepath.Abs(filepath.Join(pluginDir, "dist", "run"))
			if err != nil {
				t.Fatalf("resolve the launcher: %v", err)
			}
			fakeClaude := fakeclaude.Build(t)

			t.Run("loads and prints help", func(t *testing.T) {
				checkBinaryLoads(t, provider, fakeClaude)
			})
			t.Run("the launcher dist/run starts the installed huddle binary with every argument", func(t *testing.T) {
				checkLauncherRunsBinary(t, provider, pluginDir, launcher)
			})
			t.Run("ships what an install needs: dist/ holds the executable launcher and nothing else", func(t *testing.T) {
				checkDistHoldsTheLauncher(t, provider, pluginDir)
			})
			t.Run("setup runs the binary against a throwaway install and reports ready", func(t *testing.T) {
				checkSetupReady(t, provider, pluginDir, fakeClaude)
			})
			t.Run("a hook never fails the session: setup --hook exits 0 silently even when its state dir cannot be made", func(t *testing.T) {
				checkHookNeverFails(t, provider, launcher)
			})
			t.Run("board --hook is silent with nothing to report and exits 0", func(t *testing.T) {
				checkBoardHookSilent(t, provider, launcher)
			})
		})
	}
}

// runOne captures one child run's streams separately and returns them with its exit code.
func runOne(t *testing.T, name string, args []string, env []string, dir string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = env
	cmd.Dir = dir
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); ok {
		return out.String(), errb.String(), exit.ExitCode()
	}
	if err != nil {
		t.Fatalf("run %s %s: %v", name, strings.Join(args, " "), err)
	}
	return out.String(), errb.String(), 0
}

// coreStarted is the proof that a run exec'd the built huddle binary and nothing else stood in:
// one "plugin-core <pid>" line in the file its env named (the PLUGIN_CORE_* pins are test env
// production never sets).
func coreStarted(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the started file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "plugin-core ") {
		t.Fatalf("started file = %q, want one plugin-core line", string(raw))
	}
}

// smokeEnv is one launcher run's environment: the temp home's binary home, plus extra entries.
func smokeEnv(t *testing.T, provider domain.Provider, home string, extra map[string]string) []string {
	t.Helper()
	env := childEnv(t, provider, home)
	env = append(env, "AGENTS_HUDDLE_HOME="+filepath.Join(home, "huddle-bin"))
	for name, value := range extra {
		env = append(env, name+"="+value)
	}
	return env
}

// checkBinaryLoads is the binary alone: it loads, prints its help, and says who it is.
func checkBinaryLoads(t *testing.T, provider domain.Provider, fakeClaude string) {
	t.Helper()
	home := testkit.TempDir(t, provider.Name+"-smoke-")
	started := filepath.Join(home, "started.log")
	env := smokeEnv(t, provider, home, map[string]string{
		"PLUGIN_CORE_STARTED_FILE":         started,
		provider.EnvPrefix + "_CLAUDE_BIN": fakeClaude,
	})
	stdout, stderr, code := runOne(t, huddleBin, []string{"provider", provider.Name, "cli", "--help"}, env, home)
	if code != 0 {
		t.Fatalf("the help exited %d: %s", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("the help wrote stderr: %q", stderr)
	}
	thereIsNo := "there is no " + provider.Name + " command on PATH"
	if !strings.Contains(stdout, thereIsNo) {
		t.Errorf("the help never says %q:\n%s", thereIsNo, stdout)
	}
	if !strings.Contains(stdout, "\n  run <brief.md|->") {
		t.Errorf("the help never shows the run synopsis:\n%s", stdout)
	}
	coreStarted(t, started)
}

// checkLauncherRunsBinary is the launcher with an installed binary: same help, binary exec'd.
func checkLauncherRunsBinary(t *testing.T, provider domain.Provider, pluginDir, launcher string) {
	t.Helper()
	home := testkit.TempDir(t, provider.Name+"-launcher-")
	installBinary(t, pluginDir, filepath.Join(home, "huddle-bin"))
	started := filepath.Join(home, "started.log")
	env := smokeEnv(t, provider, home, map[string]string{"PLUGIN_CORE_STARTED_FILE": started})

	stdout, stderr, code := runOne(t, "sh", []string{launcher, provider.Name + ".js", "--help"}, env, home)
	if code != 0 {
		t.Fatalf("the launcher exited %d: %s", code, stderr)
	}
	thereIsNo := "there is no " + provider.Name + " command on PATH"
	if !strings.Contains(stdout, thereIsNo) {
		t.Errorf("the launcher's help never says %q:\n%s", thereIsNo, stdout)
	}
	if !strings.Contains(stdout, "\n  run <brief.md|->") {
		t.Errorf("the launcher's help never shows the run synopsis:\n%s", stdout)
	}
	coreStarted(t, started)
}

// checkDistHoldsTheLauncher is fresh-install's first check: dist/ holds the executable launcher
// and nothing else, and every hook command runs it.
func checkDistHoldsTheLauncher(t *testing.T, provider domain.Provider, pluginDir string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(pluginDir, "dist"))
	if err != nil {
		t.Fatalf("read dist/: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "run" {
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("dist/ holds %v, want [run]", names)
	}
	info, err := entries[0].Info()
	if err != nil {
		t.Fatalf("stat dist/run: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("dist/run is not executable: %v", info.Mode())
	}
	hookCommand := regexp.MustCompile(`^sh "\$\{CLAUDE_PLUGIN_ROOT\}/dist/run" [a-z-]+\.js `)
	var hooks hooksDocument
	decodeStrictJSON(t, readFile(t, filepath.Join(pluginDir, "hooks", "hooks.json")), &hooks)
	for event, groups := range hooks.Hooks {
		for _, command := range flatten(groups) {
			if !hookCommand.MatchString(command.Command) {
				t.Errorf("%s: hook command %q does not run the launcher", event, command.Command)
			}
		}
	}
}

// throwawayInstall is a copy of the plugin the way a setup run sees it: the bundles and the agents
// beside them, the installed binary the launcher execs, a fake router already running, and the pid
// file that says so.
type throwawayInstall struct {
	home string
	port string
	bin  string
	env  []string
}

// fakeRouter answers like the running router — its health at the running code's own version, and
// its key check — so setup's start sees one running and starts nothing real.
func fakeRouter(t *testing.T, provider domain.Provider, healthPath, version string) *httptest.Server {
	t.Helper()
	health := map[string]any{
		"ok":           true,
		"name":         provider.Name,
		"provider":     provider.Display,
		"mode":         "router",
		"version":      version,
		"frontVersion": 3,
	}
	key := map[string]any{"ok": true, "source": provider.KeyEnv[0]}
	healthJSON, _ := json.Marshal(health)
	keyJSON, _ := json.Marshal(key)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case healthPath:
			_, _ = w.Write(healthJSON)
		case healthPath + "/key":
			_, _ = w.Write(keyJSON)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// newThrowawayInstall lays one provider's plugin out as a setup run's throwaway install.
func newThrowawayInstall(t *testing.T, provider domain.Provider, pluginDir, fakeClaude string) throwawayInstall {
	t.Helper()
	home := testkit.TempDir(t, provider.Name+"-bundle-")
	if err := os.CopyFS(filepath.Join(home, "dist"), os.DirFS(filepath.Join(pluginDir, "dist"))); err != nil {
		t.Fatalf("copy dist/: %v", err)
	}
	if err := os.CopyFS(filepath.Join(home, "agents"), os.DirFS(filepath.Join(pluginDir, "agents"))); err != nil {
		t.Fatalf("copy agents/: %v", err)
	}
	bin := installBinary(t, pluginDir, filepath.Join(home, "huddle-bin"))
	sum := sha256.Sum256(readFileBytes(t, bin))
	server := fakeRouter(t, provider, provider.Router.HealthPath, hex.EncodeToString(sum[:])[:12])
	port := server.Listener.Addr().(*net.TCPAddr).Port
	state := filepath.Join(home, "state")
	if err := os.Mkdir(state, 0o755); err != nil {
		t.Fatalf("make the state dir: %v", err)
	}
	pid, _ := json.Marshal(map[string]any{
		"pid":          os.Getpid(),
		"port":         port,
		"version":      "test",
		"frontVersion": 3,
		"mode":         "router",
		"token":        "t",
	})
	if err := os.WriteFile(filepath.Join(state, "router.pid"), pid, 0o600); err != nil {
		t.Fatalf("write the pid file: %v", err)
	}
	return throwawayInstall{
		home: home,
		port: strconv.Itoa(port),
		bin:  bin,
		env: smokeEnv(t, provider, home, map[string]string{
			"CLAUDE_PLUGIN_ROOT":                home,
			provider.EnvPrefix + "_CLAUDE_BIN":  fakeClaude,
			provider.EnvPrefix + "_API_KEY":     provider.Name + "-bundle-fake-key",
			provider.EnvPrefix + "_ROUTER_PORT": strconv.Itoa(port),
		}),
	}
}

// setupReport is the `setup --json` document, the members the ready check pins.
type setupReport struct {
	Ready   bool   `json:"ready"`
	Runtime string `json:"runtime"`
	Worker  struct {
		OK  bool   `json:"ok"`
		Bin string `json:"bin"`
	} `json:"claude"`
	Key struct {
		OK    bool   `json:"ok"`
		Value string `json:"value"`
	} `json:"key"`
	State  string `json:"state"`
	Router struct {
		OK    bool   `json:"ok"`
		Value string `json:"value"`
	} `json:"router"`
	Models struct {
		OK    bool   `json:"ok"`
		Value string `json:"value"`
	} `json:"models"`
	Settings struct {
		OK      bool `json:"ok"`
		Changed bool `json:"changed"`
	} `json:"settings"`
}

// checkSetupReady is setup against the throwaway install: ready, on the installed binary.
func checkSetupReady(t *testing.T, provider domain.Provider, pluginDir, fakeClaude string) {
	t.Helper()
	install := newThrowawayInstall(t, provider, pluginDir, fakeClaude)
	stdout, stderr, code := runOne(t, "sh", []string{
		filepath.Join(install.home, "dist", "run"), provider.Name + ".js", "setup", "--json",
	}, install.env, install.home)

	if stderr != "" {
		t.Errorf("setup wrote stderr: %q", stderr)
	}
	if code != 0 {
		t.Fatalf("setup exited %d: %s", code, stdout)
	}
	var report setupReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("setup --json printed %q: %v", stdout, err)
	}
	// plugin-core runs on no runtime; its setup line names the binary, its version and its path —
	// the installed binary the launcher exec'd.
	if want := "plugin-core dev (" + install.bin + ")"; report.Runtime != want {
		t.Errorf("runtime = %q, want %q", report.Runtime, want)
	}
	if !report.Worker.OK || report.Worker.Bin != fakeClaude {
		t.Errorf("claude = (ok %t, bin %q), want ok on %s", report.Worker.OK, report.Worker.Bin, fakeClaude)
	}
	if !report.Key.OK || report.Key.Value != provider.KeyEnv[0] {
		t.Errorf("key = (ok %t, value %q), want ok on %s", report.Key.OK, report.Key.Value, provider.KeyEnv[0])
	}
	if !strings.HasSuffix(report.State, "/state") || !strings.HasPrefix(report.State, install.home) {
		t.Errorf("state = %q, want the throwaway install's state dir", report.State)
	}
	if !report.Router.OK || !strings.Contains(report.Router.Value, "already running") {
		t.Errorf("router = (ok %t, value %q), want the already-running fake", report.Router.OK, report.Router.Value)
	}
	wantModels := provider.Catalog[domain.ModelTierMain].ID + ", " + provider.Catalog[domain.ModelTierFlash].ID
	if !report.Models.OK || report.Models.Value != wantModels {
		t.Errorf("models = (ok %t, value %q), want %q", report.Models.OK, report.Models.Value, wantModels)
	}
	if !report.Settings.OK || !report.Settings.Changed {
		t.Errorf("settings = (ok %t, changed %t), want a changed settings.json", report.Settings.OK, report.Settings.Changed)
	}
	// The copy's agents were rewritten; the shipped ones still say sonnet.
	agent := filepath.Join("agents", provider.Agents[domain.ModelTierMain]+".md")
	rewritten := readFile(t, filepath.Join(install.home, agent))
	if !strings.Contains(rewritten, "model: "+provider.Catalog[domain.ModelTierMain].ID) {
		t.Errorf("the installed %s was not rewritten to %s", agent, provider.Catalog[domain.ModelTierMain].ID)
	}
	shipped := readFile(t, filepath.Join(pluginDir, agent))
	if !regexp.MustCompile(`(?m)^model: sonnet$`).MatchString(shipped) {
		t.Errorf("the shipped %s no longer ships model: sonnet", agent)
	}
	// A ready setup had a key from its env the whole time: it never reached for the browser.
	if opened := openedCalls(t, install.home); len(opened) != 0 {
		t.Errorf("a ready setup opened a browser: %v", opened)
	}
}

// checkHookNeverFails is setup --hook over a state dir that cannot be made: silence and exit 0.
func checkHookNeverFails(t *testing.T, provider domain.Provider, launcher string) {
	t.Helper()
	home := testkit.TempDir(t, provider.Name+"-hook-")
	installBinary(t, pluginDirOf(provider.Name), filepath.Join(home, "huddle-bin"))
	broken := filepath.Join(home, "not-a-dir")
	if err := os.WriteFile(broken, []byte("a file where the state dir would go"), 0o644); err != nil {
		t.Fatal(err)
	}
	started := filepath.Join(home, "started.log")
	env := smokeEnv(t, provider, home, map[string]string{
		"PLUGIN_CORE_STARTED_FILE":        started,
		provider.EnvPrefix + "_STATE_DIR": filepath.Join(broken, "state"),
	})
	stdout, stderr, code := runOne(t, "sh", []string{launcher, provider.Name + ".js", "setup", "--hook"}, env, home)
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("setup --hook = (code %d, stdout %q, stderr %q), want silent exit 0", code, stdout, stderr)
	}
	coreStarted(t, started)
}

// checkBoardHookSilent is board --hook with nothing to report: silence and exit 0.
func checkBoardHookSilent(t *testing.T, provider domain.Provider, launcher string) {
	t.Helper()
	home := testkit.TempDir(t, provider.Name+"-board-")
	installBinary(t, pluginDirOf(provider.Name), filepath.Join(home, "huddle-bin"))
	env := smokeEnv(t, provider, home, nil)
	stdout, stderr, code := runOne(t, "sh", []string{launcher, provider.Name + ".js", "board", "--hook"}, env, home)
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("board --hook = (code %d, stdout %q, stderr %q), want silent exit 0", code, stdout, stderr)
	}
}

// readFileBytes is one file's bytes.
func readFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}
