package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
)

// who is what identity() answers in dir with the given variables: the identity plus its home.
func who(t *testing.T, dir string, env map[string]string) (Identity, string) {
	t.Helper()
	w := world(t, dir, env)
	id := Identify(context.Background(), w, "")
	return id, HomeOf(context.Background(), w)
}

func TestEnvWinsElseTheNearestAgentsHuddleHuddleJSONUpFromTheCwdElseDefaults(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{filepath.Join(root, ".agents", "huddle"), filepath.Join(root, "a", "b")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// .agents/huddle/huddle.json is the project's settings file
	writeFile(t, filepath.Join(root, ".agents", "huddle", "huddle.json"),
		`{"channel": "shop", "as": "api", "role": "fixes", "url": "http://127.0.0.1:9000/", "push": ["ask", "task.ready"], "wait": 600}`)
	cwd := realDir(t, filepath.Join(root, "a", "b"))
	id, home := who(t, cwd, nil)
	want := Identity{
		URL: "http://127.0.0.1:9000", Source: "config", Channel: "shop", As: "api", Role: "fixes",
		Push: []string{"ask", "task.ready"}, Listen: []string{}, Wait: 600, Context: "auto",
		Autostart: true, Detail: "digest", File: filepath.Join(realDir(t, root), ".agents", "huddle", "huddle.json"),
	}
	if !reflect.DeepEqual(id, want) {
		t.Errorf("identity() = %+v\nwant %+v", id, want)
	}
	// the home is the user's, whatever project the session runs in
	if !strings.HasSuffix(home, filepath.Join(".config", "huddle")) {
		t.Errorf("home = %q, want the user's ~/.config/huddle", home)
	}
	if _, home := who(t, cwd, map[string]string{"HUDDLE_HOME": "/elsewhere"}); home != "/elsewhere" {
		t.Errorf("HUDDLE_HOME: home = %q, want /elsewhere", home)
	}
	// XDG_CONFIG_HOME names the config root the home sits in
	if _, home := who(t, cwd, map[string]string{"XDG_CONFIG_HOME": "/xdg"}); home != filepath.FromSlash("/xdg/huddle") {
		t.Errorf("XDG_CONFIG_HOME: home = %q, want /xdg/huddle", home)
	}
	// a settings file's home key is the old per-repo world: ignored (the import reads it, nothing else)
	shared := t.TempDir()
	if err := os.MkdirAll(filepath.Join(shared, ".agents", "huddle"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(shared, ".agents", "huddle", "huddle.json"), `{"channel": "c", "as": "x", "home": "~/one/place"}`)
	w := world(t, realDir(t, shared), nil)
	if home := HomeOf(context.Background(), w); home != filepath.Join(w.Getenv("HOME"), ".config", "huddle") {
		t.Errorf("home = %q, want %q", home, filepath.Join(w.Getenv("HOME"), ".config", "huddle"))
	}
	// environment wins over the file; a list or a comma string both parse
	e, _ := who(t, cwd, map[string]string{"HUDDLE_AS": "api.explore", "HUDDLE_PUSH": "off", "HUDDLE_WAIT": "60"})
	if e.Channel != "shop" || e.As != "api.explore" || !reflect.DeepEqual(e.Push, []string{"off"}) || e.Wait != 60 {
		t.Errorf("env override = %+v", e)
	}
	// an empty variable counts as unset
	blank, _ := who(t, cwd, map[string]string{"HUDDLE_URL": "", "HUDDLE_AS": "", "HUDDLE_WAIT": ""})
	if blank.As != "api" || blank.URL != "http://127.0.0.1:9000" || blank.Wait != 600 {
		t.Errorf("empty vars = %+v", blank)
	}
	// a directory holding only .agents/.huddle.json finds no config there
	old := t.TempDir()
	if err := os.MkdirAll(filepath.Join(old, ".agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(old, ".agents", ".huddle.json"), `{"channel": "shop", "as": "api"}`)
	if id, _ := who(t, realDir(t, old), nil); id.Channel != "" || id.As != "" || id.File != "" {
		t.Errorf("a stray .agents/.huddle.json read as a config: %+v", id)
	}
	// nothing anywhere: no url yet, no fixed port, the defaults
	none, noneHome := who(t, realDir(t, t.TempDir()), nil)
	if none.Channel != "" || none.As != "" || none.URL != "" || none.Source != "none" || none.Wait != 1500 {
		t.Errorf("no config = %+v", none)
	}
	if !reflect.DeepEqual(none.Push, []string{"task.ready", "turn.pass", "ask", "msg", "control.*"}) {
		t.Errorf("default push = %v", none.Push)
	}
	if _, err := os.Stat(filepath.Join(noneHome, "config.toml")); !os.IsNotExist(err) {
		t.Errorf("identity wrote a config at %s", noneHome)
	}
}

// TestAutostartIsOnUnlessTurnedOff pins the default: autostart is on unless a setting turns it
// off, so a fresh install with no settings starts its server from the first session.
func TestAutostartIsOnUnlessTurnedOff(t *testing.T) {
	for value, want := range map[string]bool{
		"": true, "1": true, "true": true, "TRUE": true,
		"0": false, "false": false, "False": false, "off": false, "no": false,
	} {
		if got := autostartOf(value); got != want {
			t.Errorf("autostartOf(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestAGitWorktreeOutsideItsRepoSharesTheMainCheckoutsSettingsAndFiles(t *testing.T) {
	base := t.TempDir()
	repo, wt := filepath.Join(base, "repo"), filepath.Join(base, "elsewhere", "wt")
	if err := os.MkdirAll(filepath.Join(repo, ".agents", "huddle"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitEnv := isoenv.Environ(t, base,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null")
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = gitEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("commit", "-q", "--allow-empty", "--no-gpg-sign", "-m", "x")
	run("worktree", "add", "-q", wt)
	writeFile(t, filepath.Join(repo, ".agents", "huddle", "huddle.json"), `{"channel": "shop", "as": "shop"}`)
	shared, home := who(t, realDir(t, wt), nil)
	if shared.Channel != "shop" || shared.As != "shop" {
		t.Errorf("worktree identity = %+v", shared)
	}
	// the settings come from the main checkout; the home stays this user's, one per user
	if !strings.HasSuffix(home, filepath.Join(".config", "huddle")) {
		t.Errorf("home = %q, want the user's ~/.config/huddle", home)
	}
	// the worktree's own file, when it has one, wins
	if err := os.MkdirAll(filepath.Join(wt, ".agents", "huddle"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(wt, ".agents", "huddle", "huddle.json"), `{"channel": "other", "as": "x"}`)
	own, _ := who(t, realDir(t, wt), nil)
	if own.Channel != "other" || own.As != "x" {
		t.Errorf("own file identity = %+v", own)
	}
}

// TestMainCheckoutReadsTheGitdirMarker pins the pure half of the worktree walk: the marker
// names the main checkout, and a submodule, a pointer without the marker, or no line at all
// leaves the checkout itself.
func TestMainCheckoutReadsTheGitdirMarker(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    string
	}{
		{"the marker names the main checkout", "gitdir: /home/a/repo/.git/worktrees/w\n", "/home/a/repo"},
		{"a submodule names its own gitdir", "gitdir: /home/a/repo/.git/modules/sub\n", "d"},
		{"no marker", "gitdir: /somewhere/else\n", "d"},
		{"no line at all", "nothing here\n", "d"},
		{"a marker with nothing before it", "gitdir: /.git/worktrees/w\n", "d"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mainCheckoutOf("d", tc.content); got != tc.want {
				t.Errorf("mainCheckoutOf(%q) = %q, want %q", tc.content, got, tc.want)
			}
		})
	}
}

func TestTheJoinContextHuddleContextOrTheFilesContextWinsAutoMapsTheSessionStartSource(t *testing.T) {
	var got []string
	for _, source := range []string{"startup", "resume", "clear", "compact", ""} {
		got = append(got, ContextFor(source, "auto"))
	}
	if want := []string{"", "sync", "fresh", "fresh", ""}; !reflect.DeepEqual(got, want) {
		t.Errorf("contextFor(auto) = %v, want %v", got, want)
	}
	if ContextFor("clear", "sync") != "sync" || ContextFor("resume", "fresh") != "fresh" {
		t.Errorf("a setting of sync or fresh wins")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".agents", "huddle"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, ".agents", "huddle", "huddle.json"), `{"channel": "shop", "as": "api", "context": "fresh"}`)
	cwd := realDir(t, root)
	if id, _ := who(t, cwd, nil); id.Context != "fresh" {
		t.Errorf("the file's context: %+v", id)
	}
	if id, _ := who(t, cwd, map[string]string{"HUDDLE_CONTEXT": "sync"}); id.Context != "sync" {
		t.Errorf("HUDDLE_CONTEXT: %+v", id)
	}
	if id, _ := who(t, realDir(t, t.TempDir()), nil); id.Context != "auto" {
		t.Errorf("default context: %+v", id)
	}
}

// TestSlugIsANameValidForAChannelAndASession pins slug: a folder's lowercased name, a letter
// first, letters digits and - only, at most 32 characters, the fallback when nothing is left.
func TestSlugIsANameValidForAChannelAndASession(t *testing.T) {
	cases := []struct{ dir, or, want string }{
		{"/work/shop-api", "session", "shop-api"},
		{"/work/2-shop", "session", "shop"},
		{"/work/Shop_API", "session", "shop-api"},
		{"/work/shop.api", "session", "shop-api"},
		{"/work/a-very-long-folder-name-over-32-chars", "fallback", "a-very-long-folder-name-over-32"},
		{"/work/-dash-", "session", "dash"},
		{"/work/123", "fallback", "fallback"},
		{"/", "fallback", "fallback"},
	}
	for _, c := range cases {
		if got := Slug(c.dir, c.or); got != c.want {
			t.Errorf("Slug(%q, %q) = %q, want %q", c.dir, c.or, got, c.want)
		}
	}
}

// TestSavePortKeepsTheFilesOtherKeysAndTheirOrder pins the config's rewrite: the port goes in,
// port_auto with it or out, and everything else the file holds stays.
func TestSavePortKeepsTheFilesOtherKeysAndTheirOrder(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	file := filepath.Join(root, "config.toml")
	writeFile(t, file, "favourite = \"tea\"\n\n[notify]\npush = false\n")
	if got := SavePort(ctx, world(t, root, nil), 45678, true, root); got != file {
		t.Fatalf("SavePort = %q, want %q", got, file)
	}
	cfg, err := home.LoadConfig(home.Home{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 45678 || !cfg.PortAuto {
		t.Errorf("saved = %+v", cfg)
	}
	if text := readFile(t, file); !strings.Contains(text, "favourite") || !strings.Contains(text, "push = false") {
		t.Errorf("the rewrite lost the file's own keys:\n%s", text)
	}
	SavePort(ctx, world(t, root, nil), 45679, false, root)
	cfg, err = home.LoadConfig(home.Home{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 45679 || cfg.PortAuto {
		t.Errorf("after a chosen save = %+v", cfg)
	}
	if choice, ok := SavedPort(root); !ok || choice.Port != 45679 || choice.Auto {
		t.Errorf("SavedPort = %+v %v", choice, ok)
	}
}

// TestSavedPortRejectsEverythingThatIsNotALivePortNumber pins what counts as a saved port.
func TestSavedPortRejectsEverythingThatIsNotALivePortNumber(t *testing.T) {
	for name, text := range map[string]string{
		"no file":     "",
		"not TOML":    "nope [",
		"no port":     "machine = \"laptop\"\n",
		"zero":        "port = 0\n",
		"negative":    "port = -1\n",
		"too high":    "port = 65536\n",
		"a fraction":  "port = 8080.5\n",
		"a string":    "port = \"8808\"\n",
		"a torn file": "port = ",
	} {
		root := t.TempDir()
		if text != "" {
			writeFile(t, filepath.Join(root, "config.toml"), text)
		}
		if _, ok := SavedPort(root); ok {
			t.Errorf("%s: SavedPort accepted %q", name, text)
		}
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "config.toml"), "port = 8808\nport_auto = true\n")
	choice, ok := SavedPort(root)
	if !ok || choice.Port != 8808 || !choice.Auto {
		t.Errorf("saved port = %+v %v", choice, ok)
	}
}

// TestRunningPidNamesOnlyAHuddleServer pins the process check: a pid file may be stale, so the
// process it names must be this binary running the serve entry.
func TestRunningPidNamesOnlyAHuddleServer(t *testing.T) {
	w := &World{Proc: Proc{Command: func(pid int) (string, bool) {
		switch pid {
		case 1:
			return "/some/huddle serve", true
		case 2:
			return "/some/huddle up", true
		case 3:
			return "grep huddle serve", true
		default:
			return "", false
		}
	}}, Dir: t.TempDir()}
	root := w.Dir
	if err := os.MkdirAll(filepath.Join(root, "run"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "run", "huddle.pid"), "1")
	if pid := RunningPid(context.Background(), w, root); pid != 1 {
		t.Errorf("a huddle server: RunningPid = %d, want 1", pid)
	}
	for _, pidText := range []string{"2", "3", "4", "-1", "1.5", "nope", ""} {
		writeFile(t, filepath.Join(root, "run", "huddle.pid"), pidText)
		if pid := RunningPid(context.Background(), w, root); pid != 0 {
			t.Errorf("pid %q: RunningPid = %d, want 0", pidText, pid)
		}
	}
	if err := os.Remove(filepath.Join(root, "run", "huddle.pid")); err != nil {
		t.Fatal(err)
	}
	if pid := RunningPid(context.Background(), w, root); pid != 0 {
		t.Errorf("no pid file: RunningPid = %d, want 0", pid)
	}
	// a pid file left at the home's root by an older layout names nothing
	writeFile(t, filepath.Join(root, "huddle.pid"), "1")
	if pid := RunningPid(context.Background(), w, root); pid != 0 {
		t.Errorf("a root-level pid file: RunningPid = %d, want 0", pid)
	}
}
