package cli

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/server"
)

// cliWorld is the environment of one throwaway project whose huddle home is home: no url from
// the environment, so the port comes from the home. State (sessions, the servers registry) is
// shared with every world of the test, as one machine's is.
func cliWorld(t *testing.T, home, state string, env map[string]string) *World {
	t.Helper()
	vars := map[string]string{
		"HUDDLE_HOME":        home,
		"CLAUDE_PROJECT_DIR": home,
		"XDG_STATE_HOME":     state,
		"HUDDLE_CHANNEL":     "p",
		"HUDDLE_AS":          "a",
		"HUDDLE_TOKEN":       "test-root-credential",
		"HOME":               t.TempDir(),
	}
	for key, value := range env {
		if value == "" {
			delete(vars, key)
			continue
		}
		vars[key] = value
	}
	entries := make([]string, 0, len(vars))
	for key, value := range vars {
		entries = append(entries, key+"="+value)
	}
	w := &World{
		Getenv:  func(key string) string { return vars[key] },
		Environ: func() []string { return append([]string{}, entries...) },
		Dir:     home,
		Now:     func() time.Time { return time.Now() },
		Bin:     huddleBin,
	}
	t.Cleanup(func() { stopHome(t, w, home) })
	return w
}

// oneHome is a throwaway home with its own state.
func oneHome(t *testing.T) (string, *World) {
	t.Helper()
	home := t.TempDir()
	return home, cliWorld(t, home, filepath.Join(t.TempDir(), "state"), nil)
}

// savedCfg is the home's config.toml as the home package reads it.
func savedCfg(t *testing.T, root string) home.Config {
	t.Helper()
	cfg, err := home.LoadConfig(home.Home{Root: root})
	if err != nil {
		t.Fatalf("load the home's config: %v", err)
	}
	return cfg
}

// savedText is the home's config.toml, as written.
func savedText(t *testing.T, root string) string {
	t.Helper()
	return readFile(t, filepath.Join(root, "config.toml"))
}

// inWalk answers whether p is one of the ports huddle walks through: 18860 to 18869.
func inWalk(p int) bool { return p >= server.PortFirst && p <= server.PortLast }

func TestTheFirstHuddleUpKeepsTheSavedPortAndTheNextStartReusesIt(t *testing.T) {
	home, w := oneHome(t)
	code, out := runServerCommand(t, w, "server")
	if code != 5 || !strings.Contains(out, "no port here yet") {
		t.Fatalf("server with no port = (%d, %q)", code, out)
	}
	pinned := pinFreePort(t, home)
	code, first := runServerCommand(t, w, "up")
	if code != 0 {
		t.Fatalf("up = (%d, %q)", code, first)
	}
	cfg := savedCfg(t, home)
	port := cfg.Port
	if port != pinned || !cfg.PortAuto {
		t.Errorf("saved = %+v, want the pinned %d marked auto", cfg, pinned)
	}
	if !strings.Contains(first, fmt.Sprintf("started at http://127.0.0.1:%d", port)) {
		t.Errorf("up says = %q", first)
	}
	if !regexp.MustCompile(fmt.Sprintf(`join: +/huddle:join 127\.0\.0\.1:%d --token [a-z0-9]{6}\.[a-z0-9]{16}`, port)).MatchString(first) {
		t.Errorf("no join line in %q", first)
	}
	if !regexp.MustCompile(fmt.Sprintf(`dashboard: http://127\.0\.0\.1:%d/\?code=\S+`, port)).MatchString(first) {
		t.Errorf("no dashboard link in %q", first)
	}
	// the run directory holds the pid, the port and the start's line in the log
	if _, err := os.Stat(filepath.Join(home, "run", "huddle.pid")); err != nil {
		t.Errorf("no pid file: %v", err)
	}
	if text := readFile(t, filepath.Join(home, "run", "huddle.port")); strings.TrimSpace(text) != strconv.Itoa(port) {
		t.Errorf("run/huddle.port = %q, want %d", text, port)
	}
	if log := readFile(t, filepath.Join(home, "run", "huddle.log")); !strings.Contains(log, "start pid") || !strings.Contains(log, fmt.Sprintf("port %d", port)) {
		t.Errorf("the run log misses the start's line:\n%s", log)
	}
	code, out = runServerCommand(t, w, "server")
	if code != 0 || !strings.Contains(out, fmt.Sprintf("up at http://127.0.0.1:%d", port)) {
		t.Errorf("server after up = (%d, %q)", code, out)
	}
	id := Identify(context.Background(), w, "")
	if id.URL != fmt.Sprintf("http://127.0.0.1:%d", port) || id.Source != "saved" {
		t.Errorf("identity after up = %+v", id)
	}
	if code, out := runServerCommand(t, w, "down"); code != 0 || !strings.Contains(out, "stopped") {
		t.Errorf("down = (%d, %q)", code, out)
	}
	if log := readFile(t, filepath.Join(home, "run", "huddle.log")); !strings.Contains(log, "stop pid") {
		t.Errorf("the run log misses the stop's line:\n%s", log)
	}
	code, again := runServerCommand(t, w, "up")
	if code != 0 || !strings.Contains(again, fmt.Sprintf("started at http://127.0.0.1:%d", port)) {
		t.Fatalf("up again = (%d, %q), want the same address", code, again)
	}
	if savedCfg(t, home).Port != port {
		t.Errorf("saved = %+v, want the port kept", savedCfg(t, home))
	}
}

