package main

// An idle session wakes on what is meant for it: huddle's asyncRewake Stop hook, `huddle hook
// idle-wait`, long-polls the session's channel and exits 2 with the message on stderr, which
// Claude Code shows as a system reminder and answers at once. The tests run the built binary the
// way Claude Code runs the hook: two repos joined to one channel, the Stop hook's JSON on stdin,
// a message sent from the other repo. Every process is this test's child and every home a temp
// one.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// idleWakeWorld is a hub with channel homelab, three joined sessions (homelab, proxbeam, nas)
// and the environment each one runs in.
type idleWakeWorld struct {
	t       *testing.T
	root    string
	hubHome string
	hubURL  string
	repos   map[string]string
	envs    map[string]map[string]string
}

// idleWakeSession is the session id each member runs under.
func idleWakeSession(name string) string { return "sess-" + name }

// newIdleWakeWorld starts a hub, creates channel homelab from the first repo and joins the other
// two repos to it; each session starts through its session-start hook.
func newIdleWakeWorld(t *testing.T) *idleWakeWorld {
	t.Helper()
	// a short root: the homes and sockets under it stay inside the unix path bounds
	root, err := os.MkdirTemp("", "hr-iw-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	user := filepath.Join(root, "user")
	w := &idleWakeWorld{
		t: t, root: root, hubHome: filepath.Join(root, "huddle-home"),
		repos: map[string]string{}, envs: map[string]map[string]string{},
	}
	hubPort := ports.Free(t)
	w.hubURL = fmt.Sprintf("http://127.0.0.1:%d", hubPort)
	env := isoenv.Lookup(isoenv.Environ(t, user,
		"RADAR_HOME="+filepath.Join(root, "radar"),
		"RADAR_AUTOSTART=0",
		"HUDDLE_HOME="+w.hubHome,
		"HUDDLE_DATA="+filepath.Join(root, "huddle-data"),
		"HUDDLE_ROUTER_HOME="+filepath.Join(root, "router"),
		"HUDDLE_ROUTER_WATCH=0",
		"HUDDLE_NOTIFY_LOG="+filepath.Join(root, "notify.log"),
		"HUDDLE_PORT="+strconv.Itoa(hubPort),
	))
	for _, name := range []string{"homelab", "proxbeam", "nas"} {
		repo := twoReposRepo(t, user, root, name)
		w.repos[name] = repo
		w.envs[name] = argvLayer(env, map[string]string{
			"CLAUDE_PROJECT_DIR": repo, "CLAUDE_CODE_SESSION_ID": idleWakeSession(name),
		})
	}
	t.Cleanup(func() {
		pid := twoReposHubPid(w.hubHome)
		_, _, _ = runBin(t, huddleBin, w.envs["homelab"], w.repos["homelab"], "down")
		if !pollFor(5*time.Second, func() bool { return !argvHealthy(w.hubURL) }) && pid > 0 {
			// `down` left the test's own hub running: end it the direct way
			_ = syscall.Kill(pid, syscall.SIGTERM)
			if !pollFor(3*time.Second, func() bool { return !argvHealthy(w.hubURL) }) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		if pid > 0 && !pollFor(5*time.Second, func() bool { return syscall.Kill(pid, 0) != nil }) {
			t.Errorf("the hub (pid %d) still runs after the test", pid)
		}
	})

	code, out, errOut := runBin(t, huddleBin, w.envs["homelab"], w.repos["homelab"],
		"setup", "--channel", twoReposChannel, "--as", "homelab", "--start")
	if code != 0 {
		t.Fatalf("setup --start = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	until(t, 20*time.Second, func() bool { return argvHealthy(w.hubURL) }, "the hub to answer its health path")
	join := regexp.MustCompile(`/huddle:join (\S+) --token (\S+)`).FindStringSubmatch(out)
	if join == nil {
		t.Fatalf("setup printed no join line:\n%s", out)
	}
	for _, name := range []string{"proxbeam", "nas"} {
		settings := `{"channel":"` + twoReposChannel + `","as":"` + name + `","autostart":false}` + "\n"
		dir := filepath.Join(w.repos[name], ".agents", "huddle")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "huddle.json"), []byte(settings), 0o644); err != nil {
			t.Fatal(err)
		}
		code, out, errOut := runBin(t, huddleBin, w.envs[name], w.repos[name], "join", join[1], "--token", join[2], "--as", name)
		if code != 0 {
			t.Fatalf("%s join = %d\nstdout:\n%s\nstderr:\n%s", name, code, out, errOut)
		}
	}
	for _, name := range []string{"homelab", "proxbeam", "nas"} {
		stdin := `{"hook_event_name":"SessionStart","session_id":"` + idleWakeSession(name) + `","source":"startup","cwd":` + strconv.Quote(w.repos[name]) + `}`
		if code, out, errOut := hooksRun(t, w.envs[name], w.repos[name], stdin, "hook", "session-start"); code != 0 {
			t.Fatalf("the %s session-start hook = %d\nstdout:\n%s\nstderr:\n%s", name, code, out, errOut)
		}
	}
	return w
}

// run runs one huddle word as a member and answers its stdout; a failure fails the test.
func (w *idleWakeWorld) run(name string, args ...string) string {
	w.t.Helper()
	code, out, errOut := runBin(w.t, huddleBin, w.envs[name], w.repos[name], args...)
	if code != 0 {
		w.t.Fatalf("%s: huddle %v = %d\nstdout:\n%s\nstderr:\n%s", name, args, code, out, errOut)
	}
	return out
}

// pidFile is the waiter pid file a member's session keeps under the huddle home.
func (w *idleWakeWorld) pidFile(name string) string {
	return filepath.Join(w.hubHome, "run", "idle-wait", idleWakeSession(name)+".json")
}

// idleWaiter is one `huddle hook idle-wait` child, started as Claude Code starts the Stop hook.
type idleWaiter struct {
	cmd    *exec.Cmd
	done   chan struct{}
	mu     sync.Mutex
	stdout bytes.Buffer
	stderr bytes.Buffer
}

// stopHookInput is the JSON Claude Code gives the Stop hook for a member's session.
func (w *idleWakeWorld) stopHookInput(name string) string {
	return `{"hook_event_name":"Stop","session_id":"` + idleWakeSession(name) + `","stop_hook_active":false,"cwd":` + strconv.Quote(w.repos[name]) + `}`
}

// startWaiter starts the idle-wait hook for a member, with extraEnv on top of the member's
// environment, and registers its stop: a waiter still running at the end of the test gets
// SIGTERM, then SIGKILL.
func (w *idleWakeWorld) startWaiter(name, stdin string, extraEnv ...string) *idleWaiter {
	w.t.Helper()
	waiter := &idleWaiter{done: make(chan struct{})}
	waiter.cmd = exec.Command(huddleBin, "hook", "idle-wait")
	waiter.cmd.Env = append(argvEntries(w.envs[name]), extraEnv...)
	waiter.cmd.Dir = w.repos[name]
	waiter.cmd.Stdin = strings.NewReader(stdin)
	waiter.cmd.Stdout = &lockedWriter{waiter, &waiter.stdout}
	waiter.cmd.Stderr = &lockedWriter{waiter, &waiter.stderr}
	if err := waiter.cmd.Start(); err != nil {
		w.t.Fatalf("start the %s waiter: %v", name, err)
	}
	go func() {
		_ = waiter.cmd.Wait()
		close(waiter.done)
	}()
	w.t.Cleanup(func() {
		select {
		case <-waiter.done:
			return
		default:
		}
		_ = waiter.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-waiter.done:
		case <-time.After(5 * time.Second):
			_ = waiter.cmd.Process.Kill()
			<-waiter.done
			w.t.Errorf("the %s waiter did not stop on SIGTERM", name)
		}
	})
	return waiter
}

// lockedWriter writes to one of a waiter's buffers under its lock.
type lockedWriter struct {
	waiter *idleWaiter
	buf    *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.waiter.mu.Lock()
	defer l.waiter.mu.Unlock()
	return l.buf.Write(p)
}

// exited answers whether the waiter ended within d, and its exit code.
func (waiter *idleWaiter) exited(d time.Duration) (bool, int) {
	select {
	case <-waiter.done:
		return true, waiter.cmd.ProcessState.ExitCode()
	case <-time.After(d):
		return false, 0
	}
}

// output answers what the waiter wrote so far.
func (waiter *idleWaiter) output() (stdout, stderr string) {
	waiter.mu.Lock()
	defer waiter.mu.Unlock()
	return waiter.stdout.String(), waiter.stderr.String()
}

// waitForPidFile answers the pid a member's pid file names once it holds one other than not.
func (w *idleWakeWorld) waitForPidFile(name string, not int) int {
	w.t.Helper()
	pid := 0
	until(w.t, 10*time.Second, func() bool {
		raw, err := os.ReadFile(w.pidFile(name))
		if err != nil {
			return false
		}
		match := regexp.MustCompile(`"pid":(\d+)`).FindStringSubmatch(string(raw))
		if match == nil {
			return false
		}
		pid, _ = strconv.Atoi(match[1])
		return pid != not
	}, "the "+name+" waiter's pid file")
	return pid
}

// waitUntilListening gives a started waiter time to open its stream: its pid file is written
// before the first poll, and the stream follows within a moment.
func (w *idleWakeWorld) waitUntilListening(name string) {
	w.t.Helper()
	w.waitForPidFile(name, 0)
	time.Sleep(500 * time.Millisecond)
}

func TestE2EIdleWakeAnAskFromTheOtherRepoWakesTheWaiter(t *testing.T) {
	w := newIdleWakeWorld(t)
	waiter := w.startWaiter("homelab", w.stopHookInput("homelab"))
	w.waitUntilListening("homelab")

	w.run("proxbeam", "send", "homelab", "PING 1", "--ask")

	ended, code := waiter.exited(3 * time.Second)
	_, stderr := waiter.output()
	if !ended {
		t.Fatalf("the waiter did not exit within 3 s of the ask; stderr:\n%s", stderr)
	}
	if code != 2 {
		t.Errorf("the waiter exited %d, want 2; stderr:\n%s", code, stderr)
	}
	if !regexp.MustCompile(`proxbeam asks: PING 1 \(reply with: huddle reply \d+ "<text>"\)`).MatchString(stderr) {
		t.Errorf("the waiter's stderr = %q, want the ask with how to reply", stderr)
	}
	if stdout, _ := waiter.output(); stdout != "" {
		t.Errorf("the waiter wrote %q to stdout, want nothing", stdout)
	}
	if _, err := os.Stat(w.pidFile("homelab")); !os.IsNotExist(err) {
		t.Errorf("the waiter left its pid file behind: %v", err)
	}
}

func TestE2EIdleWakeASessionThatNeverJoinsEndsItsWaitInSilence(t *testing.T) {
	w := newIdleWakeWorld(t)
	stranger := twoReposRepo(t, filepath.Join(w.root, "user"), w.root, "stranger")
	env := argvLayer(w.envs["homelab"], map[string]string{
		"CLAUDE_PROJECT_DIR": stranger, "CLAUDE_CODE_SESSION_ID": "sess-stranger",
		// the wait for a join lasts as long as the cycle, here two seconds
		"HUDDLE_IDLE_WAIT_SECONDS": "2",
	})
	started := time.Now()
	code, out, errOut := hooksRun(t, env, stranger, `{"hook_event_name":"Stop","session_id":"sess-stranger"}`, "hook", "idle-wait")
	if code != 0 || out != "" || errOut != "" {
		t.Errorf("exit %d, stdout %q, stderr %q, want 0 and silence", code, out, errOut)
	}
	if took := time.Since(started); took > 6*time.Second {
		t.Errorf("a session that never joined held the hook for %s, want it back after its 2 s wait", took)
	}
	if _, err := os.Stat(filepath.Join(w.hubHome, "run", "idle-wait", "sess-stranger.json")); !os.IsNotExist(err) {
		t.Errorf("a session that never joined got a pid file: %v", err)
	}
}

func TestE2EIdleWakeAMessageToTheWholeChannelWakesTheWaiter(t *testing.T) {
	w := newIdleWakeWorld(t)
	waiter := w.startWaiter("homelab", w.stopHookInput("homelab"))
	w.waitUntilListening("homelab")

	w.run("proxbeam", "send", "build is green")

	ended, code := waiter.exited(3 * time.Second)
	_, stderr := waiter.output()
	if !ended || code != 2 || !strings.Contains(stderr, "proxbeam says to the channel: build is green") {
		t.Fatalf("ended %v, exit %d, stderr %q, want exit 2 with the broadcast within 3 s", ended, code, stderr)
	}
}

func TestE2EIdleWakeOverheardChatterNeverWakesTheWaiter(t *testing.T) {
	w := newIdleWakeWorld(t)
	waiter := w.startWaiter("homelab", w.stopHookInput("homelab"))
	w.waitUntilListening("homelab")

	w.run("proxbeam", "send", "nas", "the pool is mounted")
	w.run("proxbeam", "remember", "fact", "NAS VLAN", "the NAS is on VLAN 20")

	if ended, code := waiter.exited(3 * time.Second); ended {
		_, stderr := waiter.output()
		t.Fatalf("the waiter exited %d on chatter meant for others; stderr:\n%s", code, stderr)
	}
	_ = waiter.cmd.Process.Signal(syscall.SIGTERM)
	if ended, code := waiter.exited(5 * time.Second); !ended || code != 0 {
		t.Errorf("the waiter ended = %v with code %d after SIGTERM, want a quiet exit 0", ended, code)
	}
	if stdout, stderr := waiter.output(); stdout != "" || stderr != "" {
		t.Errorf("the waiter printed stdout %q, stderr %q, want nothing", stdout, stderr)
	}
}

func TestE2EIdleWakeAHubRestartMidWaitStillWakes(t *testing.T) {
	w := newIdleWakeWorld(t)
	waiter := w.startWaiter("homelab", w.stopHookInput("homelab"))
	w.waitUntilListening("homelab")

	w.run("homelab", "down")
	until(t, 10*time.Second, func() bool { return !argvHealthy(w.hubURL) }, "the hub to go down")
	if ended, _ := waiter.exited(1500 * time.Millisecond); ended {
		_, stderr := waiter.output()
		t.Fatalf("the waiter ended while the hub was down; stderr:\n%s", stderr)
	}
	w.run("homelab", "up")
	until(t, 20*time.Second, func() bool { return argvHealthy(w.hubURL) }, "the hub to come back")

	w.run("proxbeam", "send", "homelab", "PING after restart", "--ask")

	ended, code := waiter.exited(30 * time.Second)
	_, stderr := waiter.output()
	if !ended || code != 2 || !strings.Contains(stderr, "proxbeam asks: PING after restart") {
		t.Fatalf("after the restart: ended %v, exit %d, stderr %q, want exit 2 with the ask", ended, code, stderr)
	}
}

func TestE2EIdleWakeASessionStartedAndNeverPromptedStillWakes(t *testing.T) {
	w := newIdleWakeWorld(t)
	// the SessionStart line starts the waiter; the session never reaches a Stop
	input := `{"hook_event_name":"SessionStart","session_id":"` + idleWakeSession("homelab") + `","source":"resume","cwd":` + strconv.Quote(w.repos["homelab"]) + `}`
	waiter := w.startWaiter("homelab", input)
	w.waitUntilListening("homelab")

	w.run("proxbeam", "send", "homelab", "PING for a session nobody prompted", "--ask")

	ended, code := waiter.exited(3 * time.Second)
	_, stderr := waiter.output()
	if !ended || code != 2 || !strings.Contains(stderr, "proxbeam asks: PING for a session nobody prompted") {
		t.Fatalf("ended %v, exit %d, stderr %q, want exit 2 with the ask within 3 s", ended, code, stderr)
	}
}

func TestE2EIdleWakeASecondWaiterForTheSameSessionReplacesTheFirst(t *testing.T) {
	w := newIdleWakeWorld(t)
	first := w.startWaiter("homelab", w.stopHookInput("homelab"))
	firstPid := w.waitForPidFile("homelab", 0)
	if firstPid != first.cmd.Process.Pid {
		t.Fatalf("the pid file names %d, want the first waiter's %d", firstPid, first.cmd.Process.Pid)
	}

	second := w.startWaiter("homelab", w.stopHookInput("homelab"))
	secondPid := w.waitForPidFile("homelab", firstPid)
	if secondPid != second.cmd.Process.Pid {
		t.Fatalf("the pid file names %d, want the second waiter's %d", secondPid, second.cmd.Process.Pid)
	}

	if ended, code := first.exited(5 * time.Second); !ended || code != 0 {
		t.Fatalf("the first waiter ended = %v with code %d, want it stopped with exit 0", ended, code)
	}
	if err := syscall.Kill(firstPid, 0); err == nil {
		t.Errorf("the first waiter (pid %d) still runs", firstPid)
	}
	if stdout, stderr := first.output(); stdout != "" || stderr != "" {
		t.Errorf("the replaced waiter printed stdout %q, stderr %q, want nothing", stdout, stderr)
	}
	if ended, _ := second.exited(500 * time.Millisecond); ended {
		t.Error("the second waiter ended with the first")
	}
}

func TestE2EIdleWakeAnIdleSessionKeepsListeningPastOneFullCycle(t *testing.T) {
	w := newIdleWakeWorld(t)
	// a two-second cycle stands for the hook's timeout
	short := "HUDDLE_IDLE_WAIT_SECONDS=2"
	first := w.startWaiter("homelab", w.stopHookInput("homelab"), short)

	// the cycle ends with the keepalive: exit 2, one line that asks for nothing
	ended, code := first.exited(15 * time.Second)
	_, stderr := first.output()
	if !ended || code != 2 || stderr != "huddle: still listening in homelab; no new messages, no reply needed.\n" {
		t.Fatalf("the first cycle: ended %v, exit %d, stderr %q, want exit 2 with the keepalive line", ended, code, stderr)
	}
	if _, err := os.Stat(w.pidFile("homelab")); !os.IsNotExist(err) {
		t.Errorf("the first waiter left its pid file behind: %v", err)
	}

	// the session answers the keepalive, its turn ends, the Stop hook starts the next waiter;
	// a message sent in the gap between the two is still delivered
	w.run("proxbeam", "send", "homelab", "PING sent in the gap", "--ask")
	second := w.startWaiter("homelab", w.stopHookInput("homelab"), short)
	ended, code = second.exited(3 * time.Second)
	_, stderr = second.output()
	if !ended || code != 2 || !strings.Contains(stderr, "proxbeam asks: PING sent in the gap") {
		t.Fatalf("the second waiter: ended %v, exit %d, stderr %q, want exit 2 with the message from the gap", ended, code, stderr)
	}

	// the session answered without a tool call, so the feed never moved; the third waiter
	// must not repeat the gap message, and wakes on the next one
	third := w.startWaiter("homelab", w.stopHookInput("homelab"))
	w.waitUntilListening("homelab")
	w.run("proxbeam", "send", "homelab", "PING after the cycle", "--ask")
	ended, code = third.exited(3 * time.Second)
	_, stderr = third.output()
	if !ended || code != 2 || !strings.Contains(stderr, "proxbeam asks: PING after the cycle") || strings.Contains(stderr, "sent in the gap") {
		t.Fatalf("the third waiter: ended %v, exit %d, stderr %q, want exit 2 with only the new message", ended, code, stderr)
	}
}
