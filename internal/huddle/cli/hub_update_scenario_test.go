package cli

// A new binary replaces the running hub (D2): up from this binary gives an older build's hub
// of the same home way — SIGTERM, a wait for its exit — and starts this binary's hub on the
// same port, while a hub of the same executable, of another home, or one the pid file does not
// vouch for is never touched. The old hub is this suite's own binary run again as a child: it
// answers the 0.0.1 health shape on a port and its first signal ends it, so the replacement's
// every step is visible.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
)

// stubHubEnv re-executes this test binary as an old hub (see stubHubChild).
const stubHubEnv = "HUDDLE_TEST_STUB_HUB"

// stubHubChild is the old hub a replacement scenario runs: it answers the 0.0.1 health shape
// — no executable, as a 0.0.1 build names none — until its first signal ends it. HUDDLE_TEST_
// STUB_HOME is the home the answer names; HUDDLE_TEST_STUB_EXE, when set, is the executable a
// newer build's answer names.
func stubHubChild() {
	port := os.Getenv("HUDDLE_TEST_STUB_PORT")
	stubHome := os.Getenv("HUDDLE_TEST_STUB_HOME")
	if port == "" || stubHome == "" {
		fmt.Fprintln(os.Stderr, "huddle test stub hub: no port or home")
		os.Exit(3)
	}
	answer := fmt.Sprintf(`{"ok":true,"version":"0.0.1","channels":5,"home":%q,"machine":"stub"}`, stubHome)
	if exe := os.Getenv("HUDDLE_TEST_STUB_EXE"); exe != "" {
		answer = fmt.Sprintf(`{"ok":true,"version":"0.0.1","channels":5,"home":%q,"machine":"stub","executable":%q}`, stubHome, exe)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(answer))
	})
	if err := http.ListenAndServe("127.0.0.1:"+port, mux); err != nil {
		fmt.Fprintf(os.Stderr, "huddle test stub hub: %v\n", err)
		os.Exit(3)
	}
}

// startStubHub runs this test binary again as an old hub answering at port over stubHome, and
// answers its pid once the health path answers. The test reaps the child; cleanup signals a
// survivor out of the way.
func startStubHub(t *testing.T, stubHome string, port int, exe string) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = isoenv.Environ(t, t.TempDir(),
		stubHubEnv+"=1",
		"HUDDLE_TEST_STUB_PORT="+strconv.Itoa(port),
		"HUDDLE_TEST_STUB_HOME="+stubHome,
		"HUDDLE_TEST_STUB_EXE="+exe,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the stub hub: %v", err)
	}
	reaped := make(chan struct{})
	go func() { _ = cmd.Wait(); close(reaped) }()
	t.Cleanup(func() {
		select {
		case <-reaped:
			return
		default:
		}
		_ = signalTerm(cmd.Process.Pid)
		select {
		case <-reaped:
		case <-time.After(3 * time.Second):
			_ = signalKill(cmd.Process.Pid)
			<-reaped
		}
	})
	url := Local(port)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if Healthy(context.Background(), url, 500) {
			return cmd.Process.Pid
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the stub hub never answered its health path")
	return 0
}

// vouchFor makes the world's pid check name pid as a huddle serve: the stub's command line is
// the test binary's, so the lookup is the test's own answer for it. Every other pid reads from
// the machine, as the cleanup's stop depends on it.
func vouchFor(w *World, pid int) {
	w.Proc = Proc{Command: func(at int) (string, bool) {
		if at == pid {
			return "huddle serve", true
		}
		return procCommand(at)
	}}
}

// recordRun writes the run files a started hub leaves: the pid and the port it answers on.
func recordRun(t *testing.T, root string, pid, port int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(pidFile(root)), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, pidFile(root), strconv.Itoa(pid))
	writeFile(t, portFile(root), strconv.Itoa(port))
}

// runLog is the home's run log, empty when nothing was logged yet.
func runLog(t *testing.T, root string) string {
	t.Helper()
	raw, err := os.ReadFile(logFile(root))
	if err != nil {
		return ""
	}
	return string(raw)
}

