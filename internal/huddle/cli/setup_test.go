// huddle setup: the project's .agents/huddle/huddle.json, kept out of git, the port it saves,
// the server it starts or joins, and the notifications switch.
package cli

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// setupWorld is one project running huddle setup: its own directory and state, none of the real
// machine's Huddle variables, the given overrides on top.
func setupWorld(t *testing.T, proj string, env map[string]string) *World {
	t.Helper()
	vars := map[string]string{
		"CLAUDE_PROJECT_DIR":     proj,
		"XDG_STATE_HOME":         filepath.Join(t.TempDir(), "state"),
		"HUDDLE_NO_PROJECT_CRED": "",
	}
	for key, value := range env {
		vars[key] = value // "" passes through: world() treats it as unset
	}
	return world(t, proj, vars)
}

// runSetup is huddle setup in w.
func runSetup(t *testing.T, w *World, args ...string) (int, string, string) {
	t.Helper()
	return runH(t, w, append([]string{"setup"}, args...)...)
}

// gitInit makes dir a repository of its own.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	init := exec.Command("git", "init", "-q")
	init.Dir = dir
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v %s", dir, err, out)
	}
}

// userHome is the home a world resolves: its throwaway HOME's ~/.config/huddle.
func userHome(t *testing.T, w *World) string {
	t.Helper()
	return filepath.Join(w.Getenv("HOME"), ".config", "huddle")
}

// setup show names the settings, the files, the port, the switch and the server's state.
func TestSetupShowNamesEverything(t *testing.T) {
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".agents", "huddle"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(proj, ".agents", "huddle", "huddle.json"), `{"channel":"shop","as":"api"}`)
	w := setupWorld(t, proj, map[string]string{"HUDDLE_URL": "http://127.0.0.1:1"})
	home := userHome(t, w)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, "config.toml"), "port = 41873\nport_auto = true\n")
	code, out, errOut := runSetup(t, w, "show")
	if code != 0 || errOut != "" {
		t.Fatalf("setup show: %d %q", code, errOut)
	}
	lines := strings.Split(out, "\n")
	want := []string{
		"settings: " + filepath.Join(proj, ".agents", "huddle", "huddle.json") + ` {"channel":"shop","as":"api"}`,
		"files:    " + home,
		"port:     41873 (picked by huddle up)",
		"notify:   desktop notifications on (huddle setup --no-notify | --notify)",
	}
	for i, wantLine := range want {
		got := ""
		if i < len(lines) {
			got = lines[i]
		}
		if got != wantLine {
			t.Errorf("show line %d = %q, want %q", i, got, wantLine)
		}
	}
	if !strings.Contains(out, "Huddle is down at http://127.0.0.1:1") {
		t.Errorf("show's server half said %q", out)
	}
	// nothing set up yet
	fresh := setupWorld(t, t.TempDir(), nil)
	code, out, _ = runSetup(t, fresh, "show")
	if code != 0 {
		t.Fatalf("empty show: %d", code)
	}
	if !strings.Contains(out, "settings: none yet (run huddle setup)") ||
		!strings.Contains(out, "port:     none yet (huddle up picks 18860, then 18861 to 18869)") ||
		!strings.Contains(out, "Huddle has no port here yet") {
		t.Errorf("empty show said %q", out)
	}
}

// A first setup asks nothing: the channel and the session are named after the project,
// autostart is on, and the file lands where the hooks read it.
func TestAFirstSetupNamesChannelAndSessionAfterTheProject(t *testing.T) {
	proj := t.TempDir()
	w := setupWorld(t, proj, nil)
	code, out, errOut := runSetup(t, w)
	if code != 0 || errOut != "" {
		t.Fatalf("setup: %d %q %q", code, out, errOut)
	}
	file := filepath.Join(proj, ".agents", "huddle", "huddle.json")
	name := Slug(proj, "huddle")
	if raw := readFile(t, file); raw != "{\n  \"channel\": \""+name+"\",\n  \"as\": \""+name+"\",\n  \"autostart\": true\n}\n" {
		t.Errorf("the file %q", raw)
	}
	if !strings.Contains(out, file+": {\"channel\":\""+name+"\",\"as\":\""+name+"\",\"autostart\":true}") {
		t.Errorf("setup said %q", out)
	}
	if !strings.Contains(out, "channels: "+filepath.Join(userHome(t, w), "channels")) {
		t.Errorf("the channels line %q", out)
	}
	if !strings.Contains(out, "start it with: huddle up") {
		t.Errorf("no start hint: %q", out)
	}
}

