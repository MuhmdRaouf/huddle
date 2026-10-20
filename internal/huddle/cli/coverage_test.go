package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
)

// TestWorldDefaultsAreTheProcessOwn pins the zero World: it reads the process's variables,
// working directory, clock and executable, so a command without a world still runs.
func TestWorldDefaultsAreTheProcessOwn(t *testing.T) {
	var w *World
	if w.getenv("PATH") != os.Getenv("PATH") {
		t.Error("getenv")
	}
	if len(w.environ()) != len(isoenv.Inherited()) {
		t.Error("environ")
	}
	if w.dir() == "" {
		t.Error("dir")
	}
	if w.now().Before(time.Now().Add(-time.Minute)) {
		t.Error("now")
	}
	if base := filepath.Base(w.bin()); base != "cli.test" && !strings.Contains(w.bin(), "huddle") {
		t.Errorf("bin = %q", w.bin())
	}
	empty := &World{}
	if empty.getenv("PATH") != os.Getenv("PATH") || empty.dir() == "" || len(empty.environ()) == 0 {
		t.Error("the zero World")
	}
	// a variable overridden for one call, the rest untouched
	over := empty.WithVar("HUDDLE_PORT", "4321")
	if over.getenv("HUDDLE_PORT") != "4321" || over.getenv("PATH") != os.Getenv("PATH") {
		t.Error("WithVar")
	}
}

// TestSessionsDirWalksTheHomes pins where session credentials live: the variable, then the
// huddle home's sessions directory (HUDDLE_HOME, XDG_CONFIG_HOME, the user's ~/.config).
func TestSessionsDirWalksTheHomes(t *testing.T) {
	w := world(t, t.TempDir(), map[string]string{"HUDDLE_SESSIONS_DIR": "/custom/sessions", "XDG_CONFIG_HOME": "/xdg"})
	if got := SessionsDir(w); got != "/custom/sessions" {
		t.Errorf("the variable: %q", got)
	}
	own := world(t, t.TempDir(), map[string]string{"HUDDLE_HOME": "/hh"})
	if got := SessionsDir(own); got != filepath.FromSlash("/hh/sessions") {
		t.Errorf("the home's own directory: %q", got)
	}
	xdg := world(t, t.TempDir(), map[string]string{"XDG_CONFIG_HOME": "/xdg"})
	if got := SessionsDir(xdg); got != filepath.FromSlash("/xdg/huddle/sessions") {
		t.Errorf("the XDG config home: %q", got)
	}
	home := world(t, t.TempDir(), nil) // nothing set: the user's ~/.config/huddle
	if got := SessionsDir(home); got != filepath.Join(home.Getenv("HOME"), ".config", "huddle", "sessions") {
		t.Errorf("the user's config: %q", got)
	}
}

// TestCredFilesThatCannotBeReadOrWritten pins the quiet paths: a corrupt file is no credential,
// and a sessions dir that cannot be made surfaces as an error, never a panic.
func TestCredFilesThatCannotBeReadOrWritten(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	writeFile(t, blocker, "a file, not a directory")
	w := world(t, t.TempDir(), map[string]string{"HUDDLE_SESSIONS_DIR": filepath.Join(blocker, "sessions")})
	if err := SaveCred(ctx, w, Cred{URL: "http://127.0.0.1:1", Credential: "c"}, ""); err == nil {
		t.Error("SaveCred under a file succeeded")
	}
	sessions := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sessions, "terminal.json"), "not json")
	writeFile(t, filepath.Join(sessions, "empty.json"), "")
	writeFile(t, filepath.Join(sessions, "half.json"), `{"url": "http://127.0.0.1:1"}`)
	w2 := world(t, t.TempDir(), map[string]string{"HUDDLE_SESSIONS_DIR": sessions})
	if c := LoadCred(ctx, w2, "", ""); c != nil {
		t.Errorf("a corrupt file = %+v", c)
	}
	if got := TokenFor(ctx, w2, "http://127.0.0.1:1", ""); got != "" {
		t.Errorf("TokenFor over a corrupt file = %q", got)
	}
}