func TestASavedPortAnotherProgramTookHuddleUpMovesToANewOneAndSaysSoAChosenOneIsNeverMoved(t *testing.T) {
	home, w := oneHome(t)
	other, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	taken := other.Addr().(*net.TCPAddr).Port
	writeFile(t, filepath.Join(home, "config.toml"), fmt.Sprintf("port = %d\nport_auto = true\n", taken))
	code, out := runServerCommand(t, w, "up")
	if code != 0 {
		t.Fatalf("up = (%d, %q)", code, out)
	}
	moved := savedCfg(t, home).Port
	if moved == taken || !inWalk(moved) || !savedCfg(t, home).PortAuto {
		t.Errorf("saved = %+v, want a new port from the walk", savedCfg(t, home))
	}
	if !strings.Contains(out, fmt.Sprintf("port %d is taken by another program: Huddle moved to port %d", taken, moved)) ||
		!strings.Contains(out, fmt.Sprintf("started at http://127.0.0.1:%d", moved)) {
		t.Errorf("up says = %q", out)
	}
	runServerCommand(t, w, "down")
	// someone's own choice: kept, and up says what to do instead of moving it
	writeFile(t, filepath.Join(home, "config.toml"), fmt.Sprintf("favourite = \"tea\"\nport = %d\n", taken))
	before := savedText(t, home)
	code, no := runServerCommand(t, w, "up")
	if code != 5 || !strings.Contains(no, fmt.Sprintf("port %d is taken by another program", taken)) {
		t.Fatalf("up on a chosen port = (%d, %q)", code, no)
	}
	if after := savedText(t, home); after != before {
		t.Errorf("the config was rewritten to %s, want %s", after, before)
	}
}

func TestAnOverrideWinsAndIsSavedHuddlePortAndHuddleUpPort(t *testing.T) {
	home, w := oneHome(t)
	first := freePort(t)
	writeFile(t, filepath.Join(home, "config.toml"), fmt.Sprintf("port = %d\nport_auto = true\n", first))
	p1 := freePort(t)
	code, out := runServerCommand(t, cliWorld(t, home, filepath.Join(t.TempDir(), "state"), map[string]string{"HUDDLE_PORT": strconv.Itoa(p1)}), "up")
	if code != 0 || !strings.Contains(out, fmt.Sprintf("started at http://127.0.0.1:%d", p1)) {
		t.Fatalf("up with HUDDLE_PORT = (%d, %q)", code, out)
	}
	if cfg := savedCfg(t, home); cfg.Port != p1 || cfg.PortAuto {
		t.Errorf("saved = %+v, want %d as a choice, no port_auto", cfg, p1)
	}
	if id := Identify(context.Background(), w, ""); id.URL != fmt.Sprintf("http://127.0.0.1:%d", p1) || id.Source != "saved" {
		t.Errorf("identity = %+v", id)
	}
	runServerCommand(t, w, "down")
	p2 := freePort(t)
	code, out = runServerCommand(t, w, "up", "--port", strconv.Itoa(p2))
	if code != 0 || !strings.Contains(out, fmt.Sprintf("started at http://127.0.0.1:%d", p2)) {
		t.Fatalf("up --port = (%d, %q)", code, out)
	}
	if cfg := savedCfg(t, home); cfg.Port != p2 || cfg.PortAuto {
		t.Errorf("saved = %+v, want %d as a choice", cfg, p2)
	}
	if code, out := runServerCommand(t, w, "up", "--port", "x"); code != 2 {
		t.Errorf("up --port x = (%d, %q), want usage", code, out)
	}
	// HUDDLE_URL still wins over every saved port
	id := Identify(context.Background(), cliWorld(t, home, filepath.Join(t.TempDir(), "state"), map[string]string{"HUDDLE_URL": "http://127.0.0.1:12345"}), "")
	if id.URL != "http://127.0.0.1:12345" || id.Source != "env" {
		t.Errorf("identity with HUDDLE_URL = %+v", id)
	}
}

