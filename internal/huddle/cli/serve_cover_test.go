package cli

// The server commands' quiet edges: the health checks' refusals, the run directory's unreadable
// files, the places a start can be refused, and a start whose child dies or dawdles.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// healthJSON answers a /health body naming this home.
func healthJSON(root string) string { return fmt.Sprintf(`{"home": %q}`, root) }

// TestHealthyRefusesABadAddress pins the health check over a string that is no url: not
// healthy, never a crash.
func TestHealthyRefusesABadAddress(t *testing.T) {
	if Healthy(context.Background(), "://not a url", 100) {
		t.Error("an unparseable url is healthy")
	}
}

// TestSavedRunPortReadsOnlyARealPort pins the run port file's reader: a missing file, a non-
// number and an out-of-range number all read as no port.
func TestSavedRunPortReadsOnlyARealPort(t *testing.T) {
	root := t.TempDir()
	if got := savedRunPort(root); got != 0 {
		t.Errorf("savedRunPort without a file = %d", got)
	}
	if err := os.MkdirAll(filepath.Join(root, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "run", "huddle.port"), "tomorrow")
	if got := savedRunPort(root); got != 0 {
		t.Errorf("savedRunPort over a word = %d", got)
	}
	writeFile(t, filepath.Join(root, "run", "huddle.port"), "70000")
	if got := savedRunPort(root); got != 0 {
		t.Errorf("savedRunPort over 70000 = %d", got)
	}
	writeFile(t, filepath.Join(root, "run", "huddle.port"), " 18860 ")
	if got := savedRunPort(root); got != 18860 {
		t.Errorf("savedRunPort over 18860 = %d", got)
	}
}

// TestNoteRunTakesAnUnwritableLogQuietly pins the run log's refusals: a run directory that
// cannot be made and a log that is a directory both stay silent.
func TestNoteRunTakesAnUnwritableLogQuietly(t *testing.T) {
	blocked := t.TempDir()
	writeFile(t, filepath.Join(blocked, "run"), "a file, not a directory")
	noteRun(blocked, time.Unix(0, 0).UTC(), "start pid 1")
	withDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(withDir, "run", "huddle.log"), 0o700); err != nil {
		t.Fatal(err)
	}
	noteRun(withDir, time.Unix(0, 0).UTC(), "start pid 1")
}

// TestOursAnswersOnlyForOurOwnHome pins the health probe: a server of another home, a refusal
// and an answer that is not JSON are all not ours.
func TestOursAnswersOnlyForOurOwnHome(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	var mu sync.Mutex
	status := http.StatusOK
	body := healthJSON(root)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(fake.Close)
	port, err := strconv.Atoi(strings.TrimPrefix(fake.URL, "http://127.0.0.1:"))
	if err != nil {
		t.Fatal(err)
	}
	if !oursAnswers(ctx, port, root) {
		t.Error("our own home did not answer")
	}
	mu.Lock()
	status = http.StatusInternalServerError
	mu.Unlock()
	if oursAnswers(ctx, port, root) {
		t.Error("a refusing server counted as ours")
	}
	mu.Lock()
	status = http.StatusOK
	body = "not json at all"
	mu.Unlock()
	if oursAnswers(ctx, port, root) {
		t.Error("a garbage answer counted as ours")
	}
	mu.Lock()
	body = healthJSON("/another/huddle")
	mu.Unlock()
	if oursAnswers(ctx, port, root) {
		t.Error("another home's server counted as ours")
	}
}

