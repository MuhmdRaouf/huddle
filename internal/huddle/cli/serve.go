// Running the Huddle server that ships inside this binary:
//
//	huddle up       start it in the background, on loopback; a hub an older build serves is
//	                replaced, a hub this binary serves is left answering
//	huddle down     stop the server `huddle up` started
//	huddle server   is it up, which process, where its data and log are
//
// Its files live in this user's home (identity's HomeOf; the home package resolves it):
// channels/, auth.json (under HUDDLE_DATA when that is set) and run/huddle.pid, run/huddle.port,
// run/huddle.log, run/owner.key — one line in the log per start and stop, with its reason.
// `huddle up` lays the home out first (home.Ensure). The server a session's up starts watches for
// the plugin's removal (the outside package) and stops itself, taking the run files with it and
// leaving a note by the channels; UpAlone, the start without Claude Code, carries no watch.
//
// The port comes from the url (identity: HUDDLE_URL, huddle.json's url, HUDDLE_PORT, or the port
// saved in the home's config.toml); with none yet, `huddle up` picks one with the server
// package's ChoosePort (18860 first, then 18861 to 18869, then the OS's pick) and saves it, and
// a saved port it picked that another program has since taken moves to a new one (a port someone
// chose is never rewritten). Other server settings pass through from the environment. Whoever
// starts it is its creator: `huddle up` makes the root credential, hands it to the server on
// stdin (never through the environment) and keeps it as this session's credential (creds.go);
// the server also leaves the run's root in run/owner.key for this machine's owner (ownerkey.go
// on the server side); everyone else joins with an invite the creator mints.
package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/process"
	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/outside"
	"github.com/MuhmdRaouf/huddle/internal/huddle/server"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// Home answers where this user's Huddle files live.
func Home(ctx context.Context, w *World) string { return HomeOf(ctx, w) }

// DataDir answers where the server's channels live: HUDDLE_DATA, else the home itself (the
// channels are <home>/channels/<name>.db, the credential digests <home>/auth.json).
func DataDir(ctx context.Context, w *World) string {
	if dir := w.getenv("HUDDLE_DATA"); dir != "" {
		return dir
	}
	return Home(ctx, w)
}

// KeepOutOfGit lists <repo>/.agents/huddle in the repo's own .git/info/exclude (the folder holds
// the project's settings file, which names its channel and sessions). It answers the exclude
// file, or empty when there is nothing to do.
func KeepOutOfGit(ctx context.Context, w *World, dir string) string {
	// the home's suffix in either separator's spelling: HomeOf builds it the OS's way, a caller
	// may hand it over as written
	if !strings.HasSuffix(dir, "/.agents/huddle") && !strings.HasSuffix(dir, `\.agents\huddle`) {
		return ""
	}
	main := MainCheckout(ctx, w, filepath.Dir(filepath.Dir(dir)))
	if main == "" {
		return ""
	}
	exclude := filepath.Join(main, ".git", "info", "exclude")
	line := ".agents/huddle/"
	if raw, err := os.ReadFile(exclude); err == nil && strings.Contains(string(raw), line) {
		return ""
	}
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
		return ""
	}
	file, err := os.OpenFile(exclude, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return ""
	}
	defer file.Close()
	if _, err := file.WriteString("\n" + line + "\n"); err != nil {
		return ""
	}
	return exclude
}

// Result is one up, down or server command's answer.
type Result struct {
	OK      bool
	Msg     string
	Created bool // this call started the server
	First   bool // this home's Huddle never ran before (no members yet)
	URL     string
	Exit    int // a failure's own exit code; 0 leaves the command's default
	// Router is the router start's line: huddle-router runs as its own process beside the hub,
	// and up brings it up first. Empty when up started nothing (a test binary).
	Router string
}

// pidFile is the home's pid file, in its run directory.
func pidFile(root string) string { return home.Home{Root: root}.PidFile() }

// portFile is the home's port file, in its run directory: the port the running server answers
// on. The pid alone never vouches for a server: a process named by a stale file may be anything,
// so the port answering huddle's health path is what confirms it is ours.
func portFile(root string) string { return home.Home{Root: root}.PortFile() }

// logFile is the home's log file, in its run directory.
func logFile(root string) string { return home.Home{Root: root}.LogFile() }

// noteRun appends one line to the run log: a start or a stop, with its reason.
func noteRun(root string, at time.Time, line string) {
	file := logFile(root)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return
	}
	log, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer log.Close()
	_, _ = fmt.Fprintf(log, "%s %s\n", jsfmt.ISOTime(at), line)
}

// savedRunPort reads the port the running server noted in run/huddle.port; 0 when there is none.
func savedRunPort(root string) int {
	raw, err := os.ReadFile(portFile(root))
	if err != nil {
		return 0
	}
	port := jsNumber(strings.TrimSpace(string(raw)))
	if port != float64(int(port)) || port <= 0 || port >= 65536 {
		return 0
	}
	return int(port)
}

// PidOf answers the pid in the home's run/huddle.pid, when that process is still a Huddle server
// (a stale pid may belong to anything); 0 when it is not.
func PidOf(ctx context.Context, w *World) int { return RunningPid(ctx, w, Home(ctx, w)) }

// hubHealth is the part of a hub's health answer the replace decision and the domain check read.
type hubHealth struct {
	OK         bool   `json:"ok"`
	Version    string `json:"version"`
	Home       string `json:"home"`
	Executable string `json:"executable"`
}

