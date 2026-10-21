package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
)

func TestSessionKeysAreMostSpecificFirstWithTheProjectLast(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		sid  string
		want []string
	}{
		{"a hook session id wins", map[string]string{"HUDDLE_SESSION": "s2", "CLAUDE_CODE_SESSION_ID": "s3"}, "s 1!", []string{"s1", "s2", "s3"}},
		{"the Claude pid aliases them", map[string]string{"CLAUDE_PID": "42", "CLAUDE_CODE_SESSION_ID": "s3"}, "", []string{"s3", "pid-42"}},
		{"a shell outside Claude Code", nil, "", []string{"terminal"}},
		{"an unsafe key is cleaned", map[string]string{"HUDDLE_SESSION": "a/b\\c"}, "", []string{"abc"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := world(t, t.TempDir(), c.env)
			if got := SessionKeys(w, c.sid); !reflect.DeepEqual(got, c.want) {
				t.Errorf("SessionKeys = %q, want %q", got, c.want)
			}
		})
	}
}

// TestLoadCredPrefersTheProjectsKeyOverTerminal pins the lookup order: a process in a project
// dir with no session id uses the project's credential, not a leftover terminal.json another
// project saved against a server that is gone; with a session id the session's own file still
// wins, and a plain terminal outside any project still finds the terminal file.
func TestLoadCredPrefersTheProjectsKeyOverTerminal(t *testing.T) {
	ctx := context.Background()
	sessions := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	projectDir := t.TempDir()
	env := map[string]string{
		"HUDDLE_SESSIONS_DIR":    sessions,
		"CLAUDE_PROJECT_DIR":     projectDir,
		"HUDDLE_NO_PROJECT_CRED": "",
	}
	in := world(t, t.TempDir(), env)
	key := ProjectKey(ctx, in)
	if key == "" {
		t.Fatal("no project key in a project dir")
	}
	writeFile(t, filepath.Join(sessions, "terminal.json"),
		`{"url":"http://127.0.0.1:42101","channel":"other","credential":"hcred_other"}`)
	writeFile(t, filepath.Join(sessions, key+".json"),
		`{"url":"http://127.0.0.1:42101","channel":"mine","credential":"hcred_mine"}`)

	if got, want := SessionKeys(in, ""), []string{key, "terminal"}; !reflect.DeepEqual(got, want) {
		t.Errorf("SessionKeys with no session id = %q, want %q", got, want)
	}
	if cred := LoadCred(ctx, in, "", ""); cred == nil || cred.Channel != "mine" {
		t.Errorf("LoadCred with no session id = %+v, want the project's channel mine", cred)
	}
	withSession := world(t, in.Dir, map[string]string{
		"HUDDLE_SESSIONS_DIR":    sessions,
		"CLAUDE_PROJECT_DIR":     projectDir,
		"HUDDLE_NO_PROJECT_CRED": "",
		"CLAUDE_CODE_SESSION_ID": "s1",
	})
	writeFile(t, filepath.Join(sessions, "s1.json"),
		`{"url":"http://127.0.0.1:42101","channel":"sess","credential":"hcred_sess"}`)
	if cred := LoadCred(ctx, withSession, "", ""); cred == nil || cred.Channel != "sess" {
		t.Errorf("LoadCred with CLAUDE_CODE_SESSION_ID = %+v, want the session's channel sess", cred)
	}
	outside := world(t, t.TempDir(), map[string]string{"HUDDLE_SESSIONS_DIR": sessions, "HUDDLE_NO_PROJECT_CRED": "1"})
	if cred := LoadCred(ctx, outside, "", ""); cred == nil || cred.Channel != "other" {
		t.Errorf("LoadCred with no project file = %+v, want terminal's channel other", cred)
	}
}

