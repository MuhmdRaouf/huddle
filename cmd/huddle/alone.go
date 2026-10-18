// The binary's own words, for a machine that runs it without Claude Code (SPEC 10): up, down,
// status, open and pair run the one server of this user's home from a terminal, and help lists
// them. A Claude session keeps the CLI's same words — its up mints the join line, its status is
// the channel's — so each word hands the whole line to the CLI when a session identity is
// present; only a session-less invocation (a terminal, a hub machine) takes the paths here.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/outside"
)

// aloneEntry runs one of the binary's own words: argv is the whole command line (a flag keeps
// its place and its meaning wherever it stands around the word), and the returned int is the
// process exit code.
type aloneEntry = func(ctx context.Context, w *cli.World, argv []string, stdout, stderr io.Writer) int

// aloneWords holds one function per word the binary owns outright. None of them is a plugin
// entry or a channel operation: a session invoking the same word gets the CLI's meaning.
var aloneWords = map[string]aloneEntry{
	"up":      upAlone,
	"down":    downAlone,
	"status":  statusAlone,
	"open":    openAlone,
	"pair":    pairAlone,
	"service": serviceAlone,
	"devices": devicesAlone,
	"role":    roleAlone,
	"cert":    certAlone,
	"trust":   trustAlone,
	"untrust": untrustAlone,
	"help":    helpAlone,
	"-h":      helpAlone,
}

// aloneRun takes the line when its command word is one of the binary's own. A session (a channel
// identity in the environment, the project's settings or a joined credential) keeps the CLI's
// words; the second answer says whether the word was ours to take.
func aloneRun(ctx context.Context, command string, argv []string, stdout, stderr io.Writer) (int, bool) {
	word, ok := aloneWords[command]
	if !ok {
		return 0, false
	}
	w := &cli.World{}
	id := cli.Identify(ctx, w, "")
	if id.Channel != "" || id.As != "" {
		return cli.Huddle(ctx, w, argv, stdout, stderr), true
	}
	return word(ctx, w, argv, stdout, stderr), true
}

// upAlone starts the server detached and prints the one line a script can read: where it
// answers. --port names a port of the caller's choosing, saved and never moved.
func upAlone(ctx context.Context, w *cli.World, argv []string, stdout, stderr io.Writer) int {
	url := cli.Identify(ctx, w, "").URL
	if port, ok := flagValue(argv, "--port"); ok {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			fmt.Fprintf(stderr, "huddle: --port %s: a port number\n", port)
			return 2
		}
		// a port of your choosing wins, and up saves it
		w = w.WithVar("HUDDLE_PORT", port).WithVar("HUDDLE_URL", "")
		url = cli.Local(n)
	}
	result := cli.UpAlone(ctx, w, url, 35_000)
	if !result.OK {
		fmt.Fprintf(stderr, "huddle: %s\n", result.Msg)
		return 5
	}
	fmt.Fprintf(stdout, "huddle is up at %s\n", result.URL)
	return 0
}

// statusAlone answers the four facts of the running server: its address, the machine's role, the
// port, the sessions its channels hold and the pid. Nothing running is exit 6, one line.
func statusAlone(ctx context.Context, w *cli.World, argv []string, stdout, stderr io.Writer) int {
	root := cli.Home(ctx, w)
	if root == "" {
		fmt.Fprintln(stderr, "huddle: no home: set HOME or HUDDLE_HOME")
		return 1
	}
	url, port, pid, running := aloneServer(ctx, w, root)
	if !running {
		fmt.Fprintln(stderr, "huddle is not running")
		return 6
	}
	role := "solo"
	if cfg, err := home.LoadConfig(home.Home{Root: root}); err == nil && cfg.Role != "" {
		role = cfg.Role
	}
	pidWord := "unknown"
	if pid > 0 {
		pidWord = strconv.Itoa(pid)
	}
	fmt.Fprintf(stdout, "huddle is up at %s\nrole %s · port %d · sessions %s · pid %s\n",
		url, role, port, aloneSessions(ctx, w, url), pidWord)
	return 0
}

// openAlone reaches the dashboard: a one-time sign-in link, opened in the machine's browser, or
// printed with --print (a terminal with no browser to open). No opener on the machine leaves the
// link printed, so the owner still gets in.
func openAlone(ctx context.Context, w *cli.World, argv []string, stdout, stderr io.Writer) int {
	root := cli.Home(ctx, w)
	if root == "" {
		fmt.Fprintln(stderr, "huddle: no home: set HOME or HUDDLE_HOME")
		return 1
	}
	url, _, _, running := aloneServer(ctx, w, root)
	if !running {
		fmt.Fprintln(stderr, "huddle is not running")
		return 6
	}
	link := cli.Dashboard(ctx, w, url, "", 1500)
	if link == "" {
		link = url + "/" // no credential to sign a browser in with: the dashboard says how to get one
	}
	if hasFlag(argv, "--print") {
		fmt.Fprintln(stdout, link)
		return 0
	}
	if err := browserOpen(ctx, link); err != nil {
		fmt.Fprintln(stdout, link)
	}
	return 0
}