// hubAt fetches the health answer of the hub at url; ok is false when nothing answers within
// ms, or the answer is not a hub's.
func hubAt(ctx context.Context, url string, ms int) (hubHealth, bool) {
	if url == "" {
		return hubHealth{}, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/health", nil)
	if err != nil {
		return hubHealth{}, false
	}
	resp, err := (&http.Client{Timeout: time.Duration(ms) * time.Millisecond}).Do(req)
	if err != nil {
		return hubHealth{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return hubHealth{}, false
	}
	var answer hubHealth
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&answer); err != nil {
		return hubHealth{}, false
	}
	return answer, true
}

// hubNeedsReplace says whether the hub whose health answer this is gives way to this binary's
// start: only this home's own hub, and only when the pid file vouches for it, and only when
// another executable serves it — a hub that names no executable is an older build's (0.0.1),
// so it gives way too. A hub of the same executable, of another home, or an unvouched one is
// never replaced.
func hubNeedsReplace(health hubHealth, home, self string, vouched bool) bool {
	if !vouched || health.Home != home || health.Executable == self {
		return false
	}
	return true
}

// resolvedPath is bin with its symlinks resolved: the spelling a health answer's executable
// names.
func resolvedPath(bin string) string {
	resolved, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return bin
	}
	return resolved
}