// --channel and --as go together; an unknown setup subcommand is a usage error.
func TestSetupChannelAndAsGoTogetherAndUnknownSubsRefuse(t *testing.T) {
	w := setupWorld(t, t.TempDir(), nil)
	code, _, errOut := runSetup(t, w, "--channel", "x")
	if code != 2 || errOut != "huddle setup: --channel and --as go together\n" {
		t.Fatalf("channel alone: %d %q", code, errOut)
	}
	code, _, errOut = runSetup(t, w, "--as", "x")
	if code != 2 || errOut != "huddle setup: --channel and --as go together\n" {
		t.Fatalf("as alone: %d %q", code, errOut)
	}
	code, _, errOut = runSetup(t, w, "nope")
	if code != 2 || errOut != "huddle setup: unknown setup nope: use show, or flags only\n" {
		t.Fatalf("unknown sub: %d %q", code, errOut)
	}
	// a port that is not one
	code, _, errOut = runSetup(t, w, "--port", "x")
	if code != 2 || errOut != "huddle setup: --port x: a port number\n" {
		t.Fatalf("bad port: %d %q", code, errOut)
	}
}

// A .agents/.huddle.json is folded into huddle.json and removed; the settings keep their shape.
func TestSetupFoldsTheLegacyFileAndKeepsTheSettingsShape(t *testing.T) {
	proj := t.TempDir()
	gitInit(t, proj)
	if err := os.MkdirAll(filepath.Join(proj, ".agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(proj, ".agents", ".huddle.json"), `{"channel":"old","as":"alpha","role":"kept"}`)
	w := setupWorld(t, proj, nil)
	code, out, errOut := runSetup(t, w, "--channel", "s1", "--as", "alpha", "--autostart")
	if code != 0 || errOut != "" {
		t.Fatalf("setup: %d %q", code, errOut)
	}
	file := filepath.Join(proj, ".agents", "huddle", "huddle.json")
	if raw := readFile(t, file); raw != "{\n  \"channel\": \"s1\",\n  \"as\": \"alpha\",\n  \"role\": \"kept\",\n  \"autostart\": true\n}\n" {
		t.Errorf("the file %q", raw)
	}
	if _, err := os.Stat(filepath.Join(proj, ".agents", ".huddle.json")); !os.IsNotExist(err) {
		t.Error("the legacy file stayed")
	}
	if !strings.Contains(out, "folded "+filepath.Join(proj, ".agents", ".huddle.json")+" into it") {
		t.Errorf("the fold line %q", out)
	}
	exclude := filepath.Join(proj, ".git", "info", "exclude")
	if raw := readFile(t, exclude); !strings.Contains(raw, ".agents/huddle/") {
		t.Errorf("the exclude file %q", raw)
	}
	if !strings.Contains(out, "kept .agents/huddle/ out of git ("+exclude+")") {
		t.Errorf("the git line %q", out)
	}
	if out, _ := exec.Command("git", "-C", proj, "status", "--porcelain").Output(); len(out) != 0 {
		t.Errorf("git status is not clean: %q", out)
	}
}

// --no-autostart records a false; --listen lists channels; --no-listen removes the list.
func TestSetupAutostartAndListenFlags(t *testing.T) {
	proj := t.TempDir()
	w := setupWorld(t, proj, nil)
	if code, _, errOut := runSetup(t, w, "--listen", "a, b", "--role", "r"); code != 0 || errOut != "" {
		t.Fatalf("setup: %q", errOut)
	}
	file := filepath.Join(proj, ".agents", "huddle", "huddle.json")
	if raw := readFile(t, file); !strings.Contains(raw, "\"listen\": [\n    \"a\",\n    \"b\"\n  ]") || !strings.Contains(raw, `"role": "r"`) {
		t.Errorf("the file %q", raw)
	}
	// a second setup keeps the channel it chose and turns autostart off
	if code, _, errOut := runSetup(t, w, "--no-autostart"); code != 0 || errOut != "" {
		t.Fatalf("setup again: %q", errOut)
	}
	if raw := readFile(t, file); !strings.Contains(raw, `"autostart": false`) {
		t.Errorf("autostart stayed on: %q", raw)
	}
	// --no-listen drops the list again
	if code, _, errOut := runSetup(t, w, "--no-listen"); code != 0 || errOut != "" {
		t.Fatalf("setup no-listen: %q", errOut)
	}
	if raw := readFile(t, file); strings.Contains(raw, "listen") {
		t.Errorf("the listen list stayed: %q", raw)
	}
}

// A chosen port is saved in the home's config, and an env url wins over it, with a note.
func TestSetupSavesThePortAndNotesWhatWins(t *testing.T) {
	proj := t.TempDir()
	port := freePort(t)
	w := setupWorld(t, proj, map[string]string{"HUDDLE_URL": "http://127.0.0.1:9"})
	code, out, errOut := runSetup(t, w, "--port", strconv.Itoa(port))
	if code != 0 || errOut != "" {
		t.Fatalf("setup --port: %d %q", code, errOut)
	}
	home := userHome(t, w)
	if !strings.Contains(out, "port:     "+strconv.Itoa(port)+" (saved in "+filepath.Join(home, "config.toml")+")") {
		t.Errorf("the port line %q", out)
	}
	if !strings.Contains(out, "note:     HUDDLE_URL wins: the server stays at http://127.0.0.1:9") {
		t.Errorf("the note %q", out)
	}
	if cfg := savedCfg(t, home); cfg.Port != port || cfg.PortAuto {
		t.Errorf("the saved port = %+v, want %d as a choice", cfg, port)
	}
	// with nothing else set, the port becomes the server's address
	plain := setupWorld(t, proj, map[string]string{"HUDDLE_URL": ""})
	_, out, _ = runSetup(t, plain, "--port", strconv.Itoa(port))
	if strings.Contains(out, "wins:") {
		t.Errorf("an unoverridden port still noted: %q", out)
	}
}

// setup --start starts the server, joins the channel itself, and prints the join line and the
// dashboard link.
func TestSetupStartJoinsTheChannelItselfAndPrintsTheWayIn(t *testing.T) {
	proj := t.TempDir()
	gitInit(t, proj)
	state := filepath.Join(t.TempDir(), "state")
	w := setupWorld(t, proj, map[string]string{
		"HUDDLE_SESSION":         "s-1",
		"XDG_STATE_HOME":         state,
		"HUDDLE_NO_PROJECT_CRED": "",
		"CLAUDECODE":             "1",
	})
	pinFreePort(t, userHome(t, w))
	// registered before the start, so a failing assertion still stops the server it started
	t.Cleanup(func() { stopHome(t, w, HomeOf(context.Background(), w)) })
	code, out, errOut := runSetup(t, w, "--start")
	if code != 0 {
		t.Fatalf("setup --start: %d %q %q", code, out, errOut)
	}
	match := regexp.MustCompile(`Huddle started at http://127\.0\.0\.1:(\d+)`).FindStringSubmatch(out)
	if match == nil {
		t.Fatalf("setup --start said %q", out)
	}
	port := match[1]
	if !regexp.MustCompile(`(?m)^join:     /huddle:join 127\.0\.0\.1:` + port + ` --token [a-z0-9]{6}\.[a-z0-9]{16}   \(valid`).MatchString(out) {
		t.Errorf("the join line %q", out)
	}
	dash := regexp.MustCompile(`(?m)^dashboard: (http://127\.0\.0\.1:` + port + `/\?code=\S+)`).FindStringSubmatch(out)
	if dash == nil {
		t.Fatalf("no dashboard link in %q", out)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	signin, err := client.Get(dash[1])
	if err != nil {
		t.Fatalf("the sign-in link: %v", err)
	}
	signin.Body.Close()
	if signin.StatusCode != 303 {
		t.Errorf("the sign-in link got %d, want 303", signin.StatusCode)
	}
	// the channel exists: status answers right after
	code, status, errOut := runH(t, w, "status")
	if code != 0 {
		t.Fatalf("status after setup: %d %q %q", code, status, errOut)
	}
	if !strings.Contains(status, "you are ") {
		t.Errorf("status said %q", status)
	}
}

// setup --restart stops this project's server first and starts it again.
func TestSetupRestartStopsTheServerFirst(t *testing.T) {
	home := t.TempDir()
	w := cliWorld(t, home, filepath.Join(t.TempDir(), "state"), nil)
	pinFreePort(t, home)
	if code, out := runServerCommand(t, w, "up"); code != 0 || !strings.Contains(out, "started") {
		t.Fatalf("up: %d %s", code, out)
	}
	code, out, errOut := runH(t, w, "setup", "--restart", "--start")
	if code != 0 || errOut != "" {
		t.Fatalf("setup --restart: %d %q %q", code, out, errOut)
	}
	if !strings.Contains(out, "Huddle stopped (pid ") || !strings.Contains(out, "Huddle started at") {
		t.Errorf("restart said %q", out)
	}
}

// The notifications switch: off in this home's data dir, and on the running Huddle too.
func TestSetupNotifySwitchesTheHomeAndTheRunningHuddle(t *testing.T) {
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		if call.Method == "POST" && call.Path == "/api/settings" {
			return 200, `{}`
		}
		return 404, `{"error":"nope"}`
	})
	proj := t.TempDir()
	w := setupWorld(t, proj, map[string]string{"HUDDLE_URL": fake.server.URL})
	code, out, errOut := runSetup(t, w, "--no-notify")
	if code != 0 || errOut != "" {
		t.Fatalf("setup --no-notify: %d %q", code, errOut)
	}
	if !strings.Contains(out, "notify:   desktop notifications off (also on the running Huddle at "+fake.server.URL+")") {
		t.Errorf("the notify line %q", out)
	}
	if raw := readFile(t, filepath.Join(userHome(t, w), "settings.json")); !strings.Contains(raw, `"notify": false`) {
		t.Errorf("the switch file %q", raw)
	}
	code, out, _ = runSetup(t, w, "--notify")
	if code != 0 || !strings.Contains(out, "notify:   desktop notifications on") {
		t.Errorf("notify on said %q", out)
	}
}

// setup in a second project joins the Huddle this user already runs, instead of starting
// another; --new starts one anyway.
func TestSetupInASecondProjectJoinsTheHuddleThisUserAlreadyRuns(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	// one user, one home: both projects' worlds share it, so the credential the first saved is
	// the second's to find
	user := t.TempDir()
	port := freePort(t)
	url := "http://127.0.0.1:" + strconv.Itoa(port)
	api, web, docs := t.TempDir(), t.TempDir(), t.TempDir()
	for _, proj := range []string{api, web, docs} {
		gitInit(t, proj)
	}
	sess := func(proj, sid string) *World {
		return setupWorld(t, proj, map[string]string{
			"HOME":                   user,
			"XDG_STATE_HOME":         state,
			"HUDDLE_SESSION":         sid,
			"HUDDLE_NO_PROJECT_CRED": "",
		})
	}
	A := sess(api, "s-api")
	code, out, errOut := runSetup(t, A, "--port", strconv.Itoa(port), "--start")
	if code != 0 || !strings.Contains(out, "started") {
		t.Fatalf("api setup: %d %q %q", code, out, errOut)
	}
	t.Cleanup(func() { _, _ = runServerCommand(t, A, "down") })
	_, whoami, _ := runH(t, A, "whoami")
	channel := regexp.MustCompile(`"channel":"([^"]+)"`).FindStringSubmatch(whoami)
	if channel == nil {
		t.Fatalf("whoami said %q", whoami)
	}
	W := sess(web, "s-web")
	code, out, errOut = runSetup(t, W, "--start")
	if code != 0 {
		t.Fatalf("web setup: %d %q %q", code, out, errOut)
	}
	if !strings.Contains(out, "Joined the Huddle that already runs for "+realDir(t, api)+": "+url+", channel "+channel[1]+", as ") {
		t.Errorf("web setup said %q", out)
	}
	if noted := Servers(context.Background(), A); len(noted) != 1 {
		t.Errorf("a second server was started: %v", noted)
	}
	if code, _, errOut := runH(t, W, "send", "hello from web"); code != 0 {
		t.Fatalf("web send: %s", errOut)
	}
	next := sess(web, "s-web-2")
	if code, status, errOut := runH(t, next, "status"); code != 0 || !strings.Contains(status, "you are") {
		t.Fatalf("web's next session: %d %q %q", code, status, errOut)
	}
	code, out, _ = runSetup(t, W)
	if code != 0 || !strings.Contains(out, "already in the Huddle") {
		t.Errorf("setup again said %q", out)
	}
	// --new starts a separate one, which in one-home-per-user terms is another home
	D := setupWorld(t, docs, map[string]string{
		"HOME": t.TempDir(), "XDG_STATE_HOME": state, "HUDDLE_SESSION": "s-docs",
	})
	pinFreePort(t, userHome(t, D))
	code, out, errOut = runSetup(t, D, "--new", "--start")
	if code != 0 || !strings.Contains(out, "Huddle started at") {
		t.Fatalf("docs setup --new: %d %q %q", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(D.Getenv("HOME"), ".config", "huddle", "run", "huddle.pid")); err != nil {
		t.Errorf("docs --new started no server: %v", err)
	}
	_, _ = runServerCommand(t, D, "down")
}

// With no credential to invite through, setup says how to join instead of starting a second one.
func TestSetupWithoutACredentialThereSaysHowToJoin(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	api, web := t.TempDir(), t.TempDir()
	for _, proj := range []string{api, web} {
		gitInit(t, proj)
	}
	// the registry names a server whose project holds no credential this machine kept
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) { return 200, `{"ok":true}` })
	if err := os.MkdirAll(filepath.Join(state, "huddle"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(state, "huddle", "servers.json"),
		`{"`+jsonSpelled(filepath.Join(api, ".agents", "huddle"))+`":{"url":"`+fake.server.URL+`","channel":"team","project":"`+jsonSpelled(api)+`"}}`)
	W := setupWorld(t, web, map[string]string{
		"XDG_STATE_HOME": state,
		"HUDDLE_SESSION": "s-web",
	})
	code, out, errOut := runSetup(t, W)
	if code != 0 || errOut != "" {
		t.Fatalf("web setup: %d %q %q", code, out, errOut)
	}
	if !strings.Contains(out, "A Huddle already runs for "+api+" at "+fake.server.URL+" (channel team). To join it, run /huddle:invite in a session there and paste its join line here (/huddle:join …). To start a separate one anyway: /huddle:setup --new") {
		t.Errorf("web setup said %q", out)
	}
	if _, err := os.Stat(filepath.Join(web, ".agents", "huddle", "huddle.pid")); !os.IsNotExist(err) {
		t.Error("a server was started anyway")
	}
}

// A held credential for that server means the project is already in it.
func TestSetupWithAHeldCredentialSaysAlreadyIn(t *testing.T) {
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) { return 200, `{"ok":true}` })
	state := filepath.Join(t.TempDir(), "state")
	api, web := t.TempDir(), t.TempDir()
	for _, proj := range []string{api, web} {
		gitInit(t, proj)
	}
	if err := os.MkdirAll(filepath.Join(state, "huddle"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(state, "huddle", "servers.json"),
		`{"`+jsonSpelled(filepath.Join(api, ".agents", "huddle"))+`":{"url":"`+fake.server.URL+`","channel":"team","project":"`+jsonSpelled(api)+`"}}`)
	// web's session already holds a credential for that server, in the home its world resolves
	W := setupWorld(t, web, map[string]string{
		"XDG_STATE_HOME": state,
		"HUDDLE_SESSION": "s-web",
	})
	sessions := filepath.Join(userHome(t, W), "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sessions, "s-web.json"),
		`{"url":"`+fake.server.URL+`","channel":"team","as":"web","credential":"hcred_web"}`)
	code, out, errOut := runSetup(t, W)
	if code != 0 || errOut != "" {
		t.Fatalf("web setup: %d %q", code, errOut)
	}
	if !strings.Contains(out, "This project is already in the Huddle at "+fake.server.URL+" (channel team, as web). To start a separate one: /huddle:setup --new") {
		t.Errorf("web setup said %q", out)
	}
}

