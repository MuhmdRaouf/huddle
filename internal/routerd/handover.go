// The daemon's handover: a running daemon serves its listening socket to a taker
// over a unix socket in the run dir, so a new binary — or a fresh copy of the same one — takes
// the port over without it ever closing, and the old process drains the streams it already
// accepted. Takeover is the taker's side; the ceiling that makes a daemon hand itself over is
// the rss watch.

package routerd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/fsutil"
	"github.com/MuhmdRaouf/huddle/internal/adapters/process"
	"github.com/MuhmdRaouf/huddle/internal/adapters/safego"
	"github.com/MuhmdRaouf/huddle/internal/adapters/singleton"
	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// handoverName is the unix socket in the run dir a takeover dials.
const handoverName = "handover.sock"

// answerWait bounds one takeover connection's question and its answer.
const answerWait = 5 * time.Second

// takeoverWait is how long a taker waits for the old daemon's answer.
const takeoverWait = 5 * time.Second

// drainBudget is how long a daemon that handed its socket over keeps the streams it already
// accepted before it gives up on them (D9's ten minutes).
const drainBudget = 10 * time.Minute

// takeoverLockWait is how long a takeover start waits for the router lock the previous daemon
// still holds while it drains: the drain's budget plus a minute of margin.
const takeoverLockWait = 11 * time.Minute

// rssInterval is how often a serving daemon looks at its own resident size.
const rssInterval = 30 * time.Second

// rssIntervalOf is how often a daemon looks at its own resident size: thirty seconds, or
// $HUDDLE_ROUTER_RSS_INTERVAL_MS when it names a positive number (tests only).
func rssIntervalOf(env domain.EnvLookup) time.Duration {
	if raw := env["HUDDLE_ROUTER_RSS_INTERVAL_MS"]; raw != "" {
		if ms, err := strconv.Atoi(raw); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return rssInterval
}

// rssCeilingDefault is the resident size over which a daemon hands itself over to a fresh
// copy of its own binary (D9's one GiB).
const rssCeilingDefault = 1 << 30

// serveHandover listens on <home>/run/handover.sock while the daemon serves and answers one
// takeover per connection (the taker's `takeover <pid>` line) with the listening TCP socket's
// file descriptor plus one `ok <port>` line, retires the socket file at once so the taker can
// open its own, and names the taker on handed for Run's drain. A socket that cannot be opened
// — a run dir whose path is past the OS's sun_path limit — is logged and skipped: the extra
// never stops the router. The returned stop closes the socket and removes its file, unless a
// handover retired it already; the caller runs it when serving ends.
func serveHandover(home string, port int, listener net.Listener, handed chan<- int, log func(line string)) (stop func()) {
	tcp, ok := listener.(*net.TCPListener)
	if !ok {
		log("huddle-router: handover socket: the listener is not a TCP socket")
		return func() {}
	}
	path := filepath.Join(home, "run", handoverName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		log("huddle-router: handover socket: " + fsutil.ErrorMessage(err))
		return func() {}
	}
	_ = os.Remove(path) // a stale file from a killed daemon never blocks the listen
	unix, err := net.Listen("unix", path)
	if err != nil {
		log("huddle-router: handover socket: " + fsutil.ErrorMessage(err))
		return func() {}
	}
	if err := os.Chmod(path, 0o600); err != nil {
		log("huddle-router: handover socket: " + fsutil.ErrorMessage(err))
	}
	retire := func() {
		_ = unix.Close()
		_ = os.Remove(path)
	}
	var once sync.Once
	safego.Go(log, func() {
		for {
			conn, err := unix.Accept()
			if err != nil {
				return
			}
			safego.Go(log, func() {
				if taker, ok := answerHandover(conn, tcp, port, log); ok {
					once.Do(func() {
						retire()
						handed <- taker
					})
				}
			})
		}
	})
	// a daemon that handed over retired its file already, and the path now names the taker's
	// socket: the once keeps this stop, which runs when the drain ends, off it
	return func() { once.Do(retire) }
}

// answerHandover is one takeover connection's exchange: read the taker's line, hand the
// listening socket over as a file descriptor, answer `ok <port>`. It answers the taker's pid
// and true when the socket changed hands; anything else closes and keeps serving, because a
// taker that cannot complete never stops the daemon.
func answerHandover(conn net.Conn, tcp *net.TCPListener, port int, log func(line string)) (int, bool) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(answerWait))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return 0, false
	}
	taker, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "takeover ")))
	if err != nil {
		return 0, false
	}
	unix, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	file, err := tcp.File()
	if err != nil {
		log("huddle-router: handover: " + fsutil.ErrorMessage(err))
		return 0, false
	}
	defer file.Close()
	// the descriptor rides the answer raw: Fd would set the shared open file description back
	// to blocking under the serving listener's feet
	raw := -1
	if control, err := file.SyscallConn(); err == nil {
		_ = control.Control(func(fd uintptr) { raw = int(fd) })
	}
	if raw < 0 {
		log("huddle-router: handover: no descriptor for the listening socket")
		return 0, false
	}
	if _, _, err := unix.WriteMsgUnix([]byte(fmt.Sprintf("ok %d\n", port)), syscall.UnixRights(raw), nil); err != nil {
		log("huddle-router: handover: " + fsutil.ErrorMessage(err))
		return 0, false
	}
	return taker, true
}