// goneWithin answers whether pid has exited within ms, asking every 100 ms.
func goneWithin(pid int, ms time.Duration) bool {
	deadline := time.Now().Add(ms)
	for {
		if !processAlive(pid) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// hubDecision is one health probe of the hub at a url, with the replace decision made.
type hubDecision struct {
	health   hubHealth
	answered bool
	pid      int  // the pid the home's pid file vouches for; 0 when it vouches for none
	replace  bool // an older build's hub of this home, vouched: this start gives it way
}

// probeHub asks the hub at url for its health once and decides whether this start replaces it:
// only this home's own hub, vouched by the pid file and served from another executable, is an
// older build's (a hub that names no executable is one too).
func probeHub(ctx context.Context, w *World, url string, rest func() time.Duration) hubDecision {
	health, ok := hubAt(ctx, url, int(clampMS(rest(), 1500)))
	if !ok {
		return hubDecision{}
	}
	h := Home(ctx, w)
	pid := PidOf(ctx, w)
	replace := hubNeedsReplace(health, h, resolvedPath(w.bin()), pid > 0) &&
		pid > 1 && pid != os.Getpid()
	return hubDecision{health: health, answered: true, pid: pid, replace: replace}
}

// stopOutdatedHub signs an outdated hub out of the way: SIGTERM, a 10 s wait for its exit,
// SIGKILL past that, each step in the run log.
func stopOutdatedHub(w *World, root string, decided hubDecision) {
	old := decided.health.Executable
	if old == "" {
		old = "an unnamed build"
	}
	noteRun(root, w.now(), fmt.Sprintf("replaced hub pid %d from %s: SIGTERM", decided.pid, old))
	_ = signalTerm(decided.pid)
	if !goneWithin(decided.pid, 10*time.Second) {
		noteRun(root, w.now(), fmt.Sprintf("replaced hub pid %d from %s: SIGKILL", decided.pid, old))
		_ = signalKill(decided.pid)
		goneWithin(decided.pid, 2*time.Second)
	}
}

// Healthy answers whether a Huddle server answers at url.
func Healthy(ctx context.Context, url string, ms int) bool {
	if url == "" {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/health", nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: time.Duration(ms) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// settled answers whether a port is free now or within a second (a port a server just let go
// of can linger briefly).
func settled(port int) bool {
	for i := 0; i < 5; i++ {
		if server.PortFree(port) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// loopbackHosts are the hosts huddle up starts a server on.
func loopbackHosts(host string) bool { return host == "127.0.0.1" || host == "localhost" }

// placement is where up starts the server.
type placement struct {
	url   string
	note  string // a line said before the start's own words
	reuse bool   // a huddle already answers there: start nothing
	err   string
	exit  int // the exit code err asks for; 0 leaves up's default
}

// oursAnswers says whether the huddle answering on port runs from this home: the health path
// names the home it serves, so a server of another home (a second HUDDLE_HOME on this machine)
// is never mistaken for ours.
func oursAnswers(ctx context.Context, port int, root string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, Local(port)+"/health", nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 750 * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}
	var answer struct {
		Home string `json:"home"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return false
	}
	return answer.Home != "" && answer.Home == root
}

// place decides where up starts the server: url's port when it is free or names a Huddle; no
// url yet means ChoosePort picks one (18860 first, then 18861 to 18869, then the OS's pick) and
// saves it; a saved port huddle picked that another program now holds means a new one, saved,
// with a line that says so; a port someone chose (HUDDLE_URL, huddle.json's url, HUDDLE_PORT,
// --port) is never moved, only saved when it came from HUDDLE_PORT; a hub behind its proxy holds
// its pinned port, and another program on it ends up with exit 2.
func place(ctx context.Context, w *World, rawURL, sid string) placement {
	h := Home(ctx, w)
	probe := func(ctx context.Context, port int) bool { return oursAnswers(ctx, port, h) }
	if rawURL == "" {
		if pid := PidOf(ctx, w); pid > 0 {
			return placement{err: fmt.Sprintf("a Huddle server (pid %d) runs from %s but its port is unknown: save it with huddle setup --port <its port>", pid, h)}
		}
		prefs := server.PortPrefs{}
		if saved, has := SavedPort(h); has {
			prefs = server.PortPrefs{Port: saved.Port, Auto: saved.Auto}
		}
		chosen, err := server.ChoosePort(ctx, prefs, probe)
		if err != nil {
			return placement{err: err.Error()}
		}
		if chosen.Huddle {
			SavePort(ctx, w, chosen.Port, false, h)
			return placement{url: Local(chosen.Port), reuse: true}
		}
		file := SavePort(ctx, w, chosen.Port, chosen.Auto, h)
		return placement{url: Local(chosen.Port), note: fmt.Sprintf("Huddle picked port %d (saved in %s)", chosen.Port, file)}
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "http" || !loopbackHosts(strings.ToLower(parsed.Hostname())) {
		return placement{err: fmt.Sprintf("%s is not a local http address: huddle up only starts a server on this machine", rawURL)}
	}
	port := 80
	if parsed.Port() != "" {
		if n, err := strconv.Atoi(parsed.Port()); err == nil {
			port = n
		}
	}
	me := Identify(ctx, w, sid)
	saved, _ := SavedPort(h)
	if settled(port) {
		// a port from HUDDLE_PORT becomes the home's saved one, as a choice
		if me.URL == rawURL && me.Source == "port" && (saved == nil || saved.Port != port || saved.Auto) {
			SavePort(ctx, w, port, false, h)
		}
		return placement{url: rawURL}
	}
	if pid := PidOf(ctx, w); pid > 0 { // this home's own server, busy or still starting: never move away from it
		return placement{err: fmt.Sprintf("Huddle (pid %d) runs from %s but does not answer at %s yet; try again, or see %s", pid, h, rawURL, logFile(h))}
	}
	if me.URL == rawURL && me.Source == "saved" && saved != nil && saved.Auto && saved.Port == port {
		chosen, err := server.ChoosePort(ctx, server.PortPrefs{Port: port, Auto: true}, probe)
		if err != nil {
			return placement{err: err.Error()}
		}
		if chosen.Huddle {
			return placement{url: Local(chosen.Port), reuse: true}
		}
		file := SavePort(ctx, w, chosen.Port, chosen.Auto, h)
		return placement{url: Local(chosen.Port), note: fmt.Sprintf("port %d is taken by another program: Huddle moved to port %d (saved in %s)", port, chosen.Port, file)}
	}
	if cfg, err := home.LoadConfig(home.Home{Root: h}); err == nil && cfg.Role == "hub" && cfg.Hub.Listen == "" &&
		saved != nil && !saved.Auto && saved.Port == port {
		return placement{exit: 2, err: fmt.Sprintf("port %d is pinned for the hub behind its proxy: free it, or choose another port with huddle setup --port <n> and update the proxy", port)}
	}
	return placement{err: fmt.Sprintf("port %d is taken by another program, not Huddle: free it, or choose another port with huddle setup --port <n>", port)}
}

// newCredential mints one root credential.
func newCredential() string {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "hcred_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "hcred_" + base64.RawURLEncoding.EncodeToString(raw[:])
}

// replaceHung clears the home's serve lock for a fresh start. A holder that answers health — the
// home's recorded port, else the url this start would take — is the server the start wanted:
// reuse names it and nothing is signalled. A holder silent twice, 5 s apart, is replaced:
// SIGTERM, a 5 s wait for the lock (the holder asked every 200 ms), then SIGKILL and a 2 s wait,
// each step in the run log. Only the holder the lock names is ever signalled, never pid ≤ 1 or
// this process. When the start's budget runs out first, proceed is false and nothing was
// spawned.
func replaceHung(ctx context.Context, w *World, root, rawURL string, rest func() time.Duration) (proceed bool, reuse string) {
	holder, held := server.LockHolder(root)
	if !held || holder <= 1 || holder == os.Getpid() {
		// no holder, or one this process never signals: the start below meets the lock itself
		return true, ""
	}
	at := rawURL
	if port := savedRunPort(root); port > 0 {
		at = Local(port)
	}
	if Healthy(ctx, at, 3000) {
		return true, at
	}
	if rest() <= 0 {
		return false, ""
	}
	time.Sleep(5 * time.Second) // the second probe: a server still starting answers by then
	if rest() <= 0 {
		return false, ""
	}
	if Healthy(ctx, at, 3000) {
		return true, at
	}
	noteRun(root, w.now(), fmt.Sprintf("replaced hung server pid %d: SIGTERM", holder))
	_ = signalTerm(holder)
	if !lockFreeWithin(root, 5*time.Second) {
		noteRun(root, w.now(), fmt.Sprintf("replaced hung server pid %d: SIGKILL", holder))
		_ = signalKill(holder)
		lockFreeWithin(root, 2*time.Second)
	}
	return rest() > 0, ""
}

// lockFreeWithin answers whether the home's serve lock is free again within ms, asking the lock
// every 200 ms.
func lockFreeWithin(root string, ms time.Duration) bool {
	deadline := time.Now().Add(ms)
	for {
		if _, held := server.LockHolder(root); !held {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// Up starts the Huddle server the way a session does: detached, and watching for the plugin's
// removal (only a server the plugin started stops itself with it). See up for the parameters.
func Up(ctx context.Context, w *World, rawURL string, budgetMS int, sid string) Result {
	return up(ctx, w, rawURL, budgetMS, sid, true)
}

// UpAlone starts the Huddle server the way the binary run without Claude Code does: the same
// spawn, the same port choice and the same creator credential, but no removal watch — a server
// the owner started by hand is not the plugin's, and must keep serving when the plugin leaves
// Claude Code (or was never there).
func UpAlone(ctx context.Context, w *World, rawURL string, budgetMS int) Result {
	return up(ctx, w, rawURL, budgetMS, "", false)
}

// up starts huddle-router and then the Huddle server, two processes (ADR-4): a hub restart
// never touches the router, so the Claude streams in flight keep going. The router start is
// routerd.Start's own, its line in the answer's Router; a router that cannot start never keeps
// the hub down. See upHub for the hub and the parameters.
func up(ctx context.Context, w *World, rawURL string, budgetMS int, sid string, removalWatch bool) Result {
	if budgetMS <= 0 {
		budgetMS = 35_000
	}
	// A Go test binary is never the hub nor the router: started as `serve` it re-runs its own
	// suite, detached and with the environment of the code that reached here — on a developer
	// machine, the owner's real home.
	bin := w.bin()
	if process.IsTestBinary(bin) {
		return Result{URL: rawURL, Msg: fmt.Sprintf("%s is a test binary, not huddle; the hub is not started", filepath.Base(bin))}
	}
	began := w.now()
	line := startRouter(ctx, w, min(routerStartBudget, time.Duration(budgetMS)*time.Millisecond))
	left := budgetMS - int(w.now().Sub(began)/time.Millisecond)
	result := upHub(ctx, w, bin, rawURL, max(left, 1), sid, removalWatch)
	result.Router = line
	return result
}

// upHub starts the Huddle server, detached, unless one already answers at url: a hub an older
// build serves is replaced and this binary's comes up on the same port, a hub this binary
// serves is left answering. budgetMS bounds the whole call (the session-start hook passes what
// is left of its own budget); a server still starting when it runs out keeps starting,
// detached, for the next session. The answer carries the url the server answers at (a new port
// when it had to move).
func upHub(ctx context.Context, w *World, bin, rawURL string, budgetMS int, sid string, removalWatch bool) Result {
	deadline := w.now().Add(time.Duration(budgetMS) * time.Millisecond)
	rest := func() time.Duration { return deadline.Sub(w.now()) }
	rawURL = trimSlash(rawURL)
	// a hub of an older binary gives way (D2): the health answer names the executable that
	// serves it, and when the pid file vouches for it as this home's hub, this start replaces
	// it and comes up on the same port
	decided := probeHub(ctx, w, rawURL, rest)
	if decided.answered && !decided.replace {
		return Result{OK: true, URL: rawURL, Msg: fmt.Sprintf("Huddle is up at %s", rawURL)}
	}
	var replaced int
	if decided.replace {
		replaced = decided.pid
		stopOutdatedHub(w, Home(ctx, w), decided)
	}
	at := place(ctx, w, rawURL, sid)
	if at.err != "" {
		return Result{URL: rawURL, Msg: at.err, Exit: at.exit}
	}
	rawURL = at.url
	said := ""
	if at.note != "" {
		said = at.note + "\n"
	}
	if at.reuse { // a huddle already answers there: nothing to start — unless it is an older
		// build's hub, which the start below replaces
		decided = probeHub(ctx, w, rawURL, rest)
		if decided.answered && !decided.replace {
			return Result{OK: true, URL: rawURL, Msg: fmt.Sprintf("%sHuddle is up at %s", said, rawURL)}
		}
		if !decided.answered {
			return Result{URL: rawURL, Msg: fmt.Sprintf("%sHuddle did not answer at %s; see %s", said, rawURL, logFile(Home(ctx, w)))}
		}
		replaced = decided.pid
		stopOutdatedHub(w, Home(ctx, w), decided)
	}
	parsed, _ := url.Parse(rawURL)
	port := "80"
	if parsed.Port() != "" {
		port = parsed.Port()
	}
	h := Home(ctx, w)
	data := DataDir(ctx, w)
	if hh, ok := TheHome(ctx, w); ok {
		if err := hh.Ensure(ctx); err != nil {
			return Result{URL: rawURL, Msg: said + err.Error()}
		}
		refreshInstalledService(ctx, w, hh.Root)
	}
	// one server per home: the home's serve lock names the process that holds it. A holder that
	// answers health is the server this start wanted — reuse it. A holder silent twice, 5 s
	// apart, is hung: it is signalled out of the way (SIGTERM, then SIGKILL), so a hung server
	// never leaves a duplicate or a locked-out start.
	proceed, reuse := replaceHung(ctx, w, h, rawURL, rest)
	if reuse != "" {
		return Result{OK: true, URL: reuse, Msg: fmt.Sprintf("%sHuddle is up at %s", said, reuse)}
	}
	if !proceed { // the budget ran out clearing a hung server: nothing was spawned
		still := fmt.Sprintf(" within %s s (still starting)", jsfmt.JSNumber(math.Round(float64(budgetMS)/100)/10))
		return Result{URL: rawURL, Msg: fmt.Sprintf("%sHuddle did not come up at %s%s; see %s", said, rawURL, still, logFile(h))}
	}
	log, err := os.OpenFile(logFile(h), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return Result{URL: rawURL, Msg: said + err.Error()}
	}
	defer log.Close()
	// the root credential: HUDDLE_TOKEN when set (scripts, tests), else the one this session
	// (or its project) already holds for this address, so the creator keeps working across
	// restarts, else a new one
	held := LoadCred(ctx, w, rawURL, sid)
	_, statErr := os.Stat(filepath.Join(data, "auth.json"))
	first := statErr != nil // first when no auth file ever existed
	root := w.getenv("HUDDLE_TOKEN")
	if root == "" && held != nil && held.Root {
		root = held.Credential
	}
	if root == "" {
		root = newCredential()
	}
	// HUDDLE_HOME so the server cleans up the same home on self-removal. HUDDLE_REMOVAL_WATCH is
	// set only by the plugin's own starts: a hand start's server is not the plugin's, and a watch
	// inherited from the caller's environment would make it stop itself all the same.
	env := w.environ()
	if w.getenv("HUDDLE_TOKEN") == "" {
		env = withoutVar(env, "HUDDLE_TOKEN")
	}
	if !removalWatch {
		env = withoutVar(env, "HUDDLE_REMOVAL_WATCH")
	}
	env = append(env,
		"PORT="+port, "HOST=127.0.0.1", "HUDDLE_DATA="+data, "HUDDLE_HOME="+h, "HUDDLE_ROOT_STDIN=1")
	if removalWatch {
		env = append(env, "HUDDLE_REMOVAL_WATCH=1")
	}
	// the no-chain rule: the hub's own requests never ride a router of ours; the user's other
	// proxies ride along
	env = withoutRouterProxies(env)
	child := exec.Command(bin, "serve")
	child.Env = env
	child.SysProcAttr = detachAttr()
	stdin, err := child.StdinPipe()
	if err != nil {
		return Result{URL: rawURL, Msg: said + err.Error()}
	}
	child.Stdout = log
	child.Stderr = log
	if err := child.Start(); err != nil {
		return Result{URL: rawURL, Msg: said + err.Error()}
	}
	exited := make(chan struct{})
	go func() { _ = child.Wait(); close(exited) }()             // reaps it, so its pid reads as gone once dead
	if _, err := io.WriteString(stdin, root+"\n"); err != nil { // a server that exits at once closes it first
		_ = err
	}
	_ = stdin.Close()
	_ = os.MkdirAll(filepath.Dir(pidFile(h)), 0o700)
	_ = os.WriteFile(pidFile(h), []byte(strconv.Itoa(child.Process.Pid)), 0o600)
	_ = os.WriteFile(portFile(h), []byte(port), 0o600)
	noteRun(h, w.now(), fmt.Sprintf("start pid %d port %s (huddle up)", child.Process.Pid, port))
	// this session is the creator: it keeps the root credential as its own
	me := Identify(ctx, w, sid)
	if w.getenv("HUDDLE_TOKEN") == "" {
		_ = SaveCred(ctx, w, Cred{URL: trimSlash(rawURL), Channel: me.Channel, As: me.As, Credential: root, Root: true}, sid)
	}
	for isRunning(child, exited) && rest() > 0 {
		if Healthy(ctx, rawURL, int(clampMS(rest(), 500))) {
			noteServer(ctx, w, h, ServerEntry{
				URL: rawURL, Channel: me.Channel, Project: projectOf(ctx, w), PID: child.Process.Pid, At: jsfmt.ISOTime(w.now()),
			})
			if replaced > 0 { // an older hub gave way: the answer names the switch
				return Result{OK: true, Created: true, First: first, URL: rawURL,
					Msg: fmt.Sprintf("Huddle updated at %s (pid %d → %d)", rawURL, replaced, child.Process.Pid)}
			}
			return Result{OK: true, Created: true, First: first, URL: rawURL,
				Msg: fmt.Sprintf("%sHuddle started at %s, UI %s (pid %d; data: %s/channels; log: %s); this session started it and holds its root credential",
					said, rawURL, rawURL, child.Process.Pid, data, logFile(h))}
		}
		time.Sleep(time.Duration(clampMS(rest(), 200)) * time.Millisecond)
	}
	// it never ran: a credential this call minted is dropped again
	dead := !isRunning(child, exited)
	if dead && (held == nil || !held.Root) {
		if now := LoadCred(ctx, w, trimSlash(rawURL), sid); now != nil && now.Credential == root {
			ForgetCred(ctx, w, sid)
		}
	}
	still := ""
	if !dead {
		still = fmt.Sprintf(" within %s s (still starting)", jsfmt.JSNumber(math.Round(float64(budgetMS)/100)/10))
	}
	return Result{URL: rawURL, Msg: fmt.Sprintf("%sHuddle did not come up at %s%s; see %s", said, rawURL, still, logFile(h))}
}

// isRunning answers whether the child has not exited yet.
func isRunning(child *exec.Cmd, exited <-chan struct{}) bool {
	select {
	case <-exited:
		return false
	default:
		return true
	}
}

// refreshInstalledService keeps an installed service pointing at the binary that runs (up and the
// session-start hook's autostart both come through here): an update replaces the versioned
// binary the unit file names, and the next entry re-renders it. Silent and bounded — an entry's
// own work never waits on it.
func refreshInstalledService(ctx context.Context, w *World, root string) {
	outside.RefreshServiceIfStale(ctx, outside.ServiceOptions{
		Home: root, Bin: w.bin(), UID: os.Getuid(), Runner: outside.CommandRunner{},
	})
}

// clampMS pins ms between 1 and max.
func clampMS(ms time.Duration, max int) int {
	whole := int(ms / time.Millisecond)
	if whole < 1 {
		return 1
	}
	if whole > max {
		return max
	}
	return whole
}

// MemoryLimitOf answers the hub's soft memory limit in bytes: HUDDLE_MEMORY_LIMIT_MB as a
// positive number of MiB, else 1 GiB. The serve entry hands it to debug.SetMemoryLimit
// once before the server starts: the GC then returns memory long before the heap doubles.
// The limit sits well above the hub's live heap: a limit below it keeps the GC running
// without pause, and the runtime then spends half of every core on collection.
func MemoryLimitOf(env func(string) string) int64 {
	const fallback = 1024 << 20
	if n, err := strconv.Atoi(env("HUDDLE_MEMORY_LIMIT_MB")); err == nil && n > 0 {
		return int64(n) << 20
	}
	return fallback
}

// GCPercentOf answers the GC percentage the hub starts at and whether to apply it:
// (50, true) when GOGC is unset or empty — at GOGC=100 the heap grows to twice the live
// set before a collection, and the hub's live set is mostly long-lived records — and
// (0, false) otherwise, the runtime already applying the user's value.
func GCPercentOf(env func(string) string) (int, bool) {
	if env("GOGC") != "" {
		return 0, false
	}
	return 50, true
}

// withoutRouterProxies drops the proxy variables whose value names a router of ours — one on
// the known ports, or the router on its own port (`HUDDLE_ROUTER_PORT`, else 18786). Pure: no
// probe; the daemon asks its loopback proxies for a health answer itself (routerd.CleanProxyEnv).
func withoutRouterProxies(env []string) []string {
	lookup := domain.EnvLookup{}
	for _, entry := range env {
		if name, value, ok := strings.Cut(entry, "="); ok {
			lookup[name] = value
		}
	}
	ownPort := int64(domain.HuddleRouterPort)
	if port, err := router.PortOf(lookup); err == nil {
		ownPort = int64(port)
	}
	out := make([]string, 0, len(env))
	for _, entry := range env {
		if name, value, ok := strings.Cut(entry, "="); ok &&
			slices.Contains(domain.ProxyEnvVars, name) && domain.IsRouterProxy(value, ownPort) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// withoutVar drops one variable from a child's environment.
func withoutVar(env []string, key string) []string {
	out := make([]string, 0, len(env))
	prefix := key + "="
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			out = append(out, entry)
		}
	}
	return out
}

// projectOf answers the project the server's channels belong to: the repo around the project
// dir, else the project dir. The answer is resolved first, as ProjectKey resolves it: one
// checkout reached through two spellings of one path (a symlinked /var) must name one project,
// or a project does not recognize the server its own session started.
func projectOf(ctx context.Context, w *World) string {
	start := w.getenv("CLAUDE_PROJECT_DIR")
	if start == "" {
		start = w.dir()
	}
	if start == "" {
		return ""
	}
	dir := MainCheckout(ctx, w, start)
	if dir == "" {
		dir = start
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return dir
}

// Down stops the server `huddle up` started. A server someone else started is named, not
// signalled. The pid file alone never vouches for the server: when it names a port, that port
// must answer huddle's health path before the process is treated as ours.
func Down(ctx context.Context, w *World, rawURL string) Result {
	h := Home(ctx, w)
	if rawURL == "" && PidOf(ctx, w) <= 0 {
		return Result{OK: true, Msg: "Huddle is not running"}
	}
	pid := PidOf(ctx, w)
	if pid > 0 && !oursOnHealth(ctx, h) {
		pid = 0
	}
	if pid <= 0 {
		_ = os.Remove(pidFile(h))
		if Healthy(ctx, rawURL, 1500) {
			return Result{Msg: fmt.Sprintf("Huddle at %s was not started by huddle up: stop it where it runs", rawURL)}
		}
		return Result{OK: true, Msg: "Huddle is not running"}
	}
	_ = signalTerm(pid)
	for i := 0; i < 50; i++ {
		if !processAlive(pid) {
			_ = os.Remove(pidFile(h))
			_ = os.Remove(home.Home{Root: h}.OwnerKey())
			noteRun(h, w.now(), fmt.Sprintf("stop pid %d (huddle down)", pid))
			return Result{OK: true, Msg: fmt.Sprintf("Huddle stopped (pid %d)", pid)}
		}
		time.Sleep(100 * time.Millisecond)
	}
	// after the wait the process itself is ended; the command check above already proved the pid
	// is our server
	if forceStop(pid) {
		_ = os.Remove(pidFile(h))
		_ = os.Remove(home.Home{Root: h}.OwnerKey()) // a forced stop had no clean exit to take it
		noteRun(h, w.now(), fmt.Sprintf("stop pid %d (huddle down, forced)", pid))
		return Result{OK: true, Msg: fmt.Sprintf("Huddle stopped (pid %d)", pid)}
	}
	return Result{Msg: fmt.Sprintf("pid %d did not stop; see %s", pid, logFile(h))}
}

// oursOnHealth answers whether the port the run directory names answers huddle's health path. A
// home without a port file (older runs, a server still writing it) answers true: the pid's own
// command check is all there is to go on then.
func oursOnHealth(ctx context.Context, root string) bool {
	port := savedRunPort(root)
	if port <= 0 {
		return true
	}
	return Healthy(ctx, Local(port), 1500)
}

// Info answers whether the server is up, which process runs it, and where its data and log are.
func Info(ctx context.Context, w *World, rawURL, sid string) Result {
	h := Home(ctx, w)
	if rawURL == "" {
		return Result{Msg: fmt.Sprintf("Huddle has no port here yet: huddle up picks 18860 (then 18861 to 18869) and saves it in %s\ndata: %s/channels\nlog:  %s",
			home.Home{Root: h}.Config(), DataDir(ctx, w), logFile(h))}
	}
	pid := PidOf(ctx, w)
	ok := Healthy(ctx, rawURL, 1500)
	who := ""
	switch {
	case pid > 0:
		who = fmt.Sprintf(" (pid %d, started by huddle up)", pid)
	case ok:
		who = " (not started by huddle up)"
	}
	link := ""
	hint := ""
	if ok {
		if got := Dashboard(ctx, w, rawURL, sid, 1500); got != "" {
			link = got
			hint = "  (signs you in once, for 5 minutes)"
		}
	}
	ui := rawURL
	if link != "" {
		ui = link
	}
	return Result{OK: ok,
		Msg: fmt.Sprintf("Huddle is %s at %s%s\nUI:   %s%s\ndata: %s/channels\nlog:  %s",
			downUp(ok), rawURL, who, ui, hint, DataDir(ctx, w), logFile(h))}
}

// downUp spells a server's state.
func downUp(up bool) string {
	if up {
		return "up"
	}
	return "down"
}

// Dashboard answers the dashboard link that signs a browser in: a one-time login code, which
// any session holding a credential gets for itself (the creator's browser has its rights; a
// member's has no admin rights). Empty for a session that has not joined. The code only signs a
// browser in for five minutes, so the CLI prints the link wherever it runs; it is not kept from
// Claude.
func Dashboard(ctx context.Context, w *World, rawURL, sid string, ms int) string {
	resp, err := HFetch(ctx, w, http.MethodPost, rawURL+"/api/login",
		map[string]string{"content-type": "application/json"}, []byte("{}"), time.Duration(ms)*time.Millisecond, sid)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	var answer struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil || answer.Code == "" {
		return ""
	}
	return fmt.Sprintf("%s/?code=%s", dashboardBase(ctx, w, rawURL), queryEscape(answer.Code))
}

// dashboardBase answers the base a printed dashboard link carries: the hub's own https address
// on a hub machine — the origin the owner bookmarks, and the one a passkey can be saved for (its
// relying party is the hub's name) — and the asking url everywhere else. Only a server on this
// machine is rewritten: a link minted on another machine's huddle keeps that machine's address.
func dashboardBase(ctx context.Context, w *World, rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	host := parsed.Hostname()
	if host != "localhost" && host != "::1" && !strings.HasPrefix(host, "127.") {
		return rawURL
	}
	root := Home(ctx, w)
	if root == "" {
		return rawURL
	}
	cfg, err := home.LoadConfig(home.Home{Root: root})
	if err != nil || cfg.Role != "hub" || cfg.Hub.Host == "" {
		return rawURL
	}
	return hubBaseURL(cfg.Hub.Host, cfg.Hub.Listen)
}

// queryEscape escapes one value the way JavaScript's encodeURIComponent does: alphanumerics
// and -_.!~*'() stay, everything else takes %XX per byte.
func queryEscape(value string) string {
	var out strings.Builder
	for _, r := range value {
		keep := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			strings.ContainsRune("-_.!~*'()", r)
		if keep {
			out.WriteRune(r)
			continue
		}
		for _, b := range []byte(string(r)) {
			fmt.Fprintf(&out, "%%%02X", b)
		}
	}
	return out.String()
}

// InviteOptions shape one invite: a time to live in seconds (nil leaves it to the server),
// single use, whether the holder may invite, the channel it opens and what it is for.
type InviteOptions struct {
	TTL         *int   `json:"ttl,omitempty"`
	SingleUse   bool   `json:"single_use"`
	CanInvite   bool   `json:"can_invite"`
	Channel     string `json:"channel,omitempty"`
	Description string `json:"description,omitempty"`
}

// Invite is a minted invite, or the refusal.
type Invite struct {
	OK      bool
	Token   string
	ID      string
	Expires string // empty when it never does
	Join    string // the line that joins with it
	Error   string
}

// MintInvite mints a fresh invite at url: the token, its id, when it expires and the join line.
func MintInvite(ctx context.Context, w *World, rawURL string, o InviteOptions, sid string) Invite {
	body, err := domain.MarshalCompactJSON(o)
	if err != nil {
		return Invite{Error: err.Error()}
	}
	resp, err := HFetch(ctx, w, http.MethodPost, rawURL+"/api/tokens",
		map[string]string{"content-type": "application/json"}, body, 5*time.Second, sid)
	if err != nil {
		return Invite{Error: err.Error()}
	}
	defer resp.Body.Close()
	var answer struct {
		Token   string  `json:"token"`
		ID      string  `json:"id"`
		Expires *string `json:"expires"`
		Error   string  `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		answer.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || answer.Token == "" {
		if answer.Error == "" {
			answer.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return Invite{Error: answer.Error}
	}
	host := rawURL
	if parsed, err := url.Parse(rawURL); err == nil {
		host = parsed.Host
	}
	expires := ""
	if answer.Expires != nil {
		expires = *answer.Expires
	}
	return Invite{OK: true, Token: answer.Token, ID: answer.ID, Expires: expires,
		Join: fmt.Sprintf("huddle join %s --token %s", host, answer.Token)}
}

// JoinResult is a fresh invite's join line, or the refusal.
type JoinResult struct {
	OK      bool
	Line    string // /huddle:join <host:port> --token <id.secret>
	Expires string
	Error   string
}

// JoinLine mints a fresh invite and answers the line another Claude session pastes in to join
// with it (/huddle:join <host:port> --token <id.secret>), printed for the user wherever the CLI
// runs.
func JoinLine(ctx context.Context, w *World, rawURL string, o InviteOptions, sid string) JoinResult {
	inv := MintInvite(ctx, w, rawURL, o, sid)
	if !inv.OK {
		return JoinResult{Error: inv.Error}
	}
	return JoinResult{OK: true, Expires: inv.Expires, Line: "/huddle:join " + strings.TrimPrefix(inv.Join, "huddle join ")}
}

// ServerEntry is one running server in the machine's registry; At is when it was noted.
type ServerEntry struct {
	URL     string `json:"url"`
	Channel string `json:"channel,omitempty"`
	Project string `json:"project,omitempty"`
	PID     int    `json:"pid,omitempty"`
	At      string `json:"at,omitempty"`
}

// RegistryFile answers where the servers this user runs are noted:
// ${XDG_STATE_HOME:-~/.local/state}/huddle/servers.json. The registry is machine state (it names
// servers of every home), so it stays under the state home, not in a home.
func RegistryFile(w *World) string {
	state := w.getenv("XDG_STATE_HOME")
	if state == "" {
		userHome := w.getenv("HOME")
		if userHome == "" {
			userHome = userHomeDir()
		}
		state = filepath.Join(userHome, ".local", "state")
	}
	return filepath.Join(state, "huddle", "servers.json")
}

// Servers answers the servers this user runs (`huddle up` notes each): keyed by home, no
// secrets. Setup looks here before it starts a second one.
func Servers(ctx context.Context, w *World) map[string]ServerEntry {
	out := map[string]ServerEntry{}
	raw, err := os.ReadFile(RegistryFile(w))
	if err != nil {
		return out
	}
	var noted map[string]ServerEntry
	if err := json.Unmarshal(raw, &noted); err != nil {
		return out
	}
	return noted
}

// noteServer records a start in the machine's registry, keeping every home already in it and
// its order; a failure never surfaces.
func noteServer(ctx context.Context, w *World, home string, entry ServerEntry) {
	raw, err := os.ReadFile(RegistryFile(w))
	if err != nil {
		raw = []byte("{}")
	}
	value, err := jsfmt.DecodeValue(raw)
	if err != nil || value.Kind() != jsfmt.KindObject {
		value = jsfmt.Object(nil, nil)
	}
	keys := append([]string{}, value.Keys()...)
	fields := append([]*jsfmt.Value{}, value.Fields()...)
	noted, err := domain.MarshalIndentJSON(entry)
	if err != nil {
		return
	}
	field, err := jsfmt.DecodeValue(noted)
	if err != nil {
		return
	}
	if at := indexOf(keys, home); at >= 0 {
		fields[at] = field
	} else {
		keys = append(keys, home)
		fields = append(fields, field)
	}
	file := RegistryFile(w)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return
	}
	tmp := fmt.Sprintf("%s.%d.tmp", file, os.Getpid())
	if err := os.WriteFile(tmp, []byte(indentJSON(jsfmt.Object(keys, fields))), 0o600); err == nil {
		_ = os.Rename(tmp, file)
	}

}

// ServerCommand runs the CLI's up, down or server branch: it starts, stops or describes the
// bundled server. rawURL is the identity's address (empty when this project has none yet), args
// the command line (up reads its --port wherever it stands in it). The answer's words go to
// stdout when it is good news and stderr when it is not; the returned int is the exit code:
// 0 ok, 2 down failed, 5 up or server unreachable, 2 again for a bad --port or a hub's pinned
// port another program holds.
func ServerCommand(ctx context.Context, w *World, command, rawURL string, args []string, stdout, stderr io.Writer) int {
	var port string
	for i := 0; i < len(args); i++ {
		if args[i] == "--port" && i+1 < len(args) {
			port = args[i+1]
			i++
		}
	}
	if command == "up" && port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			fmt.Fprintf(stderr, "huddle: --port %s: a port number\n", port)
			return 2
		}
		// a port of your choosing wins, and up saves it
		w = w.WithVar("HUDDLE_PORT", port).WithVar("HUDDLE_URL", "")
		rawURL = Local(n)
	}
	var result Result
	switch command {
	case "up":
		result = Up(ctx, w, rawURL, 35_000, "")
	case "down":
		result = Down(ctx, w, rawURL)
	case "server":
		result = Info(ctx, w, rawURL, "")
	default:
		fmt.Fprintf(stderr, "huddle: %s is not a server command\n", command)
		return 2
	}
	if result.URL != "" {
		rawURL = result.URL
	}
	if result.OK {
		fmt.Fprintln(stdout, result.Msg)
	} else {
		fmt.Fprintln(stderr, result.Msg)
	}
	if result.Router != "" {
		fmt.Fprintln(stdout, result.Router)
	}
	if result.Created { // the creator: the line another session pastes in, and the dashboard link
		if line := JoinLine(ctx, w, rawURL, InviteOptions{Channel: identityChannel(ctx, w), Description: "made by huddle up"}, ""); line.OK {
			valid := "forever"
			if line.Expires != "" {
				valid = "until " + line.Expires
			}
			fmt.Fprintf(stdout, "join:  %s   (valid %s)\n", line.Line, valid)
		}
		if link := Dashboard(ctx, w, rawURL, "", 1500); link != "" {
			fmt.Fprintf(stdout, "dashboard: %s\n", link)
		}
	}
	if result.OK {
		return 0
	}
	if result.Exit != 0 {
		return result.Exit
	}
	if command == "down" {
		return 2
	}
	return 5
}

// identityChannel is the creator's channel, for the invite it mints.
func identityChannel(ctx context.Context, w *World) string {
	return Identify(ctx, w, "").Channel
}