// The root credential `huddle up` saves its creator names no channel: it is the server's key,
// not a membership, so setup still says how to join instead of claiming the project is in.
func TestSetupWithOnlyARootCredentialSaysHowToJoin(t *testing.T) {
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) { return 200, `{"ok":true}` })
	state := filepath.Join(t.TempDir(), "state")
	api, web := t.TempDir(), t.TempDir()
	for _, proj := range []string{api, web} {
		gitInit(t, proj)
	}
	if err := os.MkdirAll(filepath.Join(state, "huddle"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(state, "huddle", "servers.json"),
		`{"`+jsonSpelled(filepath.Join(api, ".agents", "huddle"))+`":{"url":"`+fake.server.URL+`","channel":"team","project":"`+jsonSpelled(api)+`"}}`)
	W := setupWorld(t, web, map[string]string{
		"XDG_STATE_HOME": state,
		"HUDDLE_SESSION": "s-web",
	})
	sessions := filepath.Join(userHome(t, W), "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sessions, "s-web.json"),
		`{"url":"`+fake.server.URL+`","credential":"hcred_root","root":true}`)
	code, out, errOut := runSetup(t, W)
	if code != 0 || errOut != "" {
		t.Fatalf("web setup: %d %q %q", code, out, errOut)
	}
	if strings.Contains(out, "This project is already in the Huddle") {
		t.Errorf("a credential with no channel is not membership; setup said %q", out)
	}
	if !strings.Contains(out, "A Huddle already runs for "+api+" at "+fake.server.URL+" (channel team)") {
		t.Errorf("web setup said %q", out)
	}
}