// TestPlaceRefusesANonLocalAddressAndABusyForeignPort pins the start's refusals: an address off
// this machine, and a port another program holds while this home's server is named by the pid
// file.
func TestPlaceRefusesANonLocalAddressAndABusyForeignPort(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	w := cliWorld(t, root, t.TempDir(), nil)
	if at := place(ctx, w, "https://hub.mine.test:8443", ""); !strings.Contains(at.err, "not a local http address") {
		t.Errorf("place over a remote address = %+v", at)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	busy := listener.Addr().(*net.TCPAddr).Port
	if err := os.MkdirAll(filepath.Join(root, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "run", "huddle.pid"), "4242")
	w.Proc = Proc{Command: func(pid int) (string, bool) { return "huddle serve", true }}
	at := place(ctx, w, Local(busy), "")
	if !strings.Contains(at.err, "does not answer at") {
		t.Errorf("place over a busy port = %+v", at)
	}
}

// TestClampMSKeepsABudgetBetweenOneAndItsCap pins the budget clamp: nothing below one and
// nothing above the cap.
func TestClampMSKeepsABudgetBetweenOneAndItsCap(t *testing.T) {
	for _, tc := range []struct {
		ms   time.Duration
		cap  int
		want int
	}{
		{0, 500, 1},
		{time.Microsecond, 500, 1},
		{2 * time.Second, 500, 500},
		{250 * time.Millisecond, 500, 250},
	} {
		if got := clampMS(tc.ms, tc.cap); got != tc.want {
			t.Errorf("clampMS(%v, %d) = %d, want %d", tc.ms, tc.cap, got, tc.want)
		}
	}
}

// TestIsRunningFollowsTheChannel pins the child probe: an open channel means running, a closed
// one means gone.
func TestIsRunningFollowsTheChannel(t *testing.T) {
	if !isRunning(nil, make(chan struct{})) {
		t.Error("an open channel read as gone")
	}
	ended := make(chan struct{})
	close(ended)
	if isRunning(nil, ended) {
		t.Error("a closed channel read as running")
	}
}

// TestOursOnHealthChecksThePortTheRunDirNames pins the pid file's second check: no port file
// stands on the pid alone, a dead port refuses, a live huddle answers.
func TestOursOnHealthChecksThePortTheRunDirNames(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if !oursOnHealth(ctx, root) {
		t.Error("no port file read as not ours")
	}
	if err := os.MkdirAll(filepath.Join(root, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "run", "huddle.port"), "1")
	if oursOnHealth(ctx, root) {
		t.Error("a dead port read as ours")
	}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(healthJSON(root)))
	}))
	t.Cleanup(fake.Close)
	port, err := strconv.Atoi(strings.TrimPrefix(fake.URL, "http://127.0.0.1:"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "run", "huddle.port"), strconv.Itoa(port))
	if !oursOnHealth(ctx, root) {
		t.Error("a live huddle did not answer as ours")
	}
}

// TestProjectOfNamesTheRepoAroundTheWorkingDirectory pins the project a start notes: the
// checkout around the working directory, else the project directory.
func TestProjectOfNamesTheRepoAroundTheWorkingDirectory(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	w := cliWorld(t, repo, t.TempDir(), nil)
	if got := projectOf(ctx, w); got != realDir(t, repo) {
		t.Errorf("projectOf in a checkout = %q, want %q", got, realDir(t, repo))
	}
	plain := t.TempDir()
	w2 := cliWorld(t, plain, t.TempDir(), nil)
	if got := projectOf(ctx, w2); got != realDir(t, plain) {
		t.Errorf("projectOf outside a checkout = %q, want %q", got, realDir(t, plain))
	}
}

// TestProjectOfResolvesThePathsSpelling pins the registry's project through any spelling of one
// path: a project reached as /var/x and as /private/var/x must name itself one way, or setup
// does not recognize the server its own session started.
func TestProjectOfResolvesThePathsSpelling(t *testing.T) {
	proj := t.TempDir()
	gitInit(t, proj)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(proj, link); err != nil {
		t.Fatal(err)
	}
	w := world(t, link, nil)
	if got := projectOf(context.Background(), w); got != realDir(t, proj) {
		t.Errorf("projectOf through %s = %q, want %q", link, got, realDir(t, proj))
	}
}