// Takeover is the taker's side of the handover: it dials the home's handover socket, asks with
// one `takeover <pid>` line, and reads the listening socket back as a file descriptor with one
// `ok <port>` line. A daemon that is not there, one that never answers within five seconds and
// a useless answer are errors; the caller falls back to the normal start.
func Takeover(ctx context.Context, home string) (net.Listener, error) {
	path := filepath.Join(home, "run", handoverName)
	bounded, cancel := context.WithTimeout(ctx, takeoverWait)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(bounded, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("no daemon to take over from at %s (%s)", path, fsutil.ErrorMessage(err))
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(takeoverWait))
	if _, err := fmt.Fprintf(conn, "takeover %d\n", os.Getpid()); err != nil {
		return nil, fmt.Errorf("the handover socket at %s: %s", path, fsutil.ErrorMessage(err))
	}
	buffer := make([]byte, 128)
	ooo := make([]byte, 128)
	n, oobn, _, _, err := conn.(*net.UnixConn).ReadMsgUnix(buffer, ooo)
	if err != nil {
		return nil, fmt.Errorf("the daemon at %s never handed over within %s (%s)", path, takeoverWait, fsutil.ErrorMessage(err))
	}
	fds, err := parseRights(ooo[:oobn])
	if err != nil {
		return nil, err
	}
	if len(fds) != 1 {
		for _, fd := range fds {
			_ = syscall.Close(fd)
		}
		return nil, fmt.Errorf("the daemon at %s handed over %d sockets, want one", path, len(fds))
	}
	// the line's port is only checked: the taken listener carries the port in its own address
	if _, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(string(buffer[:n]), "ok "))); err != nil {
		_ = syscall.Close(fds[0])
		return nil, fmt.Errorf("the daemon at %s answered %q", path, strings.TrimSpace(string(buffer[:n])))
	}
	file := os.NewFile(uintptr(fds[0]), "huddle-router")
	listener, err := net.FileListener(file)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("the handed-over socket: %s", fsutil.ErrorMessage(err))
	}
	return listener, nil
}

// parseRights reads the file descriptors out of one SCM_RIGHTS answer.
func parseRights(ooo []byte) ([]int, error) {
	messages, err := syscall.ParseSocketControlMessage(ooo)
	if err != nil {
		return nil, fmt.Errorf("the handover answer: %s", err)
	}
	var fds []int
	for _, message := range messages {
		right, err := syscall.ParseUnixRights(&message)
		if err != nil {
			return nil, fmt.Errorf("the handover answer: %s", err)
		}
		fds = append(fds, right...)
	}
	return fds, nil
}

// tunnels is the router's CONNECT tunnels, which a server's Shutdown never waits on: the
// connections are hijacked.
type tunnels interface {
	Drain(ctx context.Context) error
}