func TestAnExplicit8808StaysARunningServerWithNoSavedPortIsFoundAndItsPortSaved(t *testing.T) {
	ctx := context.Background()
	kept := t.TempDir()
	writeFile(t, filepath.Join(kept, "config.toml"), "port = 8808\n")
	id := Identify(ctx, cliWorld(t, kept, filepath.Join(t.TempDir(), "state"), nil), "")
	if id.URL != "http://127.0.0.1:8808" || id.Source != "saved" {
		t.Errorf("identity = %+v", id)
	}
	if text := readFile(t, filepath.Join(kept, "config.toml")); text != "port = 8808\n" {
		t.Errorf("the file was rewritten to %s", text)
	}
	// a running server with a pid file but no port in the config: found once, its port saved
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, "config.toml"), "favourite = \"tea\"\n")
	port := freePort(t)
	srv := exec.Command(huddleBin, "serve")
	srv.Env = append(os.Environ(), fmt.Sprintf("PORT=%d", port), "HOST=127.0.0.1", "HUDDLE_TOKEN=test-root-credential",
		"HUDDLE_DATA="+filepath.Join(home, "data"), "HUDDLE_HOME="+home, "HOME="+t.TempDir(), "XDG_CONFIG_HOME="+t.TempDir(),
		"XDG_STATE_HOME="+t.TempDir(), "XDG_DATA_HOME="+t.TempDir())
	logFile, err := os.Create(filepath.Join(t.TempDir(), "child.log"))
	if err != nil {
		t.Fatal(err)
	}
	srv.Stdout, srv.Stderr = logFile, logFile
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	// the child is stopped, reaped and its log closed before this test's TempDirs go
	t.Cleanup(func() { stopProcess(srv.Process.Pid); _ = srv.Wait(); _ = logFile.Close() })
	w := cliWorld(t, home, filepath.Join(t.TempDir(), "state"), nil)
	healthyAt := func() bool {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}
	for i := 0; i < 300 && !healthyAt(); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if !healthyAt() {
		t.Fatal("the server did not answer /health")
	}
	writeFile(t, filepath.Join(home, "run", "huddle.pid"), strconv.Itoa(srv.Process.Pid))
	id = Identify(ctx, w, "")
	if id.URL != fmt.Sprintf("http://127.0.0.1:%d", port) || id.Source != "running" {
		t.Fatalf("identity = %+v, want the running server's port", id)
	}
	got := savedCfg(t, home)
	if got.Port != port {
		t.Errorf("saved = %+v, want the running server's port %d", got, port)
	}
	if got.PortAuto {
		t.Errorf("saved = %+v, the found port is the user's, not huddle's", got)
	}
	if !strings.Contains(savedText(t, home), "favourite") {
		t.Errorf("the rewrite lost the file's own keys:\n%s", savedText(t, home))
	}
	if id = Identify(ctx, w, ""); id.URL != fmt.Sprintf("http://127.0.0.1:%d", port) || id.Source != "saved" {
		t.Errorf("identity after the save = %+v", id)
	}
	if code, out := runServerCommand(t, w, "up"); code != 0 || !strings.Contains(out, fmt.Sprintf("up at http://127.0.0.1:%d", port)) {
		t.Errorf("up = (%d, %q)", code, out)
	}
}

