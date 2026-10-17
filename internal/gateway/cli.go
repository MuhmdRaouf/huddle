// The gateway command line: `huddle gateway` serves in the foreground; `start`, `stop` and
// `status` run the detached server's lifecycle, the way radar's and kanban's own commands do.
// Exit codes: 0 ok, 1 a start that never became healthy or a config that does not hold up,
// 2 usage, 3 a status that found nothing running.
package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/process"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// The budgets the lifecycle keeps: a detached child has three seconds to listen, and a health
// probe decides within two.
const (
	startWaitMs = 3_000
	startPollMs = 100
)

// usage is the one line every misuse prints.
const usage = "usage: huddle gateway [start|stop|status]"

// gatewayProcess matches the command line of this binary serving the gateway — the bare word a
// detached start leaves behind, after the huddle binary or as a gateway binary of its own. A
// stale record may name any process, so only a gateway is ours to signal.
var gatewayProcess = regexp.MustCompile(`(?:^|/)(?:huddle )?gateway(?:[ \n]|$)`)

// RunOptions carries what a real run reads from the world; the tests set the parts they fake.
type RunOptions struct {
	Env domain.EnvLookup
	// Signal ends a foreground run; nil means the run ends only through its own failure.
	Signal <-chan os.Signal
	// The lifecycle's effects, each with a real default.
	SpawnDetached func(args []string) int64
	Deliver       func(pid int64) bool
	Command       func(pid int64) (string, bool)
	IsAlive       func(pid int64) bool
	FetchHealth   func(url string) bool
	Sleep         func(ms int64)
	Now           func() int64
}

// runIO is what one command run reads and writes.
type runIO struct {
	env     domain.EnvLookup
	stdout  io.Writer
	stderr  io.Writer
	signal  <-chan os.Signal
	spawn   func(args []string) int64
	deliver func(pid int64) bool
	command func(pid int64) (string, bool)
	isAlive func(pid int64) bool
	fetch   func(url string) bool
	sleep   func(ms int64)
	now     func() int64
}

// Run runs one gateway command line and answers the process exit code.
func Run(args []string, stdout, stderr io.Writer, opts RunOptions) int {
	run := &runIO{
		env:     opts.Env,
		stdout:  stdout,
		stderr:  stderr,
		signal:  opts.Signal,
		spawn:   opts.SpawnDetached,
		deliver: opts.Deliver,
		command: opts.Command,
		isAlive: opts.IsAlive,
		fetch:   opts.FetchHealth,
		sleep:   opts.Sleep,
		now:     opts.Now,
	}
	// the defaults a real run uses, left in place wherever the options said nothing
	if run.spawn == nil {
		run.spawn = spawnDetached(run.env)
	}
	if run.deliver == nil {
		run.deliver = func(pid int64) bool { return process.SignalGroup(pid, syscall.SIGTERM) }
	}
	if run.command == nil {
		run.command = commandOf
	}
	if run.isAlive == nil {
		run.isAlive = process.IsAlive
	}
	if run.fetch == nil {
		run.fetch = fetchHealth
	}
	if run.sleep == nil {
		run.sleep = func(ms int64) { time.Sleep(time.Duration(ms) * time.Millisecond) }
	}
	if run.now == nil {
		run.now = func() int64 { return time.Now().UnixMilli() }
	}

	command := ""
	if len(args) > 0 {
		command = args[0]
	}
	switch command {
	case "":
		config, err := LoadConfig(run.env)
		if err != nil {
			run.errf("%v", err)
			return 1
		}
		return serve(context.Background(), run, config)
	case "start":
		return run.start()
	case "stop":
		return run.stop()
	case "status":
		return run.status()
	case "help", "--help", "-h":
		run.outf(usage)
		run.outf("gateway serves in the foreground; start runs it detached and writes $HUDDLE_HOME/gateway.port")
		return 0
	default:
		run.errf("gateway: unknown command: %s", command)
		run.errf(usage)
		return 2
	}
}

// start brings the gateway up: a record whose server still answers health is reused, anything
// else is replaced by a detached bare run whose files tell the caller when it listens.
func (run *runIO) start() int {
	if _, err := LoadConfig(run.env); err != nil {
		run.errf("%v", err)
		return 1
	}
	root, err := resolveHome(run.env)
	if err != nil {
		run.errf("gateway: %v", err)
		return 1
	}
	if state := readState(root); state != nil {
		if run.isAlive(state.Pid) && run.fetch(healthURL(state.Port)) {
			run.outf("gateway: %s", gatewayURL(state.Port))
			return 0
		}
		removeState(root)
	}
	if run.spawn([]string{"gateway"}) == 0 {
		run.errf("gateway: could not start the detached run; its log is in the home")
		return 1
	}
	deadline := run.now() + startWaitMs
	for run.now() < deadline {
		run.sleep(startPollMs)
		port, ok := readPortFile(root)
		if !ok {
			continue // the child writes the file once it listens
		}
		if run.fetch(healthURL(port)) {
			run.outf("gateway: %s", gatewayURL(port))
			return 0
		}
	}
	run.errf("gateway: did not become healthy within %d s", startWaitMs/1000)
	return 1
}

// stop ends the gateway the record names and forgets the record; nothing recorded is a no-op.
// The pid is signalled only when its command line is a gateway: a reboot may have given the pid
// to any other process, which is not ours to signal.
func (run *runIO) stop() int {
	root, err := resolveHome(run.env)
	if err != nil {
		run.errf("gateway: %v", err)
		return 1
	}
	state := readState(root)
	if state == nil {
		run.outf("gateway: not running")
		return 0
	}
	if run.isAlive(state.Pid) {
		if command, ok := run.command(state.Pid); ok && gatewayProcess.MatchString(command) {
			run.deliver(state.Pid)
		}
	}
	removeState(root)
	run.outf("gateway: stopped")
	return 0
}

// status answers where the gateway answers; nothing running is exit 3.
func (run *runIO) status() int {
	root, err := resolveHome(run.env)
	if err != nil {
		run.errf("gateway: %v", err)
		return 1
	}
	state := readState(root)
	if state == nil || !run.isAlive(state.Pid) || !run.fetch(healthURL(state.Port)) {
		run.outf("gateway: not running")
		return 3
	}
	run.outf("gateway: %s", gatewayURL(state.Port))
	return 0
}

// healthURL is the one path the gateway answers itself, the probe a status follows.
func healthURL(port int) string {
	return gatewayURL(port) + "/health"
}

// fetchHealth answers whether a gateway answers health on port, with a two-second budget.
func fetchHealth(url string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		return false
	}
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode == http.StatusOK
}

// spawnDetached starts this binary again as a bare `gateway`, this environment riding along and
// the run's log keeping its output.
func spawnDetached(env domain.EnvLookup) func(args []string) int64 {
	return func(args []string) int64 {
		self, err := os.Executable()
		if err != nil {
			return 0
		}
		root, err := resolveHome(env)
		if err != nil {
			return 0
		}
		return process.StartDetached(self, args, process.DetachedOptions{
			Env:     os.Environ(),
			LogFile: filepath.Join(root, logFile),
		}).Pid
	}
}

// commandOf answers a pid's command line as `ps -o command=` prints it; false when ps cannot
// name the pid. The probe is bounded, so a wedged ps never holds the stop.
func commandOf(pid int64) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "command=", "-p", strconv.FormatInt(pid, 10)).Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

func (run *runIO) outf(format string, args ...any) {
	fmt.Fprintf(run.stdout, format+"\n", args...)
}

func (run *runIO) errf(format string, args ...any) {
	fmt.Fprintf(run.stderr, format+"\n", args...)
}