// handOver is the daemon's answer to a taker: the listening socket is the taker's now, so
// Shutdown closes the daemon's own copy of it (the taker's dup keeps the port open) and lets the
// requests in flight finish, the tunnels drain after them, all within ten minutes, and the run
// returns nil. The lock's release is Run's defer, and the record stays for the taker to
// overwrite.
func handOver(server *http.Server, open tunnels, taker int, log func(line string)) error {
	log(fmt.Sprintf("huddle-router: handed over to pid %d; draining", taker))
	budget, cancel := context.WithTimeout(context.Background(), drainBudget)
	defer cancel()
	if err := server.Shutdown(budget); err != nil {
		log("huddle-router: " + err.Error())
	}
	if err := open.Drain(budget); err != nil {
		log("huddle-router: tunnels: " + err.Error())
	}
	return nil
}

// holdRouterLock takes the router lock when the previous holder lets go — up to eleven minutes
// of draining — and holds it until done closes: a takeover start serves the port before it can
// prove it is the one daemon, and the lock catches up with it.
func holdRouterLock(path string, done <-chan struct{}, log func(line string)) {
	defer func() { _ = recover() }()
	release, err := singleton.Wait(path, router.MultiName, takeoverLockWait)
	if err != nil {
		log("huddle-router: " + err.Error())
		return
	}
	<-done
	release()
}

// checkRSS is the memory ceiling (D9): a daemon whose resident size passes the ceiling hands
// itself over to a fresh copy of its own binary — spawned once per try, and a try that no
// takeover answers within one interval is logged, so the next check tries again. It answers the
// spawn time the next check waits from.
func checkRSS(ceiling int64, spawned time.Time, env domain.EnvLookup, log func(line string)) time.Time {
	rss, err := rssOf(os.Getpid())
	if err != nil {
		log("huddle-router: RSS: " + fsutil.ErrorMessage(err))
		return spawned
	}
	if rss <= ceiling {
		return spawned
	}
	interval := rssIntervalOf(env)
	if !spawned.IsZero() {
		if time.Since(spawned) < interval {
			return spawned
		}
		log("huddle-router: no takeover arrived within " + interval.String() + "; trying again at the next check")
	}
	log(fmt.Sprintf("huddle-router: RSS %d MiB over the ceiling; handing over", rss>>20))
	logFile, err := startLog(env)
	if err != nil {
		log("huddle-router: " + err.Error())
		return time.Now()
	}
	defer logFile.Close()
	if err := spawnServeArgs(selfExecutable(env), env, logFile, "--takeover"); err != nil {
		log("huddle-router: " + err.Error())
	}
	return time.Now()
}

// rssCeilingOf is the resident size a daemon hands itself over at: the options', else
// $HUDDLE_ROUTER_RSS_CEILING_MB in MiB, else one GiB.
func rssCeilingOf(env domain.EnvLookup, o Options) int64 {
	if o.RSSCeiling > 0 {
		return o.RSSCeiling
	}
	if raw := env["HUDDLE_ROUTER_RSS_CEILING_MB"]; raw != "" {
		if mb, err := strconv.ParseInt(raw, 10, 64); err == nil && mb > 0 {
			return mb << 20
		}
	}
	return rssCeilingDefault
}

// rssOf reads one process's resident size in bytes: ps reports rss in KiB on macOS and Linux.
func rssOf(pid int) (int64, error) {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, err
	}
	return parseRSS(string(out))
}

// parseRSS reads ps's rss column (KiB, one line per match) into bytes.
func parseRSS(out string) (int64, error) {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		kiB, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("ps printed %q, not an rss", line)
		}
		return kiB << 10, nil
	}
	return 0, errors.New("ps printed no rss")
}

// selfExecutable is a binary that exists now, for a daemon that spawns its own replacement: a
// build the launcher has since deleted would fail the spawn, so the launcher's `current` link
// names the binary to hand over to.
func selfExecutable(env domain.EnvLookup) string {
	return process.LiveExecutable(env)
}

// runningExecutable is the binary this process runs from, named resolved through its symlinks
// when that still resolves (a deleted build keeps its own name): the record must name the
// build itself, not a link every newer build shares, because a start compares it with its own
// binary to decide on an update, and a record naming the `current` link would hide an old
// daemon from the newer build behind it.
func runningExecutable() string {
	if self, err := os.Executable(); err == nil {
		return resolvedExecutable(self)
	}
	return os.Args[0]
}