// The session that started the server sat in this same project through another spelling of its
// path (a symlinked /var): setup knows the server is its own and prints the way in.
func TestSetupKnowsItsOwnServerThroughAnySpellingOfOnePath(t *testing.T) {
	proj := t.TempDir()
	gitInit(t, proj)
	state := filepath.Join(t.TempDir(), "state")
	user := t.TempDir()
	// the hook's world spells the project the resolved way, setup's world the plain way; one
	// home serves both, so the port file and the creator's credential carry over
	hookW := setupWorld(t, proj, map[string]string{
		"HOME":               user,
		"HUDDLE_SESSION":     "s-hook",
		"XDG_STATE_HOME":     state,
		"CLAUDE_PROJECT_DIR": realDir(t, proj),
	})
	pinFreePort(t, userHome(t, hookW))
	t.Cleanup(func() { stopHome(t, hookW, userHome(t, hookW)) })
	if code, out := runServerCommand(t, hookW, "up"); code != 0 || !strings.Contains(out, "started") {
		t.Fatalf("the hook's start: %d %s", code, out)
	}
	W := setupWorld(t, proj, map[string]string{
		"HOME":           user,
		"HUDDLE_SESSION": "s-1",
		"XDG_STATE_HOME": state,
	})
	code, out, errOut := runSetup(t, W, "--start")
	if code != 0 {
		t.Fatalf("setup --start: %d %q %q", code, out, errOut)
	}
	if strings.Contains(out, "already in the Huddle") || strings.Contains(out, "already runs for") {
		t.Fatalf("setup did not know its own server: %q", out)
	}
	if !regexp.MustCompile(`(?m)^join:     /huddle:join 127\.0\.0\.1:\d+ --token [a-z0-9]{6}\.[a-z0-9]{16}`).MatchString(out) {
		t.Errorf("the join line %q", out)
	}
	if _, err := os.Stat(filepath.Join(proj, ".agents", "huddle", "huddle.json")); err != nil {
		t.Errorf("the project's huddle.json: %v", err)
	}
}