func TestProjectKeyIsTheMainCheckoutHashedNotAPathSpelling(t *testing.T) {
	real := t.TempDir()
	linked := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, linked); err != nil {
		t.Skip("symlinks unavailable")
	}
	plain := world(t, real, map[string]string{"CLAUDE_PROJECT_DIR": real, "HUDDLE_NO_PROJECT_CRED": ""})
	through := world(t, real, map[string]string{"CLAUDE_PROJECT_DIR": linked, "HUDDLE_NO_PROJECT_CRED": ""})
	key := ProjectKey(context.Background(), plain)
	if len(key) != len("project-")+16 || key[:8] != "project-" {
		t.Fatalf("ProjectKey = %q", key)
	}
	if other := ProjectKey(context.Background(), through); other != key {
		t.Errorf("two spellings of one checkout: %q and %q differ", key, other)
	}
	off := world(t, real, map[string]string{"HUDDLE_NO_PROJECT_CRED": "1", "CLAUDE_PROJECT_DIR": real})
	if key := ProjectKey(context.Background(), off); key != "" {
		t.Errorf("HUDDLE_NO_PROJECT_CRED: ProjectKey = %q, want none", key)
	}
	none := world(t, real, map[string]string{"HUDDLE_NO_PROJECT_CRED": ""})
	if key := ProjectKey(context.Background(), none); key != "" {
		t.Errorf("no project: ProjectKey = %q, want none", key)
	}
}

func TestSaveLoadAndForgetCredPerSessionAndProject(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	w := world(t, t.TempDir(), map[string]string{"HUDDLE_SESSIONS_DIR": filepath.Join(state, "sessions"), "CLAUDE_PID": "7"})
	if err := SaveCred(ctx, w, Cred{URL: "http://127.0.0.1:9000/", Channel: "shop", As: "api", Credential: "hcred_x", Root: true}, "s 1!"); err != nil {
		t.Fatal(err)
	}
	// under every key of the session, the url normalised, the file 0600 in a 0700 dir
	for _, name := range []string{"s1.json", "pid-7.json"} {
		file := filepath.Join(state, "sessions", name)
		text := readFile(t, file)
		if text != `{"url":"http://127.0.0.1:9000","channel":"shop","as":"api","credential":"hcred_x","root":true}` {
			t.Errorf("%s = %s", name, text)
		}
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", name, info.Mode().Perm())
		}
	}
	info, err := os.Stat(filepath.Join(state, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("sessions dir mode = %v, want 0700", info.Mode().Perm())
	}
	// loading: any url when none is asked, only a matching one when one is
	if c := LoadCred(ctx, w, "", ""); c == nil || c.Credential != "hcred_x" {
		t.Errorf("LoadCred(any) = %+v", c)
	}
	if c := LoadCred(ctx, w, "http://127.0.0.1:9000", ""); c == nil {
		t.Errorf("LoadCred(matching url) = %+v", c)
	}
	if c := LoadCred(ctx, w, "http://127.0.0.1:1", ""); c != nil {
		t.Errorf("LoadCred(other url) = %+v", c)
	}
	// the token to send: HUDDLE_TOKEN, else the session's for that url, else none
	if got := TokenFor(ctx, w, "http://127.0.0.1:9000", ""); got != "hcred_x" {
		t.Errorf("TokenFor = %q", got)
	}
	over := world(t, w.Dir, map[string]string{"HUDDLE_SESSIONS_DIR": filepath.Join(state, "sessions"), "HUDDLE_TOKEN": "root-cred"})
	if got := TokenFor(ctx, over, "http://127.0.0.1:9000", ""); got != "root-cred" {
		t.Errorf("TokenFor with HUDDLE_TOKEN = %q", got)
	}
	if got := TokenFor(ctx, world(t, t.TempDir(), nil), "http://127.0.0.1:9000", ""); got != "" {
		t.Errorf("TokenFor with nothing = %q", got)
	}
	ForgetCred(ctx, w, "")
	if c := LoadCred(ctx, w, "", ""); c != nil {
		t.Errorf("after forget: %+v", c)
	}
}

