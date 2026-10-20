package plugins

// A fresh install works with /<p>:setup as the only step, end to end on the committed launcher:
// on a machine with nothing set up, setup opens the one-time key page, a key saved on the page is
// picked up by the still-running setup, Claude's requests for the provider's models route through
// huddle-router to the provider while `claude-*` requests reach Anthropic untouched, a SIGKILLed
// daemon comes back at the next session start (the hook's own start: RoutingHook brings the daemon
// up through routerd.Start), and `setup --remove` restores settings.json byte for byte. The
// launcher runs from a copy outside the repository with the built binary installed under
// AGENTS_HUDDLE_HOME, so no source build runs; the key never comes from the environment. Every
// process here is the scenario's own and dies with it.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/keystore"
	"github.com/MuhmdRaouf/huddle/internal/core/providers"
	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/core/testkit"
	"github.com/MuhmdRaouf/huddle/internal/core/testkit/fakeclaude"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// freshSeen is one request a fake upstream saw: the auth header and the body, the two the routing
// proof reads.
type freshSeen struct {
	auth string
	body string
}

// freshFake is one recording fake upstream: it answers every POST with a two-event stream named
// after itself, so a key check reads accepted and a routed model request reads served.
type freshFake struct {
	server *httptest.Server
	mu     sync.Mutex
	seen   []freshSeen
}

func newFreshFake(t *testing.T, name string) *freshFake {
	t.Helper()
	fake := &freshFake{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fake.mu.Lock()
		fake.seen = append(fake.seen, freshSeen{auth: r.Header.Get("Authorization"), body: string(body)})
		fake.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+name+"-1\n\n")
		_, _ = io.WriteString(w, "data: "+name+"-2\n\n")
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *freshFake) requests() []freshSeen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]freshSeen(nil), f.seen...)
}

func (f *freshFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.seen)
}

// freshHTTP talks to the key page, the router and its fakes over loopback directly: the client
// carries no proxy of its own, whatever the machine running the tests has exported.
var freshHTTP = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}}

// freshInstall is one provider's throwaway machine: a temp home with the plugin copied out of the
// repository, the built binary installed where the launcher looks it up, a fake upstream per lane
// and one for Anthropic, and the settings.json the removal must restore.
type freshInstall struct {
	t             *testing.T
	name          string
	provider      domain.Provider
	home          string
	copyRoot      string
	port          int
	upstream      *freshFake
	anthropic     *freshFake
	env           []string
	settingsPath  string
	settingsBytes []byte
	keyFile       string
	key           string

	cmd    *exec.Cmd
	ended  chan struct{}
	stdout string
	stderr string
	err    error
}