// joinEcho answers a join the way the server does: the token's channel and name echoed back.
func joinEcho(call fakeCall) (int, string) {
	name := regexp.MustCompile(`"name":"([^"]*)"`).FindStringSubmatch(call.Body)
	channel := regexp.MustCompile(`"channel":"([^"]*)"`).FindStringSubmatch(call.Body)
	n, c := "", ""
	if name != nil {
		n = name[1]
	}
	if channel != nil {
		c = channel[1]
	}
	return 200, `{"name":` + quoteJSON(n) + `,"channel":` + quoteJSON(c) + `,"credential":"hcred_member"}`
}

// A hub whose registry entry names no channel (a plain `huddle up` started it): setup in
// another project puts that project in its own channel on that hub, named the way a first setup
// names one, and saves a credential that names it.
func TestSetupAHubWithNoChannelPutsTheProjectInItsOwnChannel(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	base := t.TempDir()
	api, web := filepath.Join(base, "api-proj"), filepath.Join(base, "web")
	for _, proj := range []string{api, web} {
		if err := os.MkdirAll(proj, 0o755); err != nil {
			t.Fatal(err)
		}
		gitInit(t, proj)
	}
	var minted, joined string
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		switch {
		case call.Method == "POST" && call.Path == "/api/tokens":
			minted = call.Body
			return 200, `{"token":"abc123.0123456789abcdef","id":"abc123","expires":null}`
		case call.Method == "POST" && call.Path == "/api/join":
			joined = call.Body
			return joinEcho(call)
		case strings.HasPrefix(call.Path, "/api/c/") && strings.HasSuffix(call.Path, "/op/join"):
			return okJSON("you are web in web")
		}
		return 200, `{}`
	})
	if err := os.MkdirAll(filepath.Join(state, "huddle"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(state, "huddle", "servers.json"),
		`{"`+jsonSpelled(filepath.Join(api, ".agents", "huddle"))+`":{"url":"`+fake.server.URL+`","channel":"","project":"`+jsonSpelled(api)+`"}}`)
	W := setupWorld(t, web, map[string]string{
		"XDG_STATE_HOME": state,
		"HUDDLE_SESSION": "s-web",
	})
	// the root key `huddle up` saved its creator, under the api project's key
	sessions := filepath.Join(userHome(t, W), "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(api)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sessions, "project-"+hashKey(resolved)+".json"),
		`{"url":"`+fake.server.URL+`","credential":"hcred_root","root":true}`)
	code, out, errOut := runSetup(t, W)
	if code != 0 || errOut != "" {
		t.Fatalf("web setup: %d %q %q", code, out, errOut)
	}
	if !strings.Contains(minted, `"channel":"web"`) {
		t.Errorf("the mint body %q, want the project's own channel", minted)
	}
	if !strings.Contains(joined, `"channel":"web"`) {
		t.Errorf("the join body %q, want the project's own channel", joined)
	}
	joinedOp := false
	for _, call := range fake.asked() {
		if call.Path == "/api/c/web/op/join" {
			joinedOp = true
		}
	}
	if !joinedOp {
		t.Errorf("no join op on the project's channel: %v", fake.asked())
	}
	if !strings.Contains(out, "Joined the Huddle that already runs for "+api+": "+fake.server.URL+", channel web, as web.") {
		t.Errorf("web setup said %q", out)
	}
	cred := readFile(t, filepath.Join(sessions, "s-web.json"))
	if !strings.Contains(cred, `"channel":"web"`) || !strings.Contains(cred, "hcred_member") {
		t.Errorf("the saved credential %q", cred)
	}
}