// TestKeepOutOfGitTakesARefusedExcludeQuietly pins the exclude write's refusals: an info path
// that cannot be made and an exclude that is a directory both answer nothing.
func TestKeepOutOfGitTakesARefusedExcludeQuietly(t *testing.T) {
	ctx := context.Background()
	infoBlocked := t.TempDir()
	if err := os.MkdirAll(filepath.Join(infoBlocked, ".agents", "huddle"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(infoBlocked, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(infoBlocked, ".git", "info"), "a file, not a directory")
	if got := KeepOutOfGit(ctx, cliWorld(t, infoBlocked, t.TempDir(), nil), filepath.Join(infoBlocked, ".agents", "huddle")); got != "" {
		t.Errorf("KeepOutOfGit with a blocked info path = %q", got)
	}
	excludeBlocked := t.TempDir()
	if err := os.MkdirAll(filepath.Join(excludeBlocked, ".agents", "huddle"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(excludeBlocked, ".git", "info", "exclude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := KeepOutOfGit(ctx, cliWorld(t, excludeBlocked, t.TempDir(), nil), filepath.Join(excludeBlocked, ".agents", "huddle")); got != "" {
		t.Errorf("KeepOutOfGit over a directory exclude = %q", got)
	}
}

// TestUpSaysWhenTheServerNeverRan pins the start whose child dies at once: the answer says the
// huddle never came up, and the credential that call minted is dropped again.
func TestUpSaysWhenTheServerNeverRan(t *testing.T) {
	root := t.TempDir()
	w := cliWorld(t, root, t.TempDir(), map[string]string{"HUDDLE_TOKEN": ""})
	w.Bin = writeScript(t, "#!/bin/sh\nexit 1\n")
	port := freePort(t)
	result := Up(context.Background(), w, Local(port), 1500, "")
	if result.OK || !strings.Contains(result.Msg, "did not come up") || strings.Contains(result.Msg, "still starting") {
		t.Fatalf("up over a child that dies = %+v", result)
	}
	if cred := LoadCred(context.Background(), w, Local(port), ""); cred != nil {
		t.Errorf("the minted credential stayed: %+v", cred)
	}
}

// TestUpSaysWhenTheServerIsStillStarting pins the start whose child lives but never answers
// within the budget: the answer says it is still starting, and the child is stopped.
func TestUpSaysWhenTheServerIsStillStarting(t *testing.T) {
	root := t.TempDir()
	w := cliWorld(t, root, t.TempDir(), nil)
	w.Bin = writeScript(t, "#!/bin/sh\nexec sleep 30\n")
	port := freePort(t)
	result := Up(context.Background(), w, Local(port), 1500, "")
	if result.OK || !strings.Contains(result.Msg, "still starting") {
		t.Fatalf("up over a dawdling child = %+v", result)
	}
	if raw, err := os.ReadFile(pidFile(root)); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
			t.Cleanup(func() { stopProcess(pid) })
		}
	}
}

// TestUpReusesTheHuddleAlreadyOnTheSavedPort pins the start over a saved port that already
// answers as this home: nothing is started, and the answer says the huddle is up — or, when the
// huddle stops answering between the probe and the check, that it did not answer.
func TestUpReusesTheHuddleAlreadyOnTheSavedPort(t *testing.T) {
	root := t.TempDir()
	w := cliWorld(t, root, t.TempDir(), nil)
	var mu sync.Mutex
	calls := 0
	dim := false
	fake := httptest.NewServer(http.HandlerFunc(func(resp http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		up := !dim || calls%2 == 1 // healthy, or the probe's answer before the failed check
		mu.Unlock()
		if !up {
			resp.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = resp.Write([]byte(healthJSON(root)))
	}))
	t.Cleanup(fake.Close)
	port, err := strconv.Atoi(strings.TrimPrefix(fake.URL, "http://127.0.0.1:"))
	if err != nil {
		t.Fatal(err)
	}
	SavePort(context.Background(), w, port, true, root)
	result := Up(context.Background(), w, "", 3000, "")
	if !result.OK || result.Created || !strings.Contains(result.Msg, "Huddle is up at") {
		t.Fatalf("up over a running huddle = %+v", result)
	}
	mu.Lock()
	dim = true
	calls = 0
	mu.Unlock()
	stopped := Up(context.Background(), w, "", 3000, "")
	if stopped.OK || !strings.Contains(stopped.Msg, "did not answer") {
		t.Errorf("up over a huddle that stopped answering = %+v", stopped)
	}
}

// writeScript writes one executable shell script and answers its path.
func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "huddle-fake")
	writeFile(t, path, body)
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestDownWithoutAnythingIsFine pins down over a home with no pid file: nothing running, and
// nothing to stop.
func TestDownWithoutAnythingIsFine(t *testing.T) {
	w := cliWorld(t, t.TempDir(), t.TempDir(), nil)
	result := Down(context.Background(), w, "")
	if !result.OK || result.Msg != "Huddle is not running" {
		t.Errorf("down without a server = %+v", result)
	}
}

// TestWithoutVarDropsOnlyItsOwnEntry pins the child environment builder: the named variable
// goes, every other line stays in order.
func TestWithoutVarDropsOnlyItsOwnEntry(t *testing.T) {
	got := withoutVar([]string{"A=1", "HUDDLE_TOKEN=x", "B=2", "HUDDLE_TOKEN_ALIAS=y"}, "HUDDLE_TOKEN")
	if strings.Join(got, ",") != "A=1,B=2,HUDDLE_TOKEN_ALIAS=y" {
		t.Errorf("withoutVar = %v", got)
	}
}

// TestANewCredentialIsAlwaysPrefixed pins the minted credential's shape: hcred_ and a url-safe
// body, different every call.
func TestANewCredentialIsAlwaysPrefixed(t *testing.T) {
	first, second := newCredential(), newCredential()
	if !strings.HasPrefix(first, "hcred_") || !strings.HasPrefix(second, "hcred_") {
		t.Errorf("a credential without its prefix: %q %q", first, second)
	}
	if first == second {
		t.Error("two mints answered the same credential")
	}
}

// TestSavedPortReadsAChosenPort pins the saved port's reader: a config with a port answers it
// and whether it was a choice; a config without one answers nothing.
func TestSavedPortReadsAChosenPort(t *testing.T) {
	root := t.TempDir()
	if _, ok := SavedPort(root); ok {
		t.Error("SavedPort without a config")
	}
	if _, err := home.SavePort(home.Home{Root: root}, 18860, true); err != nil {
		t.Fatal(err)
	}
	choice, ok := SavedPort(root)
	if !ok || choice.Port != 18860 || !choice.Auto {
		t.Errorf("SavedPort = %+v (%v)", choice, ok)
	}
}