// TestNoFixedPortTwoServersOnOneMachine pins two homes on one machine: two servers, each on its
// own saved port, both in the machine's registry.
func TestNoFixedPortTwoServersOnOneMachine(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	homeA, homeB := t.TempDir(), t.TempDir()
	wa := cliWorld(t, homeA, state, nil)
	wb := cliWorld(t, homeB, state, nil)
	pinFreePort(t, homeA)
	pinFreePort(t, homeB)
	if code, out := runServerCommand(t, wa, "up"); code != 0 {
		t.Fatalf("up A = (%d, %q)", code, out)
	}
	if code, out := runServerCommand(t, wb, "up"); code != 0 {
		t.Fatalf("up B = (%d, %q)", code, out)
	}
	portA := savedCfg(t, homeA).Port
	portB := savedCfg(t, homeB).Port
	if portA == portB {
		t.Fatalf("both homes landed on port %d", portA)
	}
	for _, port := range []int{portA, portB} {
		if !Healthy(context.Background(), fmt.Sprintf("http://127.0.0.1:%d", port), 1500) {
			t.Errorf("the server on %d is not up", port)
		}
	}
	registry := Servers(context.Background(), wa)
	entryA, okA := registry[homeA]
	entryB, okB := registry[homeB]
	if !okA || !okB {
		t.Fatalf("servers = %v", registry)
	}
	if entryA.URL != fmt.Sprintf("http://127.0.0.1:%d", portA) || entryB.URL != fmt.Sprintf("http://127.0.0.1:%d", portB) {
		t.Errorf("registry = %+v %+v", entryA, entryB)
	}
	if entryA.PID == entryB.PID || entryA.PID == 0 {
		t.Errorf("registry pids = %d %d", entryA.PID, entryB.PID)
	}
	// the registry holds no secret and notes both homes
	text := readFile(t, RegistryFile(wa))
	if strings.Contains(text, "test-root-credential") {
		t.Errorf("a secret in the registry:\n%s", text)
	}
	if !strings.Contains(text, jsonSpelled(homeA)) || !strings.Contains(text, jsonSpelled(homeB)) {
		t.Errorf("a home missing from the registry:\n%s", text)
	}
}