// A join that answers no channel saves nothing and claims nothing: no membership, no "Joined".
func TestSetupAJoinWithNoChannelSavesNothingAndSaysSo(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	base := t.TempDir()
	api, web := filepath.Join(base, "api-proj"), filepath.Join(base, "web")
	for _, proj := range []string{api, web} {
		if err := os.MkdirAll(proj, 0o755); err != nil {
			t.Fatal(err)
		}
		gitInit(t, proj)
	}
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		switch {
		case call.Method == "POST" && call.Path == "/api/tokens":
			return 200, `{"token":"abc123.0123456789abcdef","id":"abc123","expires":null}`
		case call.Method == "POST" && call.Path == "/api/join":
			return 200, `{"name":"web","channel":null,"credential":"hcred_member"}`
		}
		return 200, `{}`
	})
	if err := os.MkdirAll(filepath.Join(state, "huddle"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(state, "huddle", "servers.json"),
		`{"`+jsonSpelled(filepath.Join(api, ".agents", "huddle"))+`":{"url":"`+fake.server.URL+`","channel":"","project":"`+jsonSpelled(api)+`"}}`)
	W := setupWorld(t, web, map[string]string{
		"XDG_STATE_HOME": state,
		"HUDDLE_SESSION": "s-web",
	})
	sessions := filepath.Join(userHome(t, W), "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(api)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sessions, "project-"+hashKey(resolved)+".json"),
		`{"url":"`+fake.server.URL+`","credential":"hcred_root","root":true}`)
	code, out, errOut := runSetup(t, W)
	if code != 5 {
		t.Fatalf("web setup = %d %q %q, want 5", code, out, errOut)
	}
	if !strings.Contains(out, "A Huddle already runs for "+api+" at "+fake.server.URL+", but joining it gave no channel. /huddle:setup --new starts a separate one.") {
		t.Errorf("web setup said %q", out)
	}
	if strings.Contains(out, "Joined") {
		t.Errorf("a join with no channel claimed membership: %q", out)
	}
	entries, err := os.ReadDir(sessions)
	if err != nil || len(entries) != 1 {
		t.Errorf("the sessions dir holds %v (%v), want only the root key", entries, err)
	}
	for _, call := range fake.asked() {
		if strings.HasPrefix(call.Path, "/api/c/") {
			t.Errorf("a channel-less join still ran %s", call.Path)
		}
	}
}