// TestIdentifyFillsFromTheCredential pins what a joined credential fills in: a member's bound
// channel and name win over the project file's, a root one acts as anyone, and the url counts
// as the identity's when nothing else names one.
func TestIdentifyFillsFromTheCredential(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	if err := os.MkdirAll(filepath.Join(state, ".agents", "huddle"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(state, ".agents", "huddle", "huddle.json"),
		`{"channel": "file-c", "as": "file-a", "listen": "other", "listen_detail": "all", "autostart": "no", "wait": 900}`)
	sessions := func() map[string]string {
		return map[string]string{
			"HUDDLE_SESSIONS_DIR":    filepath.Join(t.TempDir(), "sessions"),
			"HUDDLE_NO_PROJECT_CRED": "1",
		}
	}
	// a member's credential: its channel and name win, its url stands in when none is set
	member := world(t, state, sessions())
	if err := SaveCred(ctx, member, Cred{URL: "http://127.0.0.1:4321", Channel: "cred-c", As: "cred-a", Credential: "c"}, ""); err != nil {
		t.Fatal(err)
	}
	id := Identify(ctx, member, "")
	if id.Channel != "cred-c" || id.As != "cred-a" || id.URL != "http://127.0.0.1:4321" || id.Source != "credential" {
		t.Errorf("a member: %+v", id)
	}
	if id.Detail != "all" || id.Autostart || id.Wait != 900 || len(id.Listen) != 1 || id.Listen[0] != "other" {
		t.Errorf("the file's own settings: %+v", id)
	}
	// a root credential acts as anyone: the file's channel and name stand
	root := world(t, state, sessions())
	if err := SaveCred(ctx, root, Cred{URL: "http://127.0.0.1:4321", Credential: "c", Root: true}, ""); err != nil {
		t.Fatal(err)
	}
	id = Identify(ctx, root, "")
	if id.Channel != "file-c" || id.As != "file-a" {
		t.Errorf("a root: %+v", id)
	}
	// the environment still wins over both
	env := sessions()
	env["HUDDLE_CHANNEL"] = "env-c"
	env["HUDDLE_AS"] = "env-a"
	env["HUDDLE_URL"] = "http://127.0.0.1:8"
	env["HUDDLE_LISTEN_DETAIL"] = "nope"
	env["HUDDLE_AUTOSTART"] = "1"
	id = Identify(ctx, world(t, state, env), "")
	if id.Channel != "env-c" || id.As != "env-a" || id.Source != "env" || id.Detail != "digest" || !id.Autostart {
		t.Errorf("the environment: %+v", id)
	}
}

// TestSavePortKeepsNestedValues pins the config's rewrite over a file that carries lists and
// nested tables: the port changes, everything else stays.
func TestSavePortKeepsNestedValues(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	file := filepath.Join(root, "config.toml")
	writeFile(t, file, "push = [\"ask\", \"msg\"]\nport = 1\n\n[nested]\nb = 1\na = [true, false]\n")
	SavePort(ctx, world(t, root, nil), 2, false, root)
	cfg, err := home.LoadConfig(home.Home{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 2 || cfg.PortAuto {
		t.Errorf("after the save = %+v", cfg)
	}
	text := readFile(t, file)
	for _, want := range []string{`push = ["ask", "msg"]`, "[nested]", "b = 1", "a = [true, false]"} {
		if !strings.Contains(text, want) {
			t.Errorf("the rewrite lost %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "port = 1") {
		t.Errorf("the old port stayed:\n%s", text)
	}
}

// TestFeedIgnoresAnswersThatAreNotATimeline pins the feed's quiet handling of a server that
// answers something else: no events, no error, one empty answer.
func TestFeedIgnoresAnswersThatAreNotATimeline(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/timeline") {
			_, _ = w.Write([]byte("not json at all"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	w := world(t, t.TempDir(), map[string]string{"HUDDLE_TOKEN": "root"})
	id := Identity{URL: srv.URL, Channel: "c", As: "a", Push: []string{}, Listen: []string{}}
	if got := Feed(ctx, w, id, filepath.Join(t.TempDir(), "home"), "s-1", FeedOptions{MS: 500}); got != "" {
		t.Errorf("a garbage answer = %q", got)
	}
	// a home whose seen directory cannot be made: the answer still comes back
	blocker := filepath.Join(t.TempDir(), "blocker")
	writeFile(t, blocker, "a file")
	if got := Feed(ctx, w, id, blocker, "s-1", FeedOptions{MS: 500}); got != "" {
		t.Errorf("a blocked home = %q", got)
	}
}

// TestFeedCountsTwoKnowledgeEntries pins the plural of the count line's second kind.
func TestFeedCountsTwoKnowledgeEntries(t *testing.T) {
	ctx := context.Background()
	url, set := feedServer(t)
	set("c", []fakeEvent{{Seq: 5, From: "a", Topic: "kb.added", Msg: "one"}, {Seq: 6, From: "a", Topic: "kb.added", Msg: "two"}})
	w := world(t, t.TempDir(), map[string]string{"HUDDLE_TOKEN": "root"})
	id := Identity{URL: url, Channel: "c", As: "me", Push: []string{}, Listen: []string{}}
	home := t.TempDir()
	Feed(ctx, w, id, home, "s-1", FeedOptions{MS: 500}) // marks the newest
	set("c", []fakeEvent{
		{Seq: 5, From: "a", Topic: "kb.added", Msg: "one"}, {Seq: 6, From: "a", Topic: "kb.added", Msg: "two"},
		{Seq: 7, From: "a", Topic: "kb.added", Msg: "three"}, {Seq: 8, From: "a", Topic: "kb.added", Msg: "four"},
	})
	got := Feed(ctx, w, id, home, "s-1", FeedOptions{MS: 500})
	if !strings.Contains(got, "+2 knowledge entries (events after=6 for them)") {
		t.Errorf("the count line:\n%s", got)
	}
}

// TestTouchOfWalksOddGitEntries pins the repo walk over git entries that name nothing usable:
// a .git file with no gitdir line, a gitdir without a commondir, and a cwd that does not exist.
func TestTouchOfWalksOddGitEntries(t *testing.T) {
	top := realDir(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(top, "w", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	// no gitdir line: the checkout itself is the repo
	writeFile(t, filepath.Join(top, "w", ".git"), "nothing here\n")
	input := TouchInput{ToolName: "Edit"}
	input.ToolInput.FilePath = filepath.Join(top, "w", "src", "a.ts")
	if got := TouchOf(input); got == nil || got.Repo != filepath.Join(top, "w") || got.Path != "src/a.ts" {
		t.Errorf("no gitdir line: %+v", got)
	}
	// a gitdir with no commondir beside it: the gitdir's own directory
	other := realDir(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(other, "w", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(other, "bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(other, "w", ".git"), "gitdir: "+filepath.Join(other, "bare")+"\n")
	input2 := TouchInput{ToolName: "Edit"}
	input2.ToolInput.FilePath = filepath.Join(other, "w", "src", "a.ts")
	if got := TouchOf(input2); got == nil || got.Repo != realDir(t, filepath.Join(other, "bare")) {
		t.Errorf("no commondir: %+v", got)
	}
	// a cwd that does not exist: the path still resolves against it
	missing := filepath.Join(t.TempDir(), "not-there")
	gone := TouchInput{ToolName: "Edit", Cwd: missing}
	gone.ToolInput.FilePath = filepath.Join(missing, "a.ts")
	if got := TouchOf(gone); got == nil || got.Path != "a.ts" || got.Repo != gone.Cwd {
		t.Errorf("a missing cwd: %+v", got)
	}
}

// TestServeCommandRefusesAnUnknownName pins the branch's own usage error.
func TestServeCommandRefusesAnUnknownName(t *testing.T) {
	var out strings.Builder
	if code := ServerCommand(context.Background(), world(t, t.TempDir(), nil), "sideways", "", nil, &out, &out); code != 2 || !strings.Contains(out.String(), "not a server command") {
		t.Errorf("ServerCommand(sideways) = (%d, %q)", code, out.String())
	}
}

// TestJoinLineRefusedAndDataDir pins two small answers: a join line against a dead server, and
// the data dir override.
func TestJoinLineRefusedAndDataDir(t *testing.T) {
	ctx := context.Background()
	w := world(t, t.TempDir(), nil)
	if line := JoinLine(ctx, w, "http://127.0.0.1:1", InviteOptions{}, ""); line.OK || line.Error == "" {
		t.Errorf("JoinLine unreachable = %+v", line)
	}
	over := world(t, t.TempDir(), map[string]string{"HUDDLE_DATA": "/somewhere/else"})
	if got := DataDir(ctx, over); got != "/somewhere/else" {
		t.Errorf("DataDir = %q", got)
	}
	if got := Healthy(ctx, "http://127.0.0.1:99999999", 10); got {
		t.Error("an impossible url is not healthy")
	}
}

// TestRegistryNotesAStartInPlaceAndAfter pins the registry's order: a home noted again keeps
// its place, a new home goes last.
func TestRegistryNotesAStartInPlaceAndAfter(t *testing.T) {
	ctx := context.Background()
	w := world(t, t.TempDir(), nil)
	if err := os.MkdirAll(filepath.Dir(RegistryFile(w)), 0o700); err != nil {
		t.Fatal(err)
	}
	noteServer(ctx, w, "/one", ServerEntry{URL: "http://127.0.0.1:1", PID: 1, At: "2026-10-15T12:00:00.000Z"})
	noteServer(ctx, w, "/two", ServerEntry{URL: "http://127.0.0.1:2", PID: 2, At: "2026-10-15T12:00:01.000Z"})
	noteServer(ctx, w, "/one", ServerEntry{URL: "http://127.0.0.1:3", PID: 3, At: "2026-10-15T12:00:02.000Z"})
	if got, want := readFile(t, RegistryFile(w)),
		`{
  "/one": {
    "url": "http://127.0.0.1:3",
    "pid": 3,
    "at": "2026-10-15T12:00:02.000Z"
  },
  "/two": {
    "url": "http://127.0.0.1:2",
    "pid": 2,
    "at": "2026-10-15T12:00:01.000Z"
  }
}`; got != want {
		t.Errorf("the registry =\n%s\nwant\n%s", got, want)
	}
	// a registry that is not JSON starts over rather than failing
	writeFile(t, RegistryFile(w), "not json")
	noteServer(ctx, w, "/three", ServerEntry{URL: "http://127.0.0.1:4"})
	if servers := Servers(ctx, w); len(servers) != 1 || servers["/three"].URL != "http://127.0.0.1:4" {
		t.Errorf("over a corrupt registry = %v", servers)
	}
}