// jsonSpelled answers a path the way the registry's JSON writes it: every backslash is escaped
// there.
func jsonSpelled(path string) string {
	return strings.ReplaceAll(path, `\`, `\\`)
}

func TestHuddleUpServerAndDownRunTheBundledServerFromHuddleHomeAndLeaveAForeignOneAlone(t *testing.T) {
	port := freePort(t)
	home := t.TempDir()
	state := filepath.Join(t.TempDir(), "state")
	w := cliWorld(t, home, state, map[string]string{
		"HUDDLE_DATA": "", "HUDDLE_URL": fmt.Sprintf("http://127.0.0.1:%d", port), "HUDDLE_CHANNEL": "up", "HUDDLE_AS": "a"})
	if code, out := runServerCommand(t, w, "server"); code != 5 || !strings.Contains(out, "down") {
		t.Fatalf("server = (%d, %q)", code, out)
	}
	if code, out := runServerCommand(t, w, "up"); code != 0 || !strings.Contains(out, "started") {
		t.Fatalf("up = (%d, %q)", code, out)
	}
	if code, out := runServerCommand(t, w, "up"); code != 0 || !strings.Contains(out, "is up") {
		t.Fatalf("up again = (%d, %q)", code, out)
	}
	if _, out := runServerCommand(t, w, "server"); !strings.Contains(out, "started by huddle up") {
		t.Errorf("server = %q", out)
	}
	if code, out := runServerCommand(t, w, "down"); code != 0 || !strings.Contains(out, "stopped") {
		t.Errorf("down = (%d, %q)", code, out)
	}
	if code, out := runServerCommand(t, w, "down"); code != 0 || !strings.Contains(out, "not running") {
		t.Errorf("down again = (%d, %q)", code, out)
	}
	// a pid file left behind for some other process: never signalled
	writeFile(t, filepath.Join(home, "run", "huddle.pid"), strconv.Itoa(os.Getpid()))
	t.Cleanup(func() { _ = os.Remove(filepath.Join(home, "run", "huddle.pid")) })
	if _, out := runServerCommand(t, w, "down"); !strings.Contains(out, "not running") {
		t.Errorf("down past a foreign pid = %q", out)
	}
	// a server huddle up did not start is not stopped
	foreign, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() {
		served <- server.Serve(foreign, server.Options{
			Version: "test", Host: "127.0.0.1", Port: port, Home: filepath.Join(t.TempDir(), "home"),
			Data: filepath.Join(t.TempDir(), "data"), Token: "foreign-root", AutoCreate: true,
			Stdout: os.Stderr, Stderr: os.Stderr,
		})
	}()
	healthy := false
	for i := 0; i < 100 && !healthy; i++ {
		time.Sleep(50 * time.Millisecond)
		healthy = Healthy(context.Background(), fmt.Sprintf("http://127.0.0.1:%d", port), 1000)
	}
	if !healthy {
		select {
		case err := <-served:
			t.Fatalf("the foreign server did not come up: %v", err)
		default:
			t.Fatal("the foreign server did not come up")
		}
	}
	if code, out := runServerCommand(t, cliWorld(t, home, state, map[string]string{"HUDDLE_URL": fmt.Sprintf("http://127.0.0.1:%d", port)}), "down"); code != 2 {
		t.Fatalf("down on a foreign server = (%d, %q), want refusal", code, out)
	}
	// an address that is not local http: refused, never started
	far := cliWorld(t, home, state, map[string]string{"HUDDLE_URL": "http://10.255.255.1:41234"})
	if result := Up(context.Background(), far, "http://10.255.255.1:41234", 2000, ""); result.OK {
		t.Fatalf("up far away = %+v, want a refusal", result)
	}
}

// TestServerCommandSaysNoPortAndDownWithoutAnythingIsFine pins the two quiet answers.
func TestServerCommandSaysNoPortAndDownWithoutAnythingIsFine(t *testing.T) {
	_, w := oneHome(t)
	code, out := runServerCommand(t, w, "server")
	if code != 5 || !strings.Contains(out, "no port here yet") || !strings.Contains(out, "data:") || !strings.Contains(out, "log:") {
		t.Fatalf("server = (%d, %q)", code, out)
	}
	if code, out := runServerCommand(t, w, "down"); code != 0 || !strings.Contains(out, "not running") {
		t.Errorf("down = (%d, %q)", code, out)
	}
}

// TestKeepOutOfGitListsTheHomeInTheRepoExclude pins where the run files land: <repo>/.agents/
// huddle is listed in the repo's own exclude file, once, and only inside a repo.
func TestKeepOutOfGitListsTheHomeInTheRepoExclude(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(repo, ".agents", "huddle")
	w := world(t, repo, nil)
	if got := KeepOutOfGit(context.Background(), w, home); got != filepath.Join(repo, ".git", "info", "exclude") {
		t.Fatalf("KeepOutOfGit = %q", got)
	}
	text := readFile(t, filepath.Join(repo, ".git", "info", "exclude"))
	if !strings.Contains(text, ".agents/huddle/") {
		t.Errorf("exclude = %q", text)
	}
	if got := KeepOutOfGit(context.Background(), w, home); got != "" {
		t.Errorf("listed twice: %q", got)
	}
	if got := KeepOutOfGit(context.Background(), w, filepath.Join(repo, "elsewhere")); got != "" {
		t.Errorf("a home outside .agents/huddle: %q", got)
	}
	if got := KeepOutOfGit(context.Background(), world(t, t.TempDir(), nil), filepath.Join(t.TempDir(), ".agents", "huddle")); got != "" {
		t.Errorf("no repo: %q", got)
	}
}

// TestServersRegistryNotesEachStart pins the registry's read: what up noted, homes as keys, and
// an unreadable file is no servers, never an error.
func TestServersRegistryNotesEachStart(t *testing.T) {
	w := world(t, t.TempDir(), nil)
	if got := Servers(context.Background(), w); len(got) != 0 {
		t.Errorf("no registry = %v", got)
	}
	if err := os.MkdirAll(filepath.Dir(RegistryFile(w)), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, RegistryFile(w), `{"/a":{"url":"http://127.0.0.1:1","channel":"c","project":"/p","pid":9,"at":"2026-10-15T12:00:00.000Z"}}`)
	got := Servers(context.Background(), w)
	entry, ok := got["/a"]
	if !ok || entry != (ServerEntry{URL: "http://127.0.0.1:1", Channel: "c", Project: "/p", PID: 9, At: "2026-10-15T12:00:00.000Z"}) {
		t.Errorf("servers = %+v", got)
	}
	writeFile(t, RegistryFile(w), "not json")
	if again := Servers(context.Background(), w); len(again) != 0 {
		t.Errorf("an unreadable registry = %v", again)
	}
}

// TestHealthyKnowsAnUnreachableServer pins the health check the up and down commands wait on.
func TestHealthyKnowsAnUnreachableServer(t *testing.T) {
	srv := startSilentServer(t)
	if !Healthy(context.Background(), srv.url, 1500) {
		t.Error("a healthy server")
	}
	if Healthy(context.Background(), srv.url+"/nothing", 1500) {
		t.Error("a 404 is not healthy")
	}
	if Healthy(context.Background(), "http://127.0.0.1:1", 100) {
		t.Error("a dead port")
	}
	if Healthy(context.Background(), "", 100) {
		t.Error("no url")
	}
}

// silentServer is one HTTP server that answers /health and nothing else.
type silentServer struct{ url string }

func startSilentServer(t *testing.T) *silentServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })
	return &silentServer{url: fmt.Sprintf("http://127.0.0.1:%d", listener.Addr().(*net.TCPAddr).Port)}
}

// TestUpMintsARootCredentialHandsItToTheChildOnStdinAndKeepsItAsTheSessions pins the creator's
// side: with no HUDDLE_TOKEN the credential the child gets on stdin is the one this session
// keeps, and the server answers with it.
func TestUpMintsARootCredentialHandsItToTheChildOnStdinAndKeepsItAsTheSessions(t *testing.T) {
	home, w := oneHome(t)
	pinFreePort(t, home)
	// no HUDDLE_TOKEN: up mints one
	tokenFree := &World{Getenv: func(key string) string {
		if key == "HUDDLE_TOKEN" {
			return ""
		}
		return w.Getenv(key)
	}, Environ: w.Environ, Dir: w.Dir, Now: w.Now, Bin: w.Bin}
	result := Up(context.Background(), tokenFree, "", 15000, "")
	if !result.OK {
		t.Fatalf("up = %+v", result)
	}
	if !result.Created || !result.First {
		t.Errorf("up = %+v, want the creator's first start", result)
	}
	held := LoadCred(context.Background(), tokenFree, result.URL, "")
	if held == nil || !held.Root || held.Credential == "" {
		t.Fatalf("the session's credential = %+v", held)
	}
	if pid := RunningPid(context.Background(), w, home); pid <= 0 {
		t.Fatal("no running server after up")
	}
	if _, err := os.Stat(filepath.Join(home, "auth.json")); err != nil {
		t.Errorf("no auth file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "run", "huddle.pid")); err != nil {
		t.Errorf("no pid file: %v", err)
	}
	resp, err := http.Get(result.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/health = %d", resp.StatusCode)
	}
	if got := TokenFor(context.Background(), tokenFree, result.URL, ""); got != held.Credential {
		t.Errorf("TokenFor = %q, want the held root", got)
	}
	var out strings.Builder
	if code := ServerCommand(context.Background(), w, "server", result.URL, nil, &out, &out); code != 0 || !strings.Contains(out.String(), "started by huddle up") {
		t.Errorf("server = (%d, %q)", code, out.String())
	}
	if !strings.Contains(out.String(), result.URL) || !strings.Contains(out.String(), "data:") || !strings.Contains(out.String(), "log:") {
		t.Errorf("server = %q", out.String())
	}
}

// TestUpRefusesWhatItCannotPlace pins place's refusals: a running server whose port cannot be
// read, a port a stranger holds and nobody chose, and an address that is not local http.
func TestUpRefusesWhatItCannotPlace(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	w := cliWorld(t, home, filepath.Join(t.TempDir(), "state"), nil)
	writeFile(t, filepath.Join(home, "run", "huddle.pid"), "4321")
	t.Cleanup(func() { _ = os.Remove(filepath.Join(home, "run", "huddle.pid")) }) // before the stopper's lookup
	w.Proc = Proc{
		Command:  func(pid int) (string, bool) { return "/somewhere/huddle serve", true },
		ListenOf: func(pid int) (int, bool) { return 0, false }, // its port cannot be read
	}
	result := Up(ctx, w, "", 3000, "")
	if result.OK || !strings.Contains(result.Msg, fmt.Sprintf("a Huddle server (pid 4321) runs from %s but its port is unknown", home)) {
		t.Fatalf("up = %+v", result)
	}
	// a busy port nothing of ours holds and nobody chose: refused
	home2 := t.TempDir()
	w2 := cliWorld(t, home2, filepath.Join(t.TempDir(), "state"), nil)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	taken := busy.Addr().(*net.TCPAddr).Port
	writeFile(t, filepath.Join(home2, "config.toml"), fmt.Sprintf("port = %d\n", taken))
	result = Up(ctx, w2, fmt.Sprintf("http://127.0.0.1:%d", taken), 3000, "")
	if result.OK || !strings.Contains(result.Msg, "is taken by another program, not Huddle") {
		t.Fatalf("up = %+v", result)
	}
	// an address that is neither http nor loopback: refused, never started
	result = Up(ctx, w2, "https://127.0.0.1:9443", 3000, "")
	if result.OK || !strings.Contains(result.Msg, "not a local http address") {
		t.Fatalf("up = %+v", result)
	}
	result = Up(ctx, w2, "http://10.255.255.1:41234", 3000, "")
	if result.OK || !strings.Contains(result.Msg, "not a local http address") {
		t.Fatalf("up = %+v", result)
	}
}

// TestInviteAndJoinLineAndDashboard pin the three HTTP answers the creator's lines come from.
func TestInviteAndJoinLineAndDashboard(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tokens":
			if r.Header.Get(auth.Header) != "root-cred" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"token":"abc123.def0123456789abcdef","id":"abc123","expires":"2026-10-16T12:00:00.000Z"}`))
		case "/api/login":
			_, _ = w.Write([]byte(`{"code":"o ne&=?"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	w := world(t, t.TempDir(), map[string]string{"HUDDLE_TOKEN": "root-cred"})
	host := strings.TrimPrefix(srv.URL, "http://")
	inv := MintInvite(ctx, w, srv.URL, InviteOptions{SingleUse: true}, "")
	if !inv.OK || inv.Token != "abc123.def0123456789abcdef" || inv.ID != "abc123" ||
		inv.Expires != "2026-10-16T12:00:00.000Z" || inv.Join != "huddle join "+host+" --token abc123.def0123456789abcdef" {
		t.Fatalf("MintInvite = %+v", inv)
	}
	line := JoinLine(ctx, w, srv.URL, InviteOptions{SingleUse: true}, "")
	if !line.OK || line.Line != "/huddle:join "+host+" --token abc123.def0123456789abcdef" ||
		line.Expires != "2026-10-16T12:00:00.000Z" {
		t.Errorf("JoinLine = %+v", line)
	}
	if got := Dashboard(ctx, w, srv.URL, "", 1500); got != srv.URL+"/?code=o%20ne%26%3D%3F" {
		t.Errorf("Dashboard = %q", got)
	}
	// a server that refuses or cannot be reached: an error, never a panic
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"error":"no tokens for you"}`))
	}))
	if inv := MintInvite(ctx, world(t, t.TempDir(), map[string]string{"HUDDLE_TOKEN": "x"}), srv2.URL, InviteOptions{}, ""); inv.OK || inv.Error != "no tokens for you" {
		t.Errorf("MintInvite refused = %+v", inv)
	}
	srv2.Close()
	if inv := MintInvite(ctx, w, "http://127.0.0.1:1", InviteOptions{}, ""); inv.OK || inv.Error == "" {
		t.Errorf("MintInvite unreachable = %+v", inv)
	}
	if got := Dashboard(ctx, w, "http://127.0.0.1:1", "", 50); got != "" {
		t.Errorf("Dashboard unreachable = %q", got)
	}
}