// hubExecutable reads the executable the hub at url names in its health answer.
func hubExecutable(t *testing.T, url string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url+"/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Timeout: 1500 * time.Millisecond}).Do(req)
	if err != nil {
		t.Fatalf("the health answer at %s: %v", url, err)
	}
	defer resp.Body.Close()
	var answer struct {
		Executable string `json:"executable"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		t.Fatalf("the health answer at %s: %v", url, err)
	}
	return answer.Executable
}

// thisBinary is huddleBin with its symlinks resolved: the spelling a health answer names.
func thisBinary(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(huddleBin)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestHubUpdateScenarios(t *testing.T) {
	bdd.Feature(t, "a new binary replaces the running hub", func(f *bdd.F) {
		f.Scenario("an older binary's hub gives way", func(s *bdd.S) {
			var (
				home    string
				port    int
				oldPID  int
				result  Result
				logText string
			)
			s.Given("an older build's hub answers for this home and the pid file names it", func(t *testing.T) {
				home = s.T.TempDir()
				port = pinFreePort(t, home)
				oldPID = startStubHub(s.T, home, port, "")
				recordRun(t, home, oldPID, port)
			})
			s.When("a session runs up from this binary at the old hub's address", func(t *testing.T) {
				w := cliWorld(s.T, home, filepath.Join(s.T.TempDir(), "state"), nil)
				vouchFor(w, oldPID)
				result = Up(context.Background(), w, Local(port), 35_000, "")
				logText = runLog(t, home)
			})
			s.Then("up says it updated, naming the old pid and the new one", func(t *testing.T) {
				if !result.OK {
					t.Fatalf("up = %q, want it updated", result.Msg)
				}
				updated := regexp.MustCompile(fmt.Sprintf(`^Huddle updated at %s \(pid %d → \d+\)$`,
					regexp.QuoteMeta(Local(port)), oldPID))
				if !updated.MatchString(result.Msg) {
					t.Errorf("up = %q, want the updated line for pid %d", result.Msg, oldPID)
				}
			})
			s.And("the old hub is gone, its replacement in the run log", func(t *testing.T) {
				if processAlive(oldPID) {
					t.Errorf("the old hub (pid %d) still runs", oldPID)
				}
				want := fmt.Sprintf("replaced hub pid %d from an unnamed build: SIGTERM", oldPID)
				if !strings.Contains(logText, want) {
					t.Errorf("the run log misses %q:\n%s", want, logText)
				}
			})
			s.And("this binary's hub answers on the same port", func(t *testing.T) {
				if !Healthy(context.Background(), Local(port), 1500) {
					t.Fatalf("no hub answers at %s", Local(port))
				}
				if got := hubExecutable(t, Local(port)); got != thisBinary(t) {
					t.Errorf("the hub at %s names %q, want this binary %q", Local(port), got, thisBinary(t))
				}
				if pid := strings.TrimSpace(readFile(t, pidFile(home))); pid == strconv.Itoa(oldPID) {
					t.Errorf("the pid file still names the old hub %s", pid)
				}
			})
		})

		f.Scenario("a hub of the same executable is left alone", func(s *bdd.S) {
			var (
				home   string
				port   int
				pid    int
				result Result
			)
			s.Given("a hub of this binary answers for the home and the pid file names it", func(t *testing.T) {
				home = s.T.TempDir()
				port = pinFreePort(t, home)
				pid = startStubHub(s.T, home, port, thisBinary(t))
				recordRun(t, home, pid, port)
			})
			s.When("a session runs up from this binary at its address", func(t *testing.T) {
				w := cliWorld(s.T, home, filepath.Join(s.T.TempDir(), "state"), nil)
				vouchFor(w, pid)
				result = Up(context.Background(), w, Local(port), 35_000, "")
			})
			s.Then("the same hub answers and nothing was signalled", func(t *testing.T) {
				if !result.OK {
					t.Fatalf("up = %q, want it up", result.Msg)
				}
				if want := fmt.Sprintf("Huddle is up at %s", Local(port)); result.Msg != want {
					t.Errorf("up = %q, want %q", result.Msg, want)
				}
				if !processAlive(pid) {
					t.Errorf("the hub (pid %d) was signalled", pid)
				}
				if got := hubExecutable(t, Local(port)); got != thisBinary(t) {
					t.Errorf("the hub at %s names %q, want %q", Local(port), got, thisBinary(t))
				}
				if log := runLog(t, home); strings.Contains(log, "replaced hub pid") {
					t.Errorf("the run log shows a replacement:\n%s", log)
				}
			})
		})

		f.Scenario("a hub of another home is left alone", func(s *bdd.S) {
			var (
				home   string
				port   int
				pid    int
				result Result
			)
			s.Given("a hub of another home answers at the address and the pid file names it", func(t *testing.T) {
				home = s.T.TempDir()
				port = pinFreePort(t, home)
				pid = startStubHub(s.T, filepath.Join(s.T.TempDir(), "elsewhere"), port, "")
				recordRun(t, home, pid, port)
			})
			s.When("a session of this home runs up at the address", func(t *testing.T) {
				w := cliWorld(s.T, home, filepath.Join(s.T.TempDir(), "state"), nil)
				vouchFor(w, pid)
				result = Up(context.Background(), w, Local(port), 35_000, "")
			})
			s.Then("the other home's hub answers and nothing was signalled", func(t *testing.T) {
				if !result.OK {
					t.Fatalf("up = %q, want it up", result.Msg)
				}
				if want := fmt.Sprintf("Huddle is up at %s", Local(port)); result.Msg != want {
					t.Errorf("up = %q, want %q", result.Msg, want)
				}
				if !processAlive(pid) {
					t.Errorf("the other home's hub (pid %d) was signalled", pid)
				}
				if log := runLog(t, home); strings.Contains(log, "replaced hub pid") {
					t.Errorf("the run log shows a replacement:\n%s", log)
				}
			})
		})
	})
}