// newFreshInstall lays one provider's fresh machine out. The caller passes the scenario's own
// testing.T, so every cleanup lives exactly as long as the scenario.
func newFreshInstall(t *testing.T, name string) *freshInstall {
	t.Helper()
	provider, pluginDir := providerOf(t, name)
	home := testkit.TempDir(t, name+"-fresh-")
	copyRoot := filepath.Join(home, name+"-plugin")
	if err := os.CopyFS(copyRoot, os.DirFS(pluginDir)); err != nil {
		t.Fatalf("copy the plugin out of the repository: %v", err)
	}
	installBinary(t, pluginDir, filepath.Join(home, "huddle-bin"))
	fake := fakeclaude.Build(t)
	upstream := newFreshFake(t, provider.Name)
	anthropic := newFreshFake(t, "anthropic")
	routerHome := filepath.Join(home, "router-home")
	claudeConfig := filepath.Join(home, "claude-config")

	// Claude Code's own bookkeeping: the plugin installed from the copy, the path its dist bundle
	// and agents sit under (the hook's provider wiring reads it).
	installed := map[string]any{
		"plugins": map[string]any{
			name + "-plugin-cc@muhmdraouf": []map[string]any{
				{"installPath": copyRoot, "scope": "user"},
			},
		},
	}
	raw, err := json.Marshal(installed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(claudeConfig, "plugins"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeConfig, "plugins", "installed_plugins.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	rig := &freshInstall{
		t:             t,
		name:          name,
		provider:      provider,
		home:          home,
		copyRoot:      copyRoot,
		port:          ports.Free(t),
		upstream:      upstream,
		anthropic:     anthropic,
		settingsPath:  filepath.Join(claudeConfig, "settings.json"),
		settingsBytes: []byte("{\n  \"unrelated\": \"keep\"\n}\n"),
		// A key shape the page accepts (16-512 letters, digits, '.', '_', '-').
		key:     "fresh-install-e2e-" + name + "-key-0123456789",
		keyFile: keystore.KeyFilePath(provider, domain.EnvLookup{"HOME": home}, provider.KeyFile),
	}
	if err := os.WriteFile(rig.settingsPath, rig.settingsBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	rig.env = smokeEnv(t, provider, home, map[string]string{
		"CLAUDE_PLUGIN_ROOT":               copyRoot,
		"CLAUDE_PLUGIN_DATA":               filepath.Join(home, name+"-plugin-data"),
		"HUDDLE_ROUTER_HOME":               routerHome,
		"HUDDLE_ROUTER_PORT":               strconv.Itoa(rig.port),
		"HUDDLE_ROUTER_ANTHROPIC_URL":      anthropic.server.URL,
		provider.EnvPrefix + "_ROUTER_URL": upstream.server.URL,
		// The key page's own check resolves the provider's endpoint through the base URL, the
		// lane's through the router URL: both land on the same fake upstream.
		provider.EnvPrefix + "_BASE_URL":   upstream.server.URL,
		provider.EnvPrefix + "_CLAUDE_BIN": fake,
		"HUDDLE_HOME":                      filepath.Join(home, "huddle-home"),
		"RADAR_HOME":                       filepath.Join(home, "radar"),
		"HUDDLE_AUTOSTART":                 "0",
		"RADAR_AUTOSTART":                  "0",
		"HUDDLE_NO_PROJECT_CRED":           "1",
	})
	t.Cleanup(rig.stopEverything)
	return rig
}

// stopEverything ends what outlives the scenario's own children: a setup still waiting on its
// key page and the daemon the setup or the hook started.
func (rig *freshInstall) stopEverything() {
	if rig.cmd != nil && rig.cmd.Process != nil {
		select {
		case <-rig.ended:
		default:
			_ = rig.cmd.Process.Kill()
			<-rig.ended
		}
	}
	raw, err := os.ReadFile(filepath.Join(rig.home, "router-home", "run", "router.json"))
	if err != nil {
		return
	}
	var record struct {
		PID int `json:"pid"`
	}
	if json.Unmarshal(raw, &record) != nil || record.PID <= 1 {
		return
	}
	_ = syscall.Kill(record.PID, syscall.SIGTERM)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(record.PID, 0) != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(record.PID, syscall.SIGKILL)
}

// startSetup runs the launcher the way /<p>:setup runs it, without waiting: setup blocks on the
// key page until the scenario fills it in.
func (rig *freshInstall) startSetup(t *testing.T) {
	t.Helper()
	cmd := exec.Command("sh", filepath.Join(rig.copyRoot, "dist", "run"), rig.name+".js", "setup")
	cmd.Env = rig.env
	cmd.Dir = rig.home
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Start(); err != nil {
		t.Fatalf("start setup: %v", err)
	}
	rig.cmd = cmd
	rig.ended = make(chan struct{})
	go func() {
		rig.err = cmd.Wait()
		rig.stdout, rig.stderr = out.String(), errOut.String()
		close(rig.ended)
	}()
}

// awaitPageURL waits for the fake open to record the key page's URL.
func (rig *freshInstall) awaitPageURL(t *testing.T) string {
	t.Helper()
	shape := regexp.MustCompile(`^http://127\.0\.0\.1:\d+/[A-Za-z0-9_-]+$`)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range openedCalls(t, rig.home) {
			if shape.MatchString(line) {
				return line
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the fake open never recorded the key page URL within 20 s")
	return ""
}

// enterKey loads the one-time page the way a browser does and submits the fake key: the csrf
// value from the form and the page's own Origin.
func (rig *freshInstall) enterKey(t *testing.T, pageURL string) {
	t.Helper()
	response, err := freshHTTP.Get(pageURL)
	if err != nil {
		t.Fatalf("load the key page: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatalf("read the key page: %v", err)
	}
	if response.StatusCode != 200 {
		t.Fatalf("the key page answered %d", response.StatusCode)
	}
	csrf := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindSubmatch(body)
	if csrf == nil {
		t.Fatalf("the key page carries no csrf field:\n%s", body)
	}
	parsed, err := url.Parse(pageURL)
	if err != nil {
		t.Fatalf("parse the page URL: %v", err)
	}
	form := url.Values{"csrf": {string(csrf[1])}, "key": {rig.key}}
	request, err := http.NewRequest(http.MethodPost, pageURL, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("build the submission: %v", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "http://"+parsed.Host)
	answer, err := freshHTTP.Do(request)
	if err != nil {
		t.Fatalf("submit the key: %v", err)
	}
	answer.Body.Close()
	if answer.StatusCode != 200 {
		t.Fatalf("the key page answered the submission %d", answer.StatusCode)
	}
}

// awaitSetup waits for setup to finish and reads its verdict: exit 0 and a ready report.
func (rig *freshInstall) awaitSetup(t *testing.T) {
	t.Helper()
	select {
	case <-rig.ended:
	case <-time.After(2 * time.Minute):
		t.Fatal("setup never finished")
	}
	if rig.err != nil {
		t.Fatalf("setup failed: %v\nstdout:\n%s\nstderr:\n%s", rig.err, rig.stdout, rig.stderr)
	}
	if !strings.Contains(rig.stdout, rig.name+" setup: ready.") {
		t.Fatalf("setup never said ready:\n%s\nstderr:\n%s", rig.stdout, rig.stderr)
	}
}

// enabledProviders reads providers.json the way the daemon does.
func (rig *freshInstall) enabledProviders(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(rig.home, "router-home", "providers.json"))
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}
	var parsed struct {
		Providers []string `json:"providers"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse providers.json: %v", err)
	}
	return parsed.Providers
}

// envValue reads one env entry out of settings.json.
func (rig *freshInstall) envValue(t *testing.T, key string) string {
	t.Helper()
	raw, err := os.ReadFile(rig.settingsPath)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var parsed struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse settings.json: %v", err)
	}
	return parsed.Env[key]
}

// keyStored reads the key file the page's store wrote.
func (rig *freshInstall) keyStored(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(rig.keyFile)
	if err != nil {
		t.Fatalf("read the key file %s: %v", rig.keyFile, err)
	}
	return string(raw)
}

// daemonPID reads the pid the running daemon published.
func (rig *freshInstall) daemonPID(t *testing.T) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(rig.home, "router-home", "run", "router.json"))
	if err != nil {
		t.Fatalf("read the daemon record: %v", err)
	}
	var record struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(raw, &record); err != nil || record.PID <= 0 {
		t.Fatalf("the daemon record %q names no pid", raw)
	}
	return record.PID
}

// healthy says whether the router answers its health path.
func (rig *freshInstall) healthy() bool {
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d%s", rig.port, router.MultiHealthPath))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

// freshPoll waits within for the answer to turn true.
func freshPoll(within time.Duration, f func() bool) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if f() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return f()
}

// postModel sends one model request the way Claude Code does — the caller's own credentials on
// it — and answers the status and body.
func (rig *freshInstall) postModel(t *testing.T, model string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/v1/messages", rig.port),
		strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[]}`, model)))
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer anthropic-oauth")
	request.Header.Set("X-Api-Key", "anthropic-key")
	resp, err := freshHTTP.Do(request)
	if err != nil {
		t.Fatalf("post %s: %v", model, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the answer: %v", err)
	}
	return resp.StatusCode, string(body)
}

// runHook runs the merged session-start hook the way Claude Code starts it: one huddle process,
// the event's payload on stdin.
func (rig *freshInstall) runHook(t *testing.T, payload string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(huddleBin, "hook", "session-start")
	cmd.Env = rig.env
	cmd.Dir = rig.home
	cmd.Stdin = strings.NewReader(payload)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), out.String(), errOut.String()
	}
	if err != nil {
		t.Fatalf("run huddle hook session-start: %v", err)
	}
	return 0, out.String(), errOut.String()
}

// runRemove runs `setup --remove` through the launcher, the way /<p>:setup --remove runs it.
func (rig *freshInstall) runRemove(t *testing.T) (string, string, int) {
	t.Helper()
	return runOne(t, "sh", []string{
		filepath.Join(rig.copyRoot, "dist", "run"), rig.name + ".js", "setup", "--remove",
	}, rig.env, rig.home)
}

func TestFreshInstall(t *testing.T) {
	bdd.Feature(t, "a fresh install works with /<p>:setup as the only step", func(f *bdd.F) {
		for _, name := range providers.Names {
			f.Scenario(name+" setup is the only step on an empty machine", func(s *bdd.S) {
				s.T.Parallel()
				var rig *freshInstall
				s.Given("an empty machine: an unrelated settings.json and the plugin installed from a copy", func(t *testing.T) {
					rig = newFreshInstall(s.T, name)
				})
				s.When("setup runs and the key page is filled in", func(t *testing.T) {
					rig.startSetup(t)
					rig.enterKey(t, rig.awaitPageURL(t))
				})
				s.Then("setup exits 0 and reports ready", func(t *testing.T) {
					rig.awaitSetup(t)
				})
				s.And("the provider is recorded, the proxy points at the router and the key is stored", func(t *testing.T) {
					if got := rig.envValue(t, "HTTPS_PROXY"); got != fmt.Sprintf("http://127.0.0.1:%d", rig.port) {
						t.Errorf("HTTPS_PROXY = %q, want the router's http://127.0.0.1:%d", got, rig.port)
					}
					found := false
					for _, enabled := range rig.enabledProviders(t) {
						if enabled == rig.name {
							found = true
						}
					}
					if !found {
						t.Errorf("providers.json = %v, want it to list %s", rig.enabledProviders(t), rig.name)
					}
					if stored := rig.keyStored(t); !strings.Contains(stored, rig.provider.KeyEnv[0]+"="+rig.key) {
						t.Errorf("the key file = %q, want it to hold the saved key", stored)
					}
				})
				s.And("a request for the provider's model reaches the fake upstream with the saved key", func(t *testing.T) {
					before := rig.upstream.count()
					status, body := rig.postModel(t, rig.provider.Catalog[domain.ModelTierMain].ID)
					if status != 200 {
						t.Fatalf("%s = %d %q", rig.provider.Catalog[domain.ModelTierMain].ID, status, body)
					}
					seen := rig.upstream.requests()[before:]
					if len(seen) == 0 {
						t.Fatal("the fake upstream saw no request")
					}
					for _, one := range seen {
						if one.auth != "Bearer "+rig.key {
							t.Errorf("authorization = %q, want the saved key", one.auth)
						}
					}
				})
				s.And("a claude request reaches the fake Anthropic untouched", func(t *testing.T) {
					anthropicBefore := rig.anthropic.count()
					upstreamBefore := rig.upstream.count()
					status, body := rig.postModel(t, "claude-opus-5-5")
					if status != 200 {
						t.Fatalf("claude-opus-5-5 = %d %q", status, body)
					}
					if got := rig.anthropic.count() - anthropicBefore; got != 1 {
						t.Errorf("the fake Anthropic saw %d requests, want 1", got)
					}
					if got := rig.upstream.count() - upstreamBefore; got != 0 {
						t.Errorf("the provider upstream saw the claude request")
					}
				})
				s.And("a killed router comes back at the next session start", func(t *testing.T) {
					pid := rig.daemonPID(t)
					if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
						t.Fatalf("kill the daemon: %v", err)
					}
					if !freshPoll(5*time.Second, func() bool { return !rig.healthy() }) {
						t.Fatal("the killed daemon still answers health")
					}
					code, stdout, stderr := rig.runHook(t, `{"source":"startup","session_id":"fresh-install-e2e"}`)
					if code != 0 {
						t.Fatalf("huddle hook session-start = %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
					}
					if !freshPoll(10*time.Second, rig.healthy) {
						t.Fatal("the router never answered health again within 10 s of the session start")
					}
					if again := rig.daemonPID(t); again == pid {
						t.Errorf("the router record still names the killed pid %d", pid)
					}
				})
				s.Then("setup --remove restores settings.json byte for byte and unrecords the provider", func(t *testing.T) {
					stdout, stderr, code := rig.runRemove(t)
					if code != 0 {
						t.Fatalf("setup --remove exited %d:\n%s\n%s", code, stdout, stderr)
					}
					raw, err := os.ReadFile(rig.settingsPath)
					if err != nil {
						t.Fatalf("read settings.json: %v", err)
					}
					if !bytes.Equal(raw, rig.settingsBytes) {
						t.Errorf("settings.json was not restored byte for byte:\n%s", raw)
					}
					for _, enabled := range rig.enabledProviders(t) {
						if enabled == rig.name {
							t.Errorf("providers.json still lists %s", rig.name)
						}
					}
				})
			})
		}
	})
}
