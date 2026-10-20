package cli

// The singleton up: a session whose home's serve lock is held by a server that answers reuses
// it, and one held by a server that stopped answering is replaced — never a duplicate.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/server"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
)

// hangEnv re-executes this test binary as a hung server (see hangServerChild).
const hangEnv = "HUDDLE_TEST_HANG"

// hangServerChild is the hung server a replacement scenario runs: it takes its home's serve
// lock, says so on stdout and then sleeps without listening — a server that stopped answering
// still holds the home. Only a signal ends it.
func hangServerChild() {
	release, err := server.LockHome(os.Getenv("HUDDLE_HOME"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "huddle test hang: %v\n", err)
		os.Exit(3)
	}
	defer release()
	fmt.Println("huddle test hang: lock held")
	for {
		time.Sleep(time.Hour)
	}
}

// startHungServer runs this test binary again as a hung server over home and waits until it
// holds the lock. It answers the helper's pid; the test reaps the helper, and a survivor of the
// replacement is killed when the test ends.
func startHungServer(t *testing.T, home string) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = isoenv.Environ(t, t.TempDir(), hangEnv+"=1", "HUDDLE_HOME="+home)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("pipe the hung helper: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the hung helper: %v", err)
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
	ready := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), "lock held") {
				ready <- nil
				return
			}
		}
		ready <- errors.New("ended before taking the lock")
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("the hung helper: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the hung helper never took the lock")
	}
	return cmd.Process.Pid
}

func TestUpSingletonScenarios(t *testing.T) {
	bdd.Feature(t, "a session replaces a server that stopped answering", func(f *bdd.F) {
		f.Scenario("a hung server is stopped and a fresh one serves", func(s *bdd.S) {
			var (
				home     string
				hung     int
				port     int
				state    string
				result   Result
				freshLog string
			)
			s.Given("a hung server holds the home's serve lock and answers nothing", func(t *testing.T) {
				home = s.T.TempDir()
				state = filepath.Join(s.T.TempDir(), "state")
				port = pinFreePort(t, home)
				hung = startHungServer(s.T, home)
			})
			s.When("a session runs huddle up", func(t *testing.T) {
				w := cliWorld(s.T, home, state, nil)
				result = Up(context.Background(), w, "", 30_000, "")
				freshLog = readFile(t, logFile(home))
			})
			s.Then("a fresh server serves the home", func(t *testing.T) {
				if !result.OK {
					t.Fatalf("up = %q, want it up", result.Msg)
				}
				if result.URL != Local(port) {
					t.Errorf("up = %s, want %s", result.URL, Local(port))
				}
				if !Healthy(context.Background(), result.URL, 1500) {
					t.Errorf("the fresh server does not answer at %s", result.URL)
				}
			})
			s.And("the hung one was signalled out of the way, each step in the run log", func(t *testing.T) {
				if processAlive(hung) {
					t.Errorf("the hung helper (pid %d) is still running", hung)
				}
				for _, want := range []string{
					fmt.Sprintf("replaced hung server pid %d: SIGTERM", hung),
				} {
					if !strings.Contains(freshLog, want) {
						t.Errorf("the run log misses %q:\n%s", want, freshLog)
					}
				}
			})
		})

		f.Scenario("a healthy server is reused, never signalled", func(s *bdd.S) {
			var (
				home      string
				port      int
				state     string
				pidBefore string
				first     Result
				second    Result
			)
			s.Given("a healthy server serves the home", func(t *testing.T) {
				home = s.T.TempDir()
				state = filepath.Join(s.T.TempDir(), "state")
				port = pinFreePort(t, home)
				w := cliWorld(s.T, home, state, nil)
				first = Up(context.Background(), w, "", 35_000, "")
				if !first.OK {
					t.Fatalf("the first up = %q", first.Msg)
				}
				pidBefore = strings.TrimSpace(readFile(t, pidFile(home)))
				pid, err := strconv.Atoi(pidBefore)
				if err != nil || pid <= 0 {
					t.Fatalf("no pid in the run file: %q", pidBefore)
				}
				s.T.Cleanup(func() {
					if processAlive(pid) {
						stopProcess(pid)
					}
				})
			})
			s.When("a session starts huddle again naming another address", func(t *testing.T) {
				w := cliWorld(s.T, home, state, nil)
				second = Up(context.Background(), w, Local(freePort(t)), 35_000, "")
			})
			s.Then("the same server answers and nothing was signalled", func(t *testing.T) {
				if !second.OK {
					t.Fatalf("the second up = %q", second.Msg)
				}
				if second.URL != Local(port) {
					t.Errorf("the second up = %s, want a reuse at %s", second.URL, Local(port))
				}
				if !Healthy(context.Background(), Local(port), 1500) {
					t.Errorf("the server stopped answering at %s", Local(port))
				}
				if pidNow := strings.TrimSpace(readFile(t, pidFile(home))); pidNow != pidBefore {
					t.Errorf("the pid file = %s, want the same server %s", pidNow, pidBefore)
				}
				if log := readFile(t, logFile(home)); strings.Contains(log, "replaced hung server") {
					t.Errorf("the run log shows a replacement:\n%s", log)
				}
			})
		})
	})
}