// An entry that names a channel keeps it: the mint and the join carry that channel, as today.
func TestSetupAnEntryWithAChannelKeepsIt(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	base := t.TempDir()
	api, web := filepath.Join(base, "api-proj"), filepath.Join(base, "web")
	for _, proj := range []string{api, web} {
		if err := os.MkdirAll(proj, 0o755); err != nil {
			t.Fatal(err)
		}
		gitInit(t, proj)
	}
	var minted, joined string
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		switch {
		case call.Method == "POST" && call.Path == "/api/tokens":
			minted = call.Body
			return 200, `{"token":"abc123.0123456789abcdef","id":"abc123","expires":null}`
		case call.Method == "POST" && call.Path == "/api/join":
			joined = call.Body
			return joinEcho(call)
		case strings.HasPrefix(call.Path, "/api/c/") && strings.HasSuffix(call.Path, "/op/join"):
			return okJSON("you are web in team")
		}
		return 200, `{}`
	})
	if err := os.MkdirAll(filepath.Join(state, "huddle"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(state, "huddle", "servers.json"),
		`{"`+jsonSpelled(filepath.Join(api, ".agents", "huddle"))+`":{"url":"`+fake.server.URL+`","channel":"team","project":"`+jsonSpelled(api)+`"}}`)
	W := setupWorld(t, web, map[string]string{
		"XDG_STATE_HOME": state,
		"HUDDLE_SESSION": "s-web",
	})
	sessions := filepath.Join(userHome(t, W), "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(api)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sessions, "project-"+hashKey(resolved)+".json"),
		`{"url":"`+fake.server.URL+`","credential":"hcred_root","root":true}`)
	code, out, errOut := runSetup(t, W)
	if code != 0 || errOut != "" {
		t.Fatalf("web setup: %d %q %q", code, out, errOut)
	}
	if !strings.Contains(minted, `"channel":"team"`) {
		t.Errorf("the mint body %q, want the entry's channel", minted)
	}
	if !strings.Contains(joined, `"channel":"team"`) {
		t.Errorf("the join body %q, want the entry's channel", joined)
	}
	if !strings.Contains(out, "Joined the Huddle that already runs for "+api+": "+fake.server.URL+", channel team, as web.") {
		t.Errorf("web setup said %q", out)
	}
}

// The registry's keys and projects are paths, and a path may carry the backslashes JSON
// escapes: setup reads the entry whatever it spelled.
func TestSetupReadsARegistryKeyedInAnyPathsSpelling(t *testing.T) {
	for _, tc := range []struct {
		name    string
		home    string
		project string
	}{
		{"a plain path", "/tmp/proj/.agents/huddle", "/tmp/proj"},
		{"a path with backslashes", `C:\Users\runner\proj\.agents\huddle`, `C:\Users\runner\proj`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "state")
			if err := os.MkdirAll(filepath.Join(state, "huddle"), 0o700); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(state, "huddle", "servers.json"),
				`{"`+jsonSpelled(tc.home)+`":{"url":"http://127.0.0.1:9","channel":"team","project":"`+jsonSpelled(tc.project)+`"}}`)
			W := setupWorld(t, t.TempDir(), map[string]string{"XDG_STATE_HOME": state})
			notes := serversInOrder(context.Background(), W)
			if len(notes) != 1 || notes[0].home != tc.home || notes[0].entry.Project != tc.project {
				t.Errorf("registry = %+v", notes)
			}
		})
	}
}