// TestUpAloneStartsTheServerWithoutTheRemovalWatch pins the alone start's one difference from
// the plugin's: the child it spawns carries no removal watch, so a server the owner started by
// hand never stops itself because the huddle plugin left Claude Code. The shim binary records
// the child's environment, then hands the line to the real server entry.
func TestUpAloneStartsTheServerWithoutTheRemovalWatch(t *testing.T) {
	home := t.TempDir()
	shim := t.TempDir()
	record := filepath.Join(shim, "child.env")
	script := filepath.Join(shim, "huddle")
	body := "#!/bin/sh\n/usr/bin/env > " + record + "\nexec " + huddleBin + " \"$@\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	w := cliWorld(t, home, filepath.Join(t.TempDir(), "state"), nil)
	w.Bin = script
	pinFreePort(t, home)
	result := UpAlone(context.Background(), w, "", 35_000)
	if !result.OK {
		t.Fatalf("UpAlone = %+v", result)
	}
	if !Healthy(context.Background(), result.URL, 1500) {
		t.Fatalf("nothing answers at %s", result.URL)
	}
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the shim never recorded the child's environment: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "HUDDLE_REMOVAL_WATCH=") {
			t.Errorf("the alone start carried %q", line)
		}
	}
	for _, want := range []string{"HUDDLE_ROOT_STDIN=1\n", "HUDDLE_HOME=" + home + "\n", "HOST=127.0.0.1\n"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the child's environment lacks %q", want)
		}
	}
	if !regexp.MustCompile(`(?m)^PORT=\d+$`).Match(raw) {
		t.Errorf("the child's environment names no port: %q", raw)
	}
}

