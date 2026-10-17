package cli

// The session credential's quiet edges: where the files live when no home resolves, a file that
// holds only half a credential, a write whose path is blocked, and the one retry a refused call
// earns.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestSessionsDirFallsBackToTheUsersHomeWhenNoHomeResolves pins the last resort: no
// HUDDLE_SESSIONS_DIR, no huddle home — the sessions directory is spelled from the user's own
// home, whose read failing stands for ".".
func TestSessionsDirFallsBackToTheUsersHomeWhenNoHomeResolves(t *testing.T) {
	t.Setenv("HOME", "")
	w := world(t, t.TempDir(), map[string]string{"HOME": ""})
	if got, want := SessionsDir(w), filepath.Join(".", ".config", "huddle", "sessions"); got != want {
		t.Errorf("SessionsDir without a home = %q, want %q", got, want)
	}
	t.Setenv("HOME", t.TempDir())
	if got := userHomeDir(); got != os.Getenv("HOME") {
		t.Errorf("userHomeDir = %q", got)
	}
}

// TestProjectKeyWithoutAProjectIsEmpty pins the project key's empty answer: no project dir and
// no checkout around the working directory means no project credential is read or written.
func TestProjectKeyWithoutAProjectIsEmpty(t *testing.T) {
	w := world(t, t.TempDir(), map[string]string{"HUDDLE_NO_PROJECT_CRED": ""})
	if got := ProjectKey(context.Background(), w); got != "" {
		t.Errorf("ProjectKey outside a project = %q", got)
	}
}

// TestReadCredRefusesAFileWithoutACredential pins the half-written file: a url with no
// credential in it is no credential.
func TestReadCredRefusesAFileWithoutACredential(t *testing.T) {
	sessions := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sessions, "terminal.json"), `{"url": "http://127.0.0.1:1"}`)
	w := world(t, t.TempDir(), map[string]string{"HUDDLE_SESSIONS_DIR": sessions})
	if cred := LoadCred(context.Background(), w, "", ""); cred != nil {
		t.Errorf("a url-only file = %+v", cred)
	}
}

// TestSaveCredSaysWhyWhenAPathItNeedsIsBlocked pins the write's two refusals: a temp path that
// cannot be created, and a target that is a directory, both surface as errors.
func TestSaveCredSaysWhyWhenAPathItNeedsIsBlocked(t *testing.T) {
	ctx := context.Background()
	sessions := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	w := world(t, t.TempDir(), map[string]string{"HUDDLE_SESSIONS_DIR": sessions})
	tmp := filepath.Join(sessions, "terminal.json."+strconv.Itoa(os.Getpid())+".tmp")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := SaveCred(ctx, w, Cred{URL: "http://127.0.0.1:1", Credential: "c"}, ""); err == nil {
		t.Error("SaveCred over a blocked temp path succeeded")
	}
	if err := os.Remove(tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(sessions, "terminal.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := SaveCred(ctx, w, Cred{URL: "http://127.0.0.1:1", Credential: "c"}, ""); err == nil {
		t.Error("SaveCred onto a directory target succeeded")
	}
}

// TestForgetIdleTakesAMissingDirectoryQuietly pins the sweep over a directory that is not
// there: nothing to forget, nothing said.
func TestForgetIdleTakesAMissingDirectoryQuietly(t *testing.T) {
	w := world(t, t.TempDir(), nil)
	forgetIdle(context.Background(), w, filepath.Join(t.TempDir(), "gone"))
}

// TestHFetchRefusesABadAddress pins the parse failure: a url that is not a url is an error,
// never a request.
func TestHFetchRefusesABadAddress(t *testing.T) {
	w := world(t, t.TempDir(), nil)
	if _, err := HFetch(context.Background(), w, http.MethodGet, "://not a url", nil, nil, 0, ""); err == nil {
		t.Error("HFetch over a bad url succeeded")
	}
}

// TestHFetchRetriesOnceWithTheFreshTokenOnARefusal pins the 401 retry: the first call carries
// the token the world had, the refusal sends it back for the credential again, and the retry
// carries the new one.
func TestHFetchRetriesOnceWithTheFreshTokenOnARefusal(t *testing.T) {
	var mu sync.Mutex
	tokens := []string{}
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		mu.Lock()
		tokens = append(tokens, call.Token)
		count := len(tokens)
		mu.Unlock()
		if count == 1 {
			return http.StatusUnauthorized, `{"error": "stale"}`
		}
		return 200, `{"ok": true}`
	})
	calls := 0
	var seen []string
	w := &World{
		Getenv: func(key string) string {
			if key == "HUDDLE_TOKEN" {
				calls++
				if calls == 1 {
					return "first-token"
				}
				return "second-token"
			}
			return ""
		},
		Environ: func() []string { return nil },
	}
	resp, err := HFetch(context.Background(), w, http.MethodPost, fake.server.URL+"/api/c/c/op/ping",
		map[string]string{"content-type": "application/json"}, []byte("{}"), 0, "")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	mu.Lock()
	seen = append([]string{}, tokens...)
	mu.Unlock()
	if strings.Join(seen, ",") != "first-token,second-token" {
		t.Errorf("the calls carried %v", seen)
	}
}