// downAlone stops the server `huddle up` started and waits for it: the port is closed by the
// time the command answers. A server someone else started is named, not signalled.
func downAlone(ctx context.Context, w *cli.World, argv []string, stdout, stderr io.Writer) int {
	result := cli.Down(ctx, w, cli.Identify(ctx, w, "").URL)
	if !result.OK {
		fmt.Fprintf(stderr, "huddle: %s\n", result.Msg)
		return 1
	}
	fmt.Fprintln(stdout, lowerFirst(result.Msg))
	return 0
}

// pairAlone mints one pairing link from this trusted machine: a QR code to scan, the link, and
// the code both sides compare.
func pairAlone(ctx context.Context, w *cli.World, argv []string, stdout, stderr io.Writer) int {
	return cli.Pair(ctx, w, argv, stdout, stderr)
}

// devicesAlone lists the paired devices, or removes one by name.
func devicesAlone(ctx context.Context, w *cli.World, argv []string, stdout, stderr io.Writer) int {
	return cli.Devices(ctx, w, argv, stdout, stderr)
}

// roleAlone writes the machine's role into config.toml: solo, hub (with its listener and name) or
// satellite (with the hub it reaches).
func roleAlone(ctx context.Context, w *cli.World, argv []string, stdout, stderr io.Writer) int {
	return cli.Role(ctx, w, argv, stdout, stderr)
}

// certAlone obtains the hub's certificate over DNS-01 (SPEC 7.2): a terminal word, because its
// token is typed with the echo off — in a session the word stays the channel's own.
func certAlone(ctx context.Context, w *cli.World, argv []string, stdout, stderr io.Writer) int {
	return cli.Cert(ctx, w, argv, stdout, stderr)
}

// trustAlone and untrustAlone put huddle's local CA root into this machine's trust stores and
// take it out: terminal words, because the OS asks for the password itself. trust --phone serves
// its guide in the foreground for ten minutes, so the terminal's own interrupt ends it.
func trustAlone(ctx context.Context, w *cli.World, argv []string, stdout, stderr io.Writer) int {
	foreground, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return cli.Trust(foreground, w, argv, stdout, stderr)
}

func untrustAlone(ctx context.Context, w *cli.World, argv []string, stdout, stderr io.Writer) int {
	return cli.Untrust(ctx, w, argv, stdout, stderr)
}

// helpAlone lists the binary's entries, one line each, then the channel CLI's own list.
func helpAlone(ctx context.Context, w *cli.World, argv []string, stdout, stderr io.Writer) int {
	for _, line := range aloneHelpLines {
		fmt.Fprintln(stdout, line)
	}
	return cli.Huddle(ctx, w, []string{"help"}, stdout, stderr)
}

// aloneHelpLines is the block `huddle help` carries above the channel CLI's list: the binary's
// own entry points, one line each.
var aloneHelpLines = []string{
	"Huddle runs without Claude Code too — the same binary, the same home (~/.config/huddle):",
	"",
	"  huddle up [--port n]     start the server on this machine and leave it running; prints where it answers",
	"  huddle status            role, port, sessions and pid of the server; exit 6 when it is not running",
	"  huddle open [--print]    open the dashboard in your browser; --print prints the sign-in link",
	"  huddle down              stop the server huddle up started",
	"  huddle service install   run the server as the machine's service (launchd agent, systemd user unit)",
	"  huddle service uninstall remove that service",
	"  huddle pair [--name n]   print a one-use link and QR code that pairs a device with a passkey",
	"  huddle pair --satellite  print a one-use invite (10 min) that joins another machine as this hub's satellite",
	"  huddle join-hub <url> <invite> [--name n]   run on the satellite: stores its credential and sets its role",
	"  huddle devices [rm n]    list the paired devices; rm revokes one and ends its sessions",
	"  huddle role <role>       write the machine's role: solo; hub --listen <addr:port> --host <name>;",
	"                           satellite --hub https://<host>:<port> (a restart takes it up)",
	"  huddle cert --provider p obtain the hub's certificate over DNS-01 (asks for the provider's token",
	"                           with the echo off; the server renews it 30 days before expiry)",
	"  huddle cert --import c k install a pair you already hold (mkcert, a company CA) for the hub's",
	"                           name; it is never renewed — import a fresh one before it expires",
	"  huddle cert              say which certificate the hub serves — imported, ACME, or the local",
	"                           CA's leaf — and whether this machine trusts its root",
	"  huddle trust [--phone]    install the local CA's root so browsers accept the hub's https address",
	"                           (the OS asks for your password); huddle untrust removes it again;",
	"                           --phone instead prints a QR the phone scans for the same root",
	"                           (10 minutes, installs nothing here)",
	"  huddle serve             run the server in the foreground (the entry huddle up spawns)",
	"  huddle mcp               the stdio MCP bridge a Claude session runs",
	"  huddle hook <name>       one plugin hook entry: session-start, approve, listen, permit, stop",
	"  huddle help              this list (also -h)",
	"",
	"The channel CLI, for a session in a huddle:",
}

