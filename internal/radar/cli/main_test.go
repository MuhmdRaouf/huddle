// The wiring cases for the real effects: the deps a process builds, the fetch it runs, and the
// failed spawn it swallows.
package cli

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// answerServer answers every request with one body and status.
func answerServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

// TestRealDepsWiring pins the effects a real command run takes: the version and default window
// it carries, the watch loop that never stops on its own, and the writers and clock it uses.
func TestRealDepsWiring(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	deps := RealDeps("9.9.9", &stdout, &stderr)
	if deps.Version != "9.9.9" {
		t.Errorf("the version is %q, want the one handed in", deps.Version)
	}
	if deps.SinceMs != DefaultSinceMs {
		t.Errorf("the default window is %d, want %d", deps.SinceMs, DefaultSinceMs)
	}
	if deps.ShouldStop() {
		t.Error("the watch loop never stops on its own")
	}
	if deps.Env["HOME"] != os.Getenv("HOME") {
		t.Errorf("the environment resolves HOME to %q", deps.Env["HOME"])
	}
	deps.Out("one")
	deps.Err("two")
	if stdout.String() != "one\n" || stderr.String() != "two\n" {
		t.Errorf("the writers hold (%q, %q), want the out and err lines", stdout.String(), stderr.String())
	}
	if clock := deps.Now(); clock <= 0 {
		t.Errorf("the clock reads epoch milliseconds, got %d", clock)
	}
	deps.Sleep(1)
}

// TestFetchJSON pins the fetch the CLI runs: transport trouble, an error status and a body that
// is not JSON are misses; a body that is not an object answers OK with no body.
func TestFetchJSON(t *testing.T) {
	green := answerServer(t, `{"ok":true,"sessions":1}`, http.StatusOK)
	if got := fetchJSON(green.URL + "/api/health"); !got.OK || got.Body["ok"] != true {
		t.Errorf("a green object answers (%v, %v)", got.OK, got.Body)
	}

	list := answerServer(t, `[1,2]`, http.StatusOK)
	if got := fetchJSON(list.URL); !got.OK || got.Body != nil {
		t.Errorf("a JSON array is not a body: (%v, %v)", got.OK, got.Body)
	}

	garbage := answerServer(t, `not json`, http.StatusOK)
	if got := fetchJSON(garbage.URL); got.OK {
		t.Error("a body that is not JSON is a miss")
	}

	if got := fetchJSON("http://127.0.0.1:1/api/health"); got.OK {
		t.Error("an unreachable endpoint is a miss")
	}
}

// TestSpawnDetachedAnswersZeroWhenTheBinaryCannotBeResolved pins the failed-start answer the
// start loop ignores.
func TestSpawnDetachedAnswersZeroWhenTheBinaryCannotBeResolved(t *testing.T) {
	original := Executable
	t.Cleanup(func() { Executable = original })
	Executable = func() (string, error) { return "", errors.New("gone") }
	if pid := spawnDetached([]string{"--version"}); pid != 0 {
		t.Errorf("a failed start answered pid %d, want 0", pid)
	}
}