// pinnedPorts hands each pinFreePort call its own starting offset, 97 ports past the last one:
// two calls in one clock tick (one test pinning two homes) must never land on the same port,
// since each probe is closed before its server binds.
var pinnedPorts = func() *atomic.Int64 {
	var n atomic.Int64
	n.Store(time.Now().UnixNano() % 10000)
	return &n
}()

// pinFreePort saves a free port in the home's config, so the server a test starts keeps that
// port: with no saved one the port choice walks 18860-18869, where test packages running in
// parallel race for the same port. The port comes from 20000-29999, below the ephemeral ranges
// of macOS (49152+) and Linux (32768+): a port the OS hands out for port 0 would be free again
// once the probe closes, and another test binding port 0 could take it before the server binds.
func pinFreePort(t *testing.T, homeRoot string) int {
	t.Helper()
	start := int(pinnedPorts.Add(97))
	for i := range 10000 {
		port := 20000 + (start+i)%10000
		listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			continue
		}
		_ = listener.Close()
		if _, err := home.SavePort(home.Home{Root: homeRoot}, port, true); err != nil {
			t.Fatalf("save the port in %s: %v", homeRoot, err)
		}
		return port
	}
	t.Fatal("no free port in 20000-29999")
	return 0
}

// `huddle down` leaves no owner key behind: the server takes it on its clean exit, and a forced
// one (the process would not stop) loses it to down itself.
func TestDownTakesTheOwnerKeyWithIt(t *testing.T) {
	port := freePort(t)
	homeDir := t.TempDir()
	state := filepath.Join(t.TempDir(), "state")
	w := cliWorld(t, homeDir, state, map[string]string{
		"HUDDLE_DATA": "", "HUDDLE_URL": fmt.Sprintf("http://127.0.0.1:%d", port), "HUDDLE_CHANNEL": "up", "HUDDLE_AS": "a"})
	if code, out := runServerCommand(t, w, "up"); code != 0 || !strings.Contains(out, "started") {
		t.Fatalf("up = (%d, %q)", code, out)
	}
	key := filepath.Join(homeDir, "run", "owner.key")
	if _, err := os.Stat(key); err != nil {
		t.Fatalf("no owner key after up: %v", err)
	}
	if code, out := runServerCommand(t, w, "down"); code != 0 || !strings.Contains(out, "stopped") {
		t.Fatalf("down = (%d, %q)", code, out)
	}
	if _, err := os.Stat(key); !os.IsNotExist(err) {
		t.Errorf("owner.key after down: %v, want it gone", err)
	}
}