// aloneServer answers where the home's server answers (its url and port), its pid, and whether
// it answers at all: the saved port (or the running process's listener) must carry a healthy
// Huddle, never a pid file alone.
func aloneServer(ctx context.Context, w *cli.World, root string) (url string, port, pid int, running bool) {
	if choice, has := cli.SavedPort(root); has {
		port = choice.Port
	}
	pid = cli.PidOf(ctx, w)
	if port <= 0 && pid > 0 {
		port = cli.ListeningPort(ctx, w, pid)
	}
	if port <= 0 {
		return "", 0, pid, false
	}
	url = cli.Local(port)
	return url, port, pid, cli.Healthy(ctx, url, 1500)
}

// aloneSessions counts the sessions the home's channels hold, through the running server with
// this terminal's credential (the creator's). "unknown" when the call cannot be made — no
// credential, a refusal — rather than a count that looks counted.
func aloneSessions(ctx context.Context, w *cli.World, url string) string {
	resp, err := cli.HFetch(ctx, w, http.MethodGet, url+"/api/channels", nil, nil, 2*time.Second, "")
	if err != nil {
		return "unknown"
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "unknown"
	}
	var channels []struct {
		Sessions []struct{} `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&channels); err != nil {
		return "unknown"
	}
	n := 0
	for _, channel := range channels {
		n += len(channel.Sessions)
	}
	return strconv.Itoa(n)
}

// browserOpen hands one link to the machine's browser opener: `open` on macOS, `xdg-open` on
// Linux. Bounded, so a hung opener cannot hold the command.
func browserOpen(ctx context.Context, link string) error {
	withDeadline, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	return exec.CommandContext(withDeadline, name, link).Run()
}

// flagValue reads one --flag's value from a whole command line.
func flagValue(argv []string, name string) (string, bool) {
	for i := 0; i < len(argv); i++ {
		if argv[i] == name && i+1 < len(argv) {
			return argv[i+1], true
		}
	}
	return "", false
}

// hasFlag answers whether a bare --flag was given.
func hasFlag(argv []string, name string) bool {
	for _, arg := range argv {
		if arg == name {
			return true
		}
	}
	return false
}

// lowerFirst lowercases a line's first letter, the way the binary's own words speak ("huddle
// stopped" beside "huddle is up").
func lowerFirst(line string) string {
	if line == "" {
		return line
	}
	return strings.ToLower(line[:1]) + line[1:]
}

// serviceAlone installs or removes the machine's service: a launchd agent (macOS) or a systemd
// user unit (Linux) that runs the server in the foreground, for a hub machine with no Claude
// session open. The install records the unit file in the home's service ledger, so the plugin's
// own removal takes the service with it.
func serviceAlone(ctx context.Context, w *cli.World, argv []string, stdout, stderr io.Writer) int {
	root := cli.Home(ctx, w)
	if root == "" {
		fmt.Fprintln(stderr, "huddle: no home: set HOME or HUDDLE_HOME")
		return 1
	}
	// the service's log lands in the home's run directory, which may not exist yet on a machine
	// no server ever started
	if err := (home.Home{Root: root}).Ensure(ctx); err != nil {
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	bin, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	if abs, err := filepath.Abs(bin); err == nil {
		bin = abs
	}
	bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	options := outside.ServiceOptions{
		Home:   root,
		Bin:    bin,
		Env:    w.Getenv, // nil in a zero World: the service falls back to the process's own
		UID:    os.Getuid(),
		Runner: outside.CommandRunner{},
	}
	switch nextPlainWord(argv, "service") {
	case "install":
		record, err := outside.InstallService(bounded, options)
		if err != nil {
			fmt.Fprintf(stderr, "huddle: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "huddle service installed: %s\n", record.Unit)
		return 0
	case "uninstall":
		if err := outside.UninstallService(bounded, options); err != nil {
			fmt.Fprintf(stderr, "huddle: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "huddle service removed")
		return 0
	default:
		fmt.Fprintln(stderr, "usage: huddle service install | uninstall")
		return 2
	}
}

// nextPlainWord reads the first plain word after the command word, the way splitCommand found the
// command itself: a --flag takes the next word as its value unless that is a flag too.
func nextPlainWord(argv []string, command string) string {
	past := false
	for i := 0; i < len(argv); i++ {
		if strings.HasPrefix(argv[i], "--") {
			if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "--") {
				i++
			}
			continue
		}
		if !past {
			past = argv[i] == command
			continue
		}
		return argv[i]
	}
	return ""
}