func TestSaveCredForgetsSessionFilesIdleForAWeek(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	sessions := filepath.Join(state, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(sessions, "old.json")
	project := filepath.Join(sessions, "project-0123456789abcdef.json")
	writeFile(t, old, `{"url":"http://127.0.0.1:1","credential":"c"}`)
	writeFile(t, project, `{"url":"http://127.0.0.1:1","credential":"c"}`)
	past := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(project, past, past); err != nil {
		t.Fatal(err)
	}
	w := world(t, t.TempDir(), map[string]string{"HUDDLE_SESSIONS_DIR": sessions, "HUDDLE_SESSION": "fresh"})
	if err := SaveCred(ctx, w, Cred{URL: "http://127.0.0.1:2", Credential: "c"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("an idle session file was kept")
	}
	if _, err := os.Stat(project); err != nil {
		t.Error("a project's file was forgotten")
	}
}

func TestProjectCredReadsAnotherProjectsKey(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	w := world(t, t.TempDir(), map[string]string{"CLAUDE_PROJECT_DIR": dir, "HUDDLE_NO_PROJECT_CRED": ""})
	if err := SaveCred(ctx, w, Cred{URL: "http://127.0.0.1:9000", Credential: "shared"}, ""); err != nil {
		t.Fatal(err)
	}
	// the key a project's own sessions write (the project dir resolved)
	if got := ProjectKey(context.Background(), w); got == "" {
		t.Fatal("no project key under the default resolver")
	}
	if c := ProjectCred(ctx, w, dir, ""); c == nil {
		t.Fatalf("ProjectCred(%s) = nil", dir)
	} else if c.Credential != "shared" {
		t.Errorf("ProjectCred = %+v", c)
	}
	if c := ProjectCred(ctx, w, dir, "http://127.0.0.1:1"); c != nil {
		t.Errorf("ProjectCred with another url = %+v", c)
	}
	if c := ProjectCred(ctx, w, filepath.Join(t.TempDir(), "missing"), ""); c != nil {
		t.Errorf("ProjectCred of a dir with no key = %+v", c)
	}
}

// TestHFetchSendsTheCredentialAndRetriesOnceOn401 pins hfetch: the session's credential goes in
// the header, a 401 reads it again (another process of the session may have just joined) and
// retries once with a new one, and no other response is retried.
func TestHFetchSendsTheCredentialAndRetriesOnceOn401(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	env := map[string]string{"HUDDLE_SESSIONS_DIR": filepath.Join(state, "sessions")}
	stale := world(t, t.TempDir(), env)
	var seen []string
	refreshes := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get(auth.Header)
		seen = append(seen, token)
		if token == "stale" && refreshes == 0 {
			refreshes++
			// another process of this session joins while the call is in flight
			if err := SaveCred(ctx, stale, Cred{URL: srv.URL, Credential: "fresh"}, ""); err != nil {
				t.Error(err)
			}
		}
		if token == "stale" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"no"}`))
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	if err := SaveCred(ctx, stale, Cred{URL: srv.URL, Credential: "stale"}, ""); err != nil {
		t.Fatal(err)
	}
	resp, err := HFetch(ctx, stale, http.MethodPost, srv.URL+"/api/c/shop/op/send?as=a", map[string]string{"content-type": "application/json"}, []byte(`{"msg":"hi"}`), 0, "")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !reflect.DeepEqual(seen, []string{"stale", "fresh"}) {
		t.Fatalf("status %d, tokens %q, want one retry with the fresh credential", resp.StatusCode, seen)
	}
	// a 401 with nothing new on disk: the refusal comes back as it was
	seen = nil
	if err := SaveCred(ctx, stale, Cred{URL: srv.URL, Credential: "stale"}, ""); err != nil {
		t.Fatal(err)
	}
	resp, err = HFetch(ctx, stale, http.MethodGet, srv.URL+"/health", nil, nil, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || !reflect.DeepEqual(seen, []string{"stale"}) {
		t.Errorf("status %d, tokens %q, want the one refusal", resp.StatusCode, seen)
	}
	// HUDDLE_TOKEN overrides the file, so a 401 has nothing new to try
	seen = nil
	rooted := world(t, t.TempDir(), map[string]string{"HUDDLE_TOKEN": "root-1", "HUDDLE_SESSIONS_DIR": filepath.Join(state, "sessions")})
	resp, err = HFetch(ctx, rooted, http.MethodGet, srv.URL+"/health", nil, nil, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !reflect.DeepEqual(seen, []string{"root-1"}) {
		t.Errorf("status %d, tokens %q", resp.StatusCode, seen)
	}
}

// ownerKeyHome lays one home out with its server's port named in the run directory and the
// owner key in place, the state a serve no one handed a root to leaves behind.
func ownerKeyHome(t *testing.T, port, key string, keyMode os.FileMode) string {
	t.Helper()
	homeDir := t.TempDir()
	run := filepath.Join(homeDir, "run")
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(run, "huddle.port"), port)
	if key != "" {
		file := filepath.Join(run, "owner.key")
		if err := os.WriteFile(file, []byte(key+"\n"), keyMode); err != nil {
			t.Fatal(err)
		}
	}
	return homeDir
}

func TestTokenForReadsTheOwnerKeyOfThisHomesOwnLoopbackServer(t *testing.T) {
	ctx := context.Background()
	url := "http://127.0.0.1:29731"
	w := world(t, t.TempDir(), map[string]string{"HUDDLE_HOME": ownerKeyHome(t, "29731", "hcred_owner", 0o600)})
	if got := TokenFor(ctx, w, url, ""); got != "hcred_owner" {
		t.Errorf("TokenFor(own server) = %q, want the owner key", got)
	}
	if got := TokenFor(ctx, w, "http://localhost:29731", ""); got != "hcred_owner" {
		t.Errorf("TokenFor(localhost spelling) = %q, want the owner key", got)
	}
	// only this home's own address: another port, another machine's address, no key at all
	for _, c := range []struct {
		name string
		url  string
		env  map[string]string
	}{
		{"another port", "http://127.0.0.1:29732", nil},
		{"a remote address", "https://hub.example.dev:8443", nil},
		{"another home", url, map[string]string{"HUDDLE_HOME": ownerKeyHome(t, "29740", "", 0o600)}},
		{"no key file", url, map[string]string{"HUDDLE_HOME": ownerKeyHome(t, "29731", "", 0o600)}},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := map[string]string{"HUDDLE_HOME": w.getenv("HUDDLE_HOME")}
			for key, value := range c.env {
				env[key] = value
			}
			if got := TokenFor(ctx, world(t, t.TempDir(), env), c.url, ""); got != "" {
				t.Errorf("TokenFor(%s) = %q, want none", c.url, got)
			}
		})
	}
	// the port the home's config saves names the same server (a serve with no port named)
	other := ownerKeyHome(t, "", "hcred_owner", 0o600)
	SavePort(ctx, world(t, t.TempDir(), nil), 29733, false, other)
	configured := world(t, t.TempDir(), map[string]string{"HUDDLE_HOME": other})
	if got := TokenFor(ctx, configured, "http://127.0.0.1:29733", ""); got != "hcred_owner" {
		t.Errorf("TokenFor(config's port) = %q, want the owner key", got)
	}
	// a session's own credential and HUDDLE_TOKEN both win over the key
	mine := world(t, t.TempDir(), map[string]string{"HUDDLE_HOME": w.getenv("HUDDLE_HOME")})
	if err := SaveCred(ctx, mine, Cred{URL: url, Credential: "hcred_mine"}, ""); err != nil {
		t.Fatal(err)
	}
	if got := TokenFor(ctx, mine, url, ""); got != "hcred_mine" {
		t.Errorf("TokenFor with a session credential = %q", got)
	}
	rooted := world(t, t.TempDir(), map[string]string{"HUDDLE_HOME": w.getenv("HUDDLE_HOME"), "HUDDLE_TOKEN": "root-cred"})
	if got := TokenFor(ctx, rooted, url, ""); got != "root-cred" {
		t.Errorf("TokenFor with HUDDLE_TOKEN = %q", got)
	}
}

// fakeOwnerKeyStat is a stat whose owner is whoever the test says: the uid check reads it through
// the World's hook, because no test may chown a file to another user.
type fakeOwnerKeyStat struct {
	mode os.FileMode
	sys  any
}

func (fakeOwnerKeyStat) Name() string        { return "owner.key" }
func (f fakeOwnerKeyStat) Size() int64       { return 32 }
func (f fakeOwnerKeyStat) Mode() os.FileMode { return f.mode }
func (fakeOwnerKeyStat) ModTime() time.Time  { return time.Unix(0, 0) }
func (fakeOwnerKeyStat) IsDir() bool         { return false }
func (f fakeOwnerKeyStat) Sys() any          { return f.sys }

func TestTheOwnerKeyAnotherUserOwnsOrOthersReadIsRefusedWithALine(t *testing.T) {
	ctx := context.Background()
	url := "http://127.0.0.1:29751"
	hearStderr := func(t *testing.T, ask func() string) (string, string) {
		t.Helper()
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		old := os.Stderr
		os.Stderr = writer
		got := ask()
		os.Stderr = old
		if err := writer.Close(); err != nil {
			t.Fatalf("close the pipe's writer: %v", err)
		}
		raw, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("read the pipe: %v", err)
		}
		return got, string(raw)
	}
	// group and others may read it
	open := world(t, t.TempDir(), map[string]string{"HUDDLE_HOME": ownerKeyHome(t, "29751", "hcred_owner", 0o644)})
	if got, said := hearStderr(t, func() string { return TokenFor(ctx, open, url, "") }); got != "" {
		t.Errorf("TokenFor(0644 key) = %q, want none", got)
	} else if !strings.Contains(said, "owner.key") || !strings.Contains(said, "group or others") {
		t.Errorf("the refusal says %q, want the file and the reason", said)
	}
	// another user owns it (a fake stat: no test may chown to one)
	homeDir := ownerKeyHome(t, "29751", "hcred_owner", 0o600)
	key := filepath.Join(homeDir, "run", "owner.key")
	for _, c := range []struct {
		name string
		stat fakeOwnerKeyStat
		why  string
	}{
		{"owned by another uid", fakeOwnerKeyStat{mode: 0o600, sys: &syscall.Stat_t{Uid: 4242}}, "another user"},
		{"no owner to read", fakeOwnerKeyStat{mode: 0o600}, "another user"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := world(t, t.TempDir(), map[string]string{"HUDDLE_HOME": homeDir})
			w.FileStat = func(path string) (os.FileInfo, error) {
				if path == key {
					return c.stat, nil
				}
				return os.Stat(path)
			}
			if got, said := hearStderr(t, func() string { return TokenFor(ctx, w, url, "") }); got != "" {
				t.Errorf("TokenFor = %q, want none", got)
			} else if !strings.Contains(said, "owner.key") || !strings.Contains(said, c.why) {
				t.Errorf("the refusal says %q, want the file and %q", said, c.why)
			}
		})
	}
	// the port file names another port: the key is not even considered, so nothing is said
	away := world(t, t.TempDir(), map[string]string{"HUDDLE_HOME": homeDir})
	if got, said := hearStderr(t, func() string { return TokenFor(ctx, away, "http://127.0.0.1:29761", "") }); got != "" || said != "" {
		t.Errorf("TokenFor(another port) = %q, stderr %q, want silence", got, said)
	}
}
