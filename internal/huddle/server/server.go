// The Huddle server: one loopback HTTP process holding every channel (each its own SQLite file,
// this process its only writer), the owner's dashboard, the sessions' API and MCP endpoints, and
// the embedded UI. Only this machine's browser and sessions may use it: the Host must be ours,
// every write carries JSON and names its action, and a named Origin must be our own.
package server

import (
	"bufio"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/safego"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/extras"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/huddle/kanbantab"
	"github.com/MuhmdRaouf/huddle/internal/huddle/outside"
	"github.com/MuhmdRaouf/huddle/internal/huddle/phone"
	"github.com/MuhmdRaouf/huddle/internal/huddle/radartab"
	"github.com/MuhmdRaouf/huddle/internal/huddle/satellite"
	"github.com/MuhmdRaouf/huddle/internal/huddle/web"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

//go:embed connect.md
var connectDoc string

// Options shapes one server run.
type Options struct {
	Version string
	// Host is the interface to bind; "" binds loopback.
	Host string
	// Port is the port to bind; 0 takes any free one and prints it.
	Port int
	// PortNamed says a PORT in the environment named this server's port (the plugin's own
	// start, a test): it is bound exactly, never chosen or saved. With no port named and none
	// set, the server chooses one the way huddle up does and saves it (port.go), so a service
	// restart comes back on the same address.
	PortNamed bool
	// Home is the huddle home (run files, the left-behind note); Data holds the channels.
	Home string
	Data string
	// Token is the root credential; "" makes a fresh one for this run.
	Token string
	// Hosts adds allowed Host headers beyond loopback for the bound port.
	Hosts []string
	// AutoCreate says a session's join may create the channel it names.
	AutoCreate bool
	// RemovalWatch stops the server once the registry agrees the plugin is gone; RemovalMS is
	// the check interval in milliseconds (10 s when zero).
	RemovalWatch bool
	RemovalMS    int
	// PidCheckMS is the registry's pid check interval in milliseconds (30 s when zero): each
	// pass ends the sessions whose process is gone.
	PidCheckMS    int
	PermitSweepMS int
	// PushSweepMS is the push sweep's interval in milliseconds (30 s when zero); each pass
	// carries the items' first pushes and the due escalations, and a fresh item wakes it.
	PushSweepMS int
	// DeskQuietMS is how long one focus report keeps the other devices quiet, in milliseconds
	// (60 s when zero).
	DeskQuietMS int
	// SSEPingMS is the ping interval of the browser streams, the registry's and radar's, in
	// milliseconds (15 s when zero): the page's watchdog counts the pings as signs of life.
	SSEPingMS int
	// Env is the environment the presence checks read; Projects names the projects to check.
	Env      map[string]string
	Projects func() []string
	// Signals installs the SIGTERM/SIGINT shutdown when true (a real process start).
	Signals bool
	// Environ is the process environment the radar tab reads (RADAR_HOME, HOME,
	// CLAUDE_CONFIG_DIR, the providers' state dirs). A nil Environ serves no radar tab: a
	// caller that names no environment cannot be told where radar's files live.
	Environ []string
	// RootStdin says the root credential came from stdin's first line.
	RootStdin bool
	Stdin     io.Reader
	// ShowRoot prints the root credential and a dashboard link (a terminal start with none handed over).
	ShowRoot bool
	Stdout   io.Writer
	Stderr   io.Writer
	// Now is the clock; nil takes the wall clock.
	Now func() time.Time
	// CertACME answers the ACME the hub role's certificate renewal uses; nil resolves lego's
	// issuer from config.toml's cert.dns_provider with its token from the keystore. A config
	// naming no provider leaves the renewal off.
	CertACME func(ctx context.Context) (home.ACME, error)
	// Secrets resolves the home's secret store the satellite link's credential is read from;
	// nil takes the OS keystore with the home's keys/ files behind it. Tests answer from a
	// fake, so no real keystore is ever read.
	Secrets func(ctx context.Context, h home.Home) home.SecretStore
}

// session401 is what a session without a working credential is told; signedOut what a browser is.
const session401 = "not in this huddle: this session holds no credential for it. In a session that is in it, run /huddle:invite and paste the join line it shows into this session (/huddle:join …)"

const signedOut = "This browser is not signed in to Huddle (its sign-in ended, or the link expired). Run `huddle open` in a terminal of this project, or /huddle:setup in a Claude session there, and open the new link."

const logoSVG = `<svg viewBox="0 0 26 26" width="36" height="36" aria-hidden="true"><path d="M5 17 Q13 2 21 17" fill="none" stroke="currentColor" stroke-width="1.6" stroke-dasharray="2.2 2.2" opacity=".75"/><circle cx="13" cy="9.6" r="2.4" fill="currentColor"/><rect x="2.5" y="17" width="5" height="6" rx="2.5" fill="currentColor"/><rect x="18.5" y="17" width="5" height="6" rx="2.5" fill="currentColor"/></svg>`

// signedOutCSS styles the signed-out page. It is served as /signed-out.css because the CSP admits
// no inline style or script; the stored theme choice waits for the app itself.
const signedOutCSS = `:root{--bg:#e6e9ef;--panel:#eff1f5;--border:#ccd0da;--border2:#bcc0cc;--text:#4c4f69;--muted:#5c5f77;--faint:#5f627a;--blue:#1e66f5;--blueink:#2e5ec4;--peach:#fe640b;--peachink:#8a5648;color-scheme:light}
@media (prefers-color-scheme:dark){:root:not([data-theme=light]){--bg:#181825;--panel:#1e1e2e;--border:#313244;--border2:#45475a;--text:#cdd6f4;--muted:#a6adc8;--faint:#9399b2;--blue:#89b4fa;--blueink:#89b4fa;--peach:#fab387;--peachink:#fab387;color-scheme:dark}}
:root[data-theme=dark]{--bg:#181825;--panel:#1e1e2e;--border:#313244;--border2:#45475a;--text:#cdd6f4;--muted:#a6adc8;--faint:#9399b2;--blue:#89b4fa;--blueink:#89b4fa;--peach:#fab387;--peachink:#fab387;color-scheme:dark}
*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:16px;background:var(--bg);color:var(--text);font:14px/1.5 Inter,ui-sans-serif,system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;-webkit-font-smoothing:antialiased}
main{width:100%;max-width:28rem;display:flex;flex-direction:column;gap:32px}.head{text-align:center;display:flex;flex-direction:column;align-items:center;gap:12px}
.logo{display:inline-flex;width:64px;height:64px;align-items:center;justify-content:center;border-radius:16px;color:var(--blueink);background:color-mix(in srgb,var(--blue) 14%,var(--panel));box-shadow:inset 0 0 0 1px color-mix(in srgb,var(--blue) 30%,transparent)}
h1{margin:0;font-size:30px;font-weight:700;letter-spacing:-.02em}.sub{margin:0;color:var(--muted);font-size:15px}
.card{background:var(--panel);border:1px solid var(--border);border-radius:10px;padding:24px;display:flex;flex-direction:column;gap:16px}
.note{display:flex;gap:10px;align-items:flex-start;border:1px solid color-mix(in srgb,var(--peach) 30%,var(--panel));background:color-mix(in srgb,var(--peach) 9%,var(--panel));border-radius:8px;padding:12px;font-size:13px}
.note b{font-weight:600}.note span{color:var(--muted)}.note i{color:var(--peachink);font-style:normal;font-weight:700}
p{margin:0}.muted{color:var(--muted);font-size:13px}.lab{font-size:13px;font-weight:500;margin-bottom:6px}
.field{display:flex;align-items:center;gap:10px;height:40px;padding:0 12px;border:1px solid var(--border2);border-radius:8px;background:var(--bg);font:13px ui-monospace,"SF Mono",Menlo,monospace}
.field::before{content:"›_";color:var(--faint);font-weight:600}
footer{text-align:center;font-size:12px;color:var(--faint)}
`

// signedOutPage is the page a used or expired sign-in link lands on: the dashboard's sign-in
// card, in Catppuccin Latte or Mocha following the system.
func signedOutPage(version string) string {
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light dark"><title>Huddle: signed out</title>
<link rel="stylesheet" href="/signed-out.css"></head>
<body><main><div class="head"><span class="logo">` + logoSVG + `</span><h1>Huddle</h1><p class="sub">Sign in to see your channels and sessions</p></div>
<section class="card"><div class="note" role="status"><i aria-hidden="true">!</i><p><b>Signed out.</b> <span>This sign-in link was used or has expired: each link signs in one browser, once.</span></p></div>
<p class="muted">Get a new link from any session in this huddle, then open it here.</p>
<div><div class="lab">In a Claude session</div><div class="field">/huddle:setup</div></div>
<div><div class="lab">Or in a terminal</div><div class="field">huddle open</div></div></section>
<footer>Huddle v` + version + `</footer></main></body></html>`
}

// staticTypes maps one file extension to the content type the shell's assets answer with.
var staticTypes = map[string]string{
	".js":          "text/javascript",
	".css":         "text/css",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".webp":        "image/webp",
	".jpg":         "image/jpeg",
	".jpeg":        "image/jpeg",
	".html":        "text/html; charset=utf-8",
	".md":          "text/markdown; charset=utf-8",
	".webmanifest": "application/manifest+json",
}

// fontName admits one allowlisted font file: the regex admits no "/", so the path can never
// leave fonts/, and only .woff2 ships.
var fontName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.woff2$`)

// iconName admits the PWA's icon files by name: the regex admits no "/", so the path can never
// leave icons/, and only the .png the manifest names ships.
var iconName = regexp.MustCompile(`^icon-[a-z0-9-]+\.png$`)

var (
	channelPath = regexp.MustCompile(`^/api/c/([a-z0-9-]{1,40})(?:/(.*))?$`)
	mcpPath     = regexp.MustCompile(`^/mcp/([a-z0-9-]{1,40})$`)
	tokenPath   = regexp.MustCompile(`^/api/tokens/([a-z0-9]{6})$`)
	memberPath  = regexp.MustCompile(`^/api/members/([a-z][a-z0-9_-]{0,31})$`)
	// a channel's name the way the hub's own rule admits it (channel.ChannelRe)
	channelNamePath = regexp.MustCompile(`^/api/channels/([a-z0-9][a-z0-9-]{0,39})$`)
	opPath          = regexp.MustCompile(`^op/([a-z_]+)$`)
	kbPath          = regexp.MustCompile(`^kb/(\d+)$`)
	taskPath        = regexp.MustCompile(`^task/([\w.-]{1,32})$`)
	repoPath        = regexp.MustCompile(`^repo/([a-z]+)$`)
	xPath           = regexp.MustCompile(`^x/([a-z/]+)$`)
	livePath        = regexp.MustCompile(`^/api/c/([a-z0-9-]{1,40})/live$`)
	// one registry session by id: a Claude Code session id, any shape but never a slash
	registrySessionPath = regexp.MustCompile(`^/api/registry/sessions/([^/]+)$`)
	// one needs-you item by its number and one of its two actions
	needsItemPath = regexp.MustCompile(`^/api/needs/([1-9][0-9]*)/(snooze|dismiss)$`)
	// one session by id and one of the owner's actions on it (needs.go)
	sessionActPath = regexp.MustCompile(`^/api/sessions/([^/]+)/(answer|pause|resume|messages)$`)
)

// flightTracker counts the requests a shutdown must let finish. A live stream has answered (its
// events run on after), so those are closed by the shutdown instead of awaited.
type flightTracker struct {
	mu       sync.Mutex
	inFlight int
	waiting  []chan struct{}
}

func (f *flightTracker) track() func() {
	f.mu.Lock()
	f.inFlight++
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		f.inFlight--
		if f.inFlight > 0 {
			f.mu.Unlock()
			return
		}
		waiting := f.waiting
		f.waiting = nil
		f.mu.Unlock()
		for _, wake := range waiting {
			close(wake)
		}
	}
}

func (f *flightTracker) whenIdle(ctx context.Context, timeout time.Duration) {
	f.mu.Lock()
	if f.inFlight == 0 {
		f.mu.Unlock()
		return
	}
	idle := make(chan struct{})
	f.waiting = append(f.waiting, idle)
	f.mu.Unlock()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-idle:
	case <-timer.C:
	case <-ctx.Done():
	}
}

// liveStream is one open SSE client: how to end it.
type liveStream struct {
	cancel context.CancelFunc
}

// server is one Huddle run: its hub, its credentials, its notifier, its views and the radar
// tab it serves under /api/radar.
type server struct {
	o        Options
	hub      *channel.Hub
	auth     *auth.Auth
	notify   *outside.Notifier
	zones    *outside.Zones
	watcher  *extras.Watcher
	views    *Views
	mcp      *Mcp
	radar    *radartab.Tab
	kanban   *kanbantab.Tab
	httpSrv  *http.Server
	listener net.Listener
	port     int
	cookie   string
	started  string
	// machine is this machine's own name: the word /health and the landing page call the hub by.
	machine string
	// exe is this serving process's executable, symlinks resolved: the health answer names it,
	// and up from a newer binary reads it to give an older hub way.
	exe string
	// hubSrv serves the TLS listener a hub role adds on hub.listen (roles.go); nil in every
	// other role and whenever it could not start, in which case the loopback listener above
	// carries the server alone.
	hubSrv      *http.Server
	hubListener net.Listener
	// registry is the home's hub.db, where the sessions' reports land; nil when it cannot be
	// opened, in which case the channels serve on and every report is refused.
	registry *hub.DB
	// now is the clock the registry's states are computed against.
	now func() time.Time
	// pairs is the pairing core over the same hub.db; nil when the registry or the relying
	// party could not be built, in which case every pairing route refuses.
	pairs *auth.Pairing
	// totp is the authenticator core over the same hub.db; nil when the registry could not be
	// built, in which case every authenticator route refuses and code sign-in is just wrong.
	totp *auth.TOTP
	// permits wakes the permission items' long polls; stepUp gates the dangerous acts (7.4):
	// a remote allow, an answer, a send — every route that carries the owner's decision or
	// words into a session.
	permits *permitWaiters
	stepUp  StepUp
	// satQueue wakes the satellites' long polls when a decision or a message is queued for
	// their machine (SPEC 6.7).
	satQueue *satWaiters
	// satMu guards satLink and satHubURL: the kanban tab mounts before the satellite link
	// starts, and every relayed flow command reads the link per request — nil until it is up.
	satMu     sync.Mutex
	satLink   *satellite.Link
	satHubURL string
	// alerts fans the raised owner alerts out to the in-process subscribers (the push sender).
	alerts *alertFanout
	// vapid signs every push send; pushHTTP carries it; pushWake nudges the push sweep. All
	// three come up with the registry: a home without its hub.db pushes nothing.
	vapid    *vapidSource
	pushHTTP *http.Client
	pushWake chan struct{}
	// radarAlerts is radar's alerts as the push sweep reads them, nil with no radar tab;
	// radarMu guards it, because radar mounts after the sweep starts. upstreamPushed is the id
	// of the failing-path alert the sweep last pushed, the sweep's own.
	radarMu        sync.Mutex
	radarAlerts    radarAnswerer
	upstreamPushed string
	// focusMu guards focus: the hub tabs that last reported themselves visible and focused, by
	// device and tab, each at its report's time. It lives in memory: a restart forgets it, and
	// a focused tab reports again within 30 s.
	focusMu sync.Mutex
	focus   map[deskTab]time.Time

	hostsMu sync.Mutex
	hosts   map[string]bool

	// phoneMu guards phone: the guide the owner's "Add a phone" starts (phone.go), one at a
	// time; nil while none serves.
	phoneMu sync.Mutex
	phone   *phone.Helper

	flights *flightTracker

	streamsMu sync.Mutex
	streams   map[*liveStream]struct{}

	// registrySubs holds the open all-channels registry streams (the Now list's): each channel
	// carries encoded SSE frames waiting for its writer.
	registrySubsMu sync.Mutex
	registrySubs   map[chan string]struct{}

	// boardSeen remembers the boards the kanban tab has served, so a board stays listed after
	// its last live session ends; it throttles the store's writes.
	boardSeen boardsSeen
}

// OptionsFromEnv reads the environment a server start finds itself in. A value of "" for a
// HUDDLE_ variable means it is not set; PORT names the port to bind (0 = any free one).
func OptionsFromEnv(version string, env func(string) string, stdout, stderr io.Writer, stdin io.Reader) Options {
	if env == nil {
		env = os.Getenv
	}
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	o := Options{Version: version, Stdout: stdout, Stderr: stderr, Stdin: stdin, AutoCreate: true}
	if p := jsfmt.Trim(env("PORT")); p != "" {
		o.PortNamed = true
		if n, err := strconv.Atoi(p); err == nil {
			o.Port = n
		}
	}
	o.Host = jsfmt.Trim(env("HOST"))
	if o.Host == "" {
		o.Host = "127.0.0.1"
	}
	homeDir := jsfmt.Trim(env("HUDDLE_HOME"))
	if homeDir == "" {
		if h, err := home.Resolve(env); err == nil {
			homeDir = h.Root
		}
	}
	o.Home = homeDir
	if homeDir != "" {
		// the hub's own name from config.toml is an allowed Host (SPEC 7.5): the owner's network
		// reaches the hub by it, so the rebinding guard must know it
		if cfg, err := home.LoadConfig(home.Home{Root: homeDir}); err == nil && cfg.Hub.Host != "" {
			o.Hosts = append(o.Hosts, cfg.Hub.Host)
		}
	}
	o.Data = jsfmt.Trim(env("HUDDLE_DATA"))
	if o.Data == "" {
		o.Data = homeDir // the channels sit in the home itself: <home>/channels/<name>.db
	}
	root := env("HUDDLE_TOKEN")
	rootStdin := jsfmt.Trim(env("HUDDLE_ROOT_STDIN")) == "1"
	if root == "" && rootStdin && stdin != nil {
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		root = jsfmt.Trim(line)
	}
	if root == "" {
		root = uuid() + uuid()
	}
	o.Token = root
	o.RootStdin = rootStdin
	// a server started by hand on a terminal, with no root handed to it, shows how to get in once
	o.ShowRoot = env("HUDDLE_TOKEN") == "" && !rootStdin && isTerminal(stdout)
	for _, h := range strings.Split(env("HUDDLE_HOSTS"), ",") {
		if h = jsfmt.Trim(h); h != "" {
			o.Hosts = append(o.Hosts, h)
		}
	}
	o.AutoCreate = env("HUDDLE_AUTO_CREATE") != "0"
	o.RemovalWatch = jsfmt.Trim(env("HUDDLE_REMOVAL_WATCH")) == "1"
	if ms := jsfmt.Trim(env("HUDDLE_REMOVAL_MS")); ms != "" {
		if n, err := strconv.Atoi(ms); err == nil && n > 0 {
			o.RemovalMS = n
		}
	}
	if ms := jsfmt.Trim(env("HUDDLE_PID_CHECK_MS")); ms != "" {
		if n, err := strconv.Atoi(ms); err == nil && n > 0 {
			o.PidCheckMS = n
		}
	}
	if ms := jsfmt.Trim(env("HUDDLE_PERMIT_SWEEP_MS")); ms != "" {
		if n, err := strconv.Atoi(ms); err == nil && n > 0 {
			o.PermitSweepMS = n
		}
	}
	if ms := jsfmt.Trim(env("HUDDLE_PUSH_SWEEP_MS")); ms != "" {
		if n, err := strconv.Atoi(ms); err == nil && n > 0 {
			o.PushSweepMS = n
		}
	}
	if ms := jsfmt.Trim(env("HUDDLE_DESK_QUIET_MS")); ms != "" {
		if n, err := strconv.Atoi(ms); err == nil && n > 0 {
			o.DeskQuietMS = n
		}
	}
	if ms := jsfmt.Trim(env("HUDDLE_SSE_PING_MS")); ms != "" {
		if n, err := strconv.Atoi(ms); err == nil && n > 0 {
			o.SSEPingMS = n
		}
	}
	o.Env = map[string]string{"HOME": env("HOME"), "CLAUDE_CONFIG_DIR": env("CLAUDE_CONFIG_DIR")}
	wd, err := os.Getwd()
	if err != nil {
		wd = ""
	}
	project := jsfmt.Trim(env("CLAUDE_PROJECT_DIR"))
	o.Projects = func() []string {
		out := []string{}
		for _, p := range []string{project, wd} {
			if p == "" {
				continue
			}
			seen := false
			for _, kept := range out {
				if kept == p {
					seen = true
				}
			}
			if !seen {
				out = append(out, p)
			}
		}
		return out
	}
	return o
}

// isTerminal says whether w is a terminal (a person may be watching it).
func isTerminal(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// uuid answers one random UUID, the shape a fresh root credential and an MCP session id take.
func uuid() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Serve runs the server until its context ends, a signal arrives (when Signals is set) or the
// plugin's removal stops it, then leaves everything closed and answers nil.
func Serve(ctx context.Context, o Options) error {
	if o.Data == "" {
		return errors.New("huddle: no data directory")
	}
	if o.Home == "" {
		return errors.New("huddle: no home directory")
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	// one server per home: the lock is taken before anything is bound or opened, so a second
	// start for the same home never touches its files. A home that cannot hold the lock still
	// serves, as it keeps serving without its owner key — the run directory is unusable, not
	// another server's.
	release, err := LockHome(o.Home)
	if err != nil {
		var held *HeldError
		if !errors.As(err, &held) {
			fmt.Fprintf(o.Stderr, "serve lock: %v\n", err)
			release = func() {}
		} else {
			return err
		}
	}
	defer release()
	if o.Host == "" {
		o.Host = "127.0.0.1"
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	root := o.Token
	if root == "" {
		root = uuid() + uuid()
	}
	if o.Port == 0 && !o.PortNamed {
		// no port named anywhere (a service start): the same choice huddle up makes, saved the
		// same way, so the restarts keep this address
		chosen, err := chooseAndServePort(ctx, o)
		if err != nil {
			return err
		}
		o.Port = chosen
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", o.Host, o.Port))
	if err != nil {
		return err
	}
	port := o.Port
	if port == 0 {
		port = listener.Addr().(*net.TCPAddr).Port
	}
	// the run's root reaches this machine's owner through the run directory (ownerkey.go): a
	// start no CLI handed a root to is the case that needs it, and a start that did writes the
	// same file, so the owner's fallback is always the current root. A home that refuses the
	// write keeps serving.
	if err := writeOwnerKey(ctx, o.Home, root); err != nil {
		fmt.Fprintf(o.Stderr, "owner key: %v\n", err)
	}
	// the machine's own name: what /health and the landing page call the hub by, and what the
	// owner's authenticator app lists the hub as
	machine := "huddle"
	if host, err := os.Hostname(); err == nil && host != "" {
		machine = host
	}
	// the serving process's executable, symlinks resolved: the health answer names it, so a
	// start from a newer binary can tell its hub from an older build's
	exe := ""
	if bin, err := os.Executable(); err == nil {
		exe = bin
		if resolved, err := filepath.EvalSymlinks(bin); err == nil {
			exe = resolved
		}
	}
	s := &server{
		o:            o,
		hub:          channel.NewHub(o.Data, o.AutoCreate),
		auth:         auth.New(root, now, func(line string) { fmt.Fprintf(o.Stdout, "huddle: %s\n", line) }, filepath.Join(o.Data, "auth.json")),
		notify:       outside.NewNotifier(o.Data),
		zones:        outside.NewZones(o.Data),
		views:        NewViews(),
		mcp:          NewMcp(o.Version),
		listener:     listener,
		port:         port,
		cookie:       fmt.Sprintf("huddle_session_%d", port),
		started:      jsfmt.ISOTime(now()),
		machine:      machine,
		exe:          exe,
		hosts:        map[string]bool{},
		flights:      &flightTracker{},
		streams:      map[*liveStream]struct{}{},
		registrySubs: map[chan string]struct{}{},
		now:          now,
		alerts:       newAlertFanout(),
		permits:      newPermitWaiters(),
		satQueue:     newSatWaiters(),
		stepUp:       soloStepUp{},
		pushWake:     make(chan struct{}, 1),
	}
	// the registry opens beside the channels: a home whose hub.db cannot open keeps every
	// channel working, with each report refused (the hooks outbox theirs)
	registry, err := hub.Open(ctx, home.Home{Root: o.Home}.HubDB())
	if err != nil {
		fmt.Fprintf(o.Stderr, "registry: %v\n", err)
	} else {
		s.registry = registry
		// pairing rides the same database: its links, devices and sessions live in hub.db, so
		// the terminal that mints a link and the server that spends it share one truth
		rpID, origins := pairRP(o.Home, port)
		pairs, err := auth.NewPairing(registry, auth.PairOptions{
			RPID: rpID, Origins: origins, Now: now,
			Log: func(line string) { fmt.Fprintf(o.Stdout, "huddle: %s\n", line) },
		})
		if err != nil {
			fmt.Fprintf(o.Stderr, "pairing: %v\n", err)
		} else {
			s.pairs = pairs
			// the pairing core carries the passkeys, so it carries the step-up check too; the
			// solo default above stands only where no pairing core could be built
			s.stepUp = pairStepUp{pairs: pairs, now: now}
		}
		// the authenticator core rides the same database: its secret and its recovery digests
		// live in hub.db, so the terminal that enrols and the server that verifies share one
		// truth. The machine's own name is what the owner's app lists the hub as.
		s.totp = auth.NewTOTP(registry, auth.TOTPOptions{
			Machine: machine, Now: now,
			Log: func(line string) { fmt.Fprintf(o.Stdout, "huddle: %s\n", line) },
		})
	}
	// the registry's pid checker stops with the server, before the database closes under it
	pidCtx, stopPidCheck := context.WithCancel(ctx)
	defer stopPidCheck()
	if s.registry != nil {
		safego.Go(s.warn, func() { s.watchPids(pidCtx) })
		safego.Go(s.warn, func() { s.watchPermissions(pidCtx) })
		// the push sender rides the same database: its keys, the subscriptions and the items'
		// bookkeeping all live in hub.db, so a home without one pushes nothing
		s.vapid = newVapidSource(vapidKeystoreStore(), filepath.Join(o.Home, "keys", "vapid"),
			func(line string, args ...any) { fmt.Fprintf(o.Stdout, "huddle: "+line+"\n", args...) })
		s.pushHTTP = &http.Client{Timeout: pushSendTimeout}
		safego.Go(s.warn, func() { s.watchPushes(pidCtx) })
		// every raised alert reaches the sender as it is raised; the sends run on their own, so
		// a slow push service never holds the request that raised the alert
		unsubscribeAlerts := s.alerts.subscribe(func(e AlertEvent) {
			safego.Go(s.warn, func() { s.pushAlertEvent(pidCtx, e) })
		})
		defer unsubscribeAlerts()
		// a satellite keeps its outbound link for the lives of this server (SPEC 3): it is
		// started from the same config the role's listeners read
		s.startSatelliteLink(pidCtx)
	}
	// a tool call through the MCP face fans the channel's broadcast posts out to its linked
	// sessions the same way the HTTP op path does (links.go)
	s.mcp.afterOp = s.overhear
	s.setHosts()
	s.watcher = extras.NewWatcher(s.notify)
	s.watcher.WatchHub(s.hub)
	// every route, radar's included, sits behind the request guards
	var handler http.HandlerFunc = s.serveHTTP
	if s.registry != nil {
		// the boards of the registry's sessions mount here: the tab reads each session's
		// working dir and serves every board it finds under /api/kanban in-process, with no
		// port of its own. Huddle's credential guards the prefix like every other /api path.
		mux := http.NewServeMux()
		s.kanban = kanbantab.Mount(ctx, kanbantab.Options{
			Roots:   s.boardRoots,
			Version: o.Version,
			Now:     now,
			// one audit row per board write the tab runs: the member the credential
			// names, the command and the card, and whether the board took it
			Audit: func(ctx context.Context, r *http.Request, command, key, id string, ok bool) {
				actor := "root"
				if who, named := s.who(r); named && who.Name != "" {
					actor = who.Name
				}
				_, _ = s.audit(ctx, actor, "kanban."+command, key+"/"+id, kanbanAuditDetail{OK: ok})
			},
			// a satellite relays the boards its hub hosts onto it (sat.go): the tab
			// asks per command, and a solo machine's nil link answers nothing
			Relay: s.relayFlowToHub,
			// its reads of those boards come from the hub too, its live event
			// stream included, with the local copy standing in while the hub is away
			ReadRelay: s.readFromHub,
		})
		mux.Handle(kanbantab.Prefix, s.kanban)
		mux.Handle(kanbantab.Prefix+"/", s.kanban)
		kanbanMux := mux
		huddle := handler
		handler = func(w http.ResponseWriter, r *http.Request) {
			if p := r.URL.Path; p == kanbantab.Prefix || strings.HasPrefix(p, kanbantab.Prefix+"/") {
				if _, ok := s.who(r); !ok {
					// a satellite's credential is the one other way in (SPEC 6.7):
					// its machine's boards write through this prefix, on the hub
					// that holds them
					machine, verified := s.satelliteBearer(r)
					if !verified {
						s.refuseCredential(w, r)
						return
					}
					r = r.WithContext(withSatellite(r.Context(), machine))
				}
				kanbanMux.ServeHTTP(w, r)
				return
			}
			huddle(w, r)
		}
	}
	if len(o.Environ) > 0 {
		// radar rides along on this server's mux: /api/radar holds its routes, and the tab
		// answers its health path while it owns radar's lock. Radar's own lock decides who
		// ingests; the hub reads the tab in-process for alerts and attribution.
		radarEnv := domain.EnvLookup{}
		for _, entry := range o.Environ {
			key, value, _ := strings.Cut(entry, "=")
			radarEnv[key] = value
		}
		mux := http.NewServeMux()
		s.radar = radartab.Mount(ctx, mux, "", radartab.MountOptions{
			Env:     radarEnv,
			Version: o.Version,
			Port:    port,
			// the tab answers the hosts this server answers, read live: the hub listener's
			// spellings join the loopback ones when that listener binds, so the Radar tab
			// serves the hub's own name too
			HostAllowed: s.hostAllowed,
			// and it takes the writes this server's own guard takes: the same host list,
			// read the way the guard reads an Origin
			OriginAllowed: func(origin string) bool { return s.hostAllowed(originHost(origin)) },
			Log:           func(line string) { fmt.Fprintln(o.Stdout, line) },
			// the day card's peak sessions and cards done come from this server's registry
			// and boards, which radar does not hold
			Day: radarDay{s: s},
			// and the hub's zone, read per request, so radar's days are the owner's days
			Zone: func() *time.Location { return s.zones.Get().Location() },
			// and the sessions' pids, for each session's CPU and memory, from the registry
			Procs: radarPids{s: s},
			// the stream pings as often as the registry's, so one watchdog fits both
			PingMs: int64(s.o.SSEPingMS),
		})
		s.hub.Radar.Source = s.radar
		s.radarMu.Lock()
		s.radarAlerts = s.radar
		s.radarMu.Unlock()
		// only radar's own paths reach its mux: the mux cleans paths, and huddle's routes keep
		// their exact matching (a dotted path stays a 404, never a redirect). Radar's API rides
		// huddle's credential — the same check every /api route runs — while its health path
		// stays open, because radar's own command line probes that one with no credential.
		huddle := handler
		handler = func(w http.ResponseWriter, r *http.Request) {
			p := r.URL.Path
			if p == radartab.Prefix || strings.HasPrefix(p, radartab.Prefix+"/") {
				// a browser or session with huddle's credential, or a proxying huddle holding
				// the tab token the lock record publishes, reads radar here
				if _, ok := s.who(r); !ok && !s.radar.AnswersToken(auth.Given(r, s.cookie)) {
					s.refuseCredential(w, r)
					return
				}
				mux.ServeHTTP(w, r)
				return
			}
			if p == "/api/health" {
				mux.ServeHTTP(w, r)
				return
			}
			huddle(w, r)
		}
	}
	s.httpSrv = &http.Server{
		Handler:           s.guard(handler),
		ReadHeaderTimeout: 60 * time.Second,
		IdleTimeout:       5 * time.Second,
	}

	names, err := s.hub.Names(ctx)
	if err != nil {
		fmt.Fprintf(o.Stderr, "storage: %v\n", err)
		names = []string{}
	}
	channelsLine := strings.Join(names, ", ")
	if channelsLine == "" {
		channelsLine = "none"
	}
	fmt.Fprintf(o.Stdout, "Huddle %s on http://%s:%d · data %s · channels %s\n",
		o.Version, o.Host, port, s.hub.Where(), channelsLine)
	if o.ShowRoot {
		// the link names the hub's own origin on a hub (the base a passkey is saved for), the
		// loopback listener it answers on everywhere else
		base := fmt.Sprintf("http://127.0.0.1:%d", port)
		if own := hubBaseURL(o.Home); own != "" {
			base = own
		}
		fmt.Fprintf(o.Stdout,
			"root credential (this run only; HUDDLE_TOKEN=… for the CLI): %s\ndashboard: %s/?code=%s\n",
			root, base, s.auth.LoginCode(nil))
	}

	// the home's role decides whether a second listener exists beside this one (roles.go)
	s.applyRole(ctx, handler)

	goneDone := make(chan struct{})
	goneStarted := &atomic.Bool{}
	gone := func(outside.Gone) {
		goneStarted.Store(true)
		stopPidCheck()
		fmt.Fprintln(o.Stdout, "huddle: the plugin is gone from Claude Code — stopping")
		_ = s.listener.Close()                          // no new connections
		s.closeHub()                                    // the hub listener's too, when the role opened one
		s.closeStreams()                                // live streams end
		s.stopRadar()                                   // the radar tab and its lock go with us
		s.stopKanban()                                  // the boards' watches go with us
		s.closePhone()                                  // the phone guide ends with the server
		s.flights.whenIdle(ctx, 30*time.Second)         // requests in flight finish, up to 30 s
		time.Sleep(100 * time.Millisecond)              // their last bytes flush
		_ = s.httpSrv.Close()                           // what outwaited the drain goes with its connection
		if fresh, err := s.hub.Names(ctx); err == nil { // fold every WAL into its file, then close
			for _, n := range fresh {
				if ch, err := s.hub.GetExisting(ctx, n); err == nil {
					_, _ = ch.Store.All(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
				}
			}
		}
		_ = s.hub.Close(ctx)
		if s.registry != nil {
			_ = s.registry.Close()
		}
		outside.RemoveRunFiles(o.Home)                    // the pid (our lock) and the log leave with us
		_ = os.Remove(filepath.Join(o.Data, "auth.json")) // and who was in: nobody joins a gone server
		_ = outside.WriteLeftBehind(o.Home, s.hub.Where(), now())
		removeInstalledService(ctx, o)
		close(goneDone)
	}
	var watch *outside.Watch
	if o.RemovalWatch {
		interval := time.Duration(o.RemovalMS) * time.Millisecond
		if interval <= 0 {
			interval = 10 * time.Second
		}
		watch = outside.WatchRemoval(outside.RemovalOptions{
			Env: o.Env, Plugin: "huddle", Projects: o.Projects, Interval: interval, Gone: gone,
		})
	}

	serveErr := make(chan error, 1)
	safego.Go(s.warn, func() { serveErr <- s.httpSrv.Serve(listener) })

	shutdown := func() {
		stopPidCheck()
		if watch != nil {
			watch.Stop()
		}
		removeOwnerKey(o.Home)
		s.auth.Flush()
		s.stopRadar()
		s.stopKanban()
		s.closePhone()
		s.closeHTTP()
		_ = s.hub.Close(context.Background())
		if s.registry != nil {
			_ = s.registry.Close()
		}
	}

	var sig chan os.Signal
	if o.Signals {
		sig = make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	}
	select {
	case <-ctx.Done():
		shutdown()
		return nil
	case <-sig:
		shutdown()
		return nil
	case <-goneDone:
		return nil
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		if goneStarted.Load() {
			// the closing listener ended the accept loop; the removal finishes on its own
			<-goneDone
			return nil
		}
		removeOwnerKey(o.Home) // this process ends here: its root leaves with it
		return err
	}
}

// removeInstalledService unloads the service `huddle service install` wrote when the home's
// ledger says there is one: the unit file sits outside the plugin, so the plugin's own removal
// restores the machine. Best effort and bounded — a hung service manager never holds the
// shutdown — and what it needs to say goes to the server's own log.
func removeInstalledService(ctx context.Context, o Options) {
	if _, ok := outside.ReadService(ctx, o.Home); !ok {
		return
	}
	bounded, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	env := func(name string) string {
		if value, ok := o.Env[name]; ok && value != "" {
			return value
		}
		return os.Getenv(name) // XDG_CONFIG_HOME, which Options' env does not carry
	}
	if err := outside.UninstallService(bounded, outside.ServiceOptions{
		Home: o.Home, Env: env, UID: os.Getuid(), Runner: outside.CommandRunner{},
	}); err != nil {
		fmt.Fprintf(o.Stdout, "huddle: the installed service stayed: %v\n", err)
	}
}

// stopRadar ends the radar tab, releasing the lock it took; a tab that proxies (an owner holds
// the lock) stops with nothing to release. Nil (no tab mounted) is a no-op.
func (s *server) stopRadar() {
	if s.radar != nil {
		s.radar.Stop()
	}
}

// stopKanban ends the kanban tab's board watches. Nil (no tab mounted) is a no-op.
func (s *server) stopKanban() {
	if s.kanban != nil {
		s.kanban.Stop()
	}
}

// boardRoots answers the dirs the kanban tab looks for boards from: the working dir of every
// live session the registry knows — an ended session's cwd drops out — then the boards the hub
// remembers from earlier sightings (boards_seen.go).
func (s *server) boardRoots(ctx context.Context) []string {
	rows, err := s.registry.Sessions(ctx, hub.SessionsFilter{}, s.now())
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.State != hub.StateEnded && row.Cwd != "" {
			out = append(out, row.Cwd)
		}
	}
	return s.seenBoards(ctx, out)
}

// closeHTTP ends the listener and the connections: a client holding a keep-alive connection
// open never gets to hold the shutdown hostage.
func (s *server) closeHTTP() {
	_ = s.listener.Close()
	s.closeHub()
	s.httpSrv.SetKeepAlivesEnabled(false)
	closed := make(chan struct{})
	safego.Go(s.warn, func() {
		_ = s.httpSrv.Close()
		close(closed)
	})
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
	}
}

func (s *server) setHosts() {
	s.hostsMu.Lock()
	defer s.hostsMu.Unlock()
	// loopback names for our own port are always allowed (a rebinding attack arrives with its
	// own hostname); HUDDLE_HOSTS and the config's hub.host add the rest
	s.hosts[fmt.Sprintf("127.0.0.1:%d", s.port)] = true
	s.hosts[fmt.Sprintf("localhost:%d", s.port)] = true
	for _, h := range s.o.Hosts {
		s.hosts[h] = true
	}
}

func (s *server) hostAllowed(host string) bool {
	s.hostsMu.Lock()
	defer s.hostsMu.Unlock()
	return s.hosts[host]
}

func (s *server) closeStreams() {
	s.streamsMu.Lock()
	streams := make([]*liveStream, 0, len(s.streams))
	for stream := range s.streams {
		streams = append(streams, stream)
	}
	s.streamsMu.Unlock()
	for _, stream := range streams {
		stream.cancel()
	}
}

// writeJSON writes one JSON answer with the headers every API answer carries.
func (s *server) writeJSON(w http.ResponseWriter, status int, body any, extra map[string]string) {
	encoded, err := domain.MarshalCompactJSON(body)
	if err != nil {
		encoded = []byte(`{"error":"could not encode"}`)
		status = 500
	}
	header := w.Header()
	header.Set("Content-Type", "application/json")
	header.Set("Cache-Control", "no-store")
	for key, value := range extra {
		header.Set(key, value)
	}
	header.Set("Content-Length", strconv.Itoa(len(encoded)))
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}

// writeText writes one plain answer. A caller that names no content type gets the one the plain
// responses carry everywhere: text/plain with the utf-8 charset, no space.
func writeText(w http.ResponseWriter, status int, body string, extra map[string]string) {
	header := w.Header()
	for key, value := range extra {
		header.Set(key, value)
	}
	if header.Get("Content-Type") == "" {
		header.Set("Content-Type", "text/plain;charset=utf-8")
	}
	header.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

type errorAnswer struct {
	Error string `json:"error"`
}

type signinAnswer struct {
	Error  string `json:"error"`
	Signin bool   `json:"signin"`
}

// body reads one request's JSON body the way the channel keeps it: no credential, code or invite
// of this run in it.
func (s *server) body(r *http.Request) (*jsfmt.Value, error) {
	if r.Method != http.MethodPost && r.Method != http.MethodPatch && r.Method != http.MethodPut {
		return jsfmt.Object(nil, nil), nil
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, &channel.HuddleError{Status: 400, Msg: "body is not JSON"}
	}
	if len(raw) == 0 {
		return jsfmt.Object(nil, nil), nil
	}
	value, err := jsfmt.DecodeValue(raw)
	if err != nil {
		return nil, &channel.HuddleError{Status: 400, Msg: "body is not JSON"}
	}
	return s.auth.Redact(value), nil
}

// actingAs answers who the request says it acts as: the query's as, else the header.
func actingAs(r *http.Request) string {
	if values, ok := r.URL.Query()["as"]; ok && len(values) > 0 {
		return values[0]
	}
	return r.Header.Get("x-huddle-as")
}

// dashboardFile reads one file of the embedded UI.
func dashboardFile(name string) ([]byte, error) {
	return fs.ReadFile(web.FS(), name)
}

// who reads the credential one request carries — the browser session cookie (a paired device's
// own included) or the x-huddle-token header — and ok when it names someone. This is the one
// check every guarded path runs, radar's routes included.
func (s *server) who(r *http.Request) (auth.Who, bool) {
	who, ok := s.auth.Who(auth.Given(r, s.cookie))
	if !ok && s.pairs != nil {
		// a paired device's cookie: its session dies with the device, so a removal is
		// felt on the very next request
		who, ok = s.pairs.Session(r.Context(), auth.PairCookie(r))
	}
	if !ok {
		// a satellite the hub listener's gate verified carries its machine in the context
		// (roles.go): the owner's machine, not a session or a browser
		if machine, satellite := satelliteFrom(r.Context()); satellite {
			who, ok = auth.Who{Name: "satellite:" + machine, Kind: auth.KindSatellite, Satellite: machine}, true
		}
	}
	return who, ok
}

// refuseCredential answers a request no credential vouches for: a browser is told how to sign
// in, a session holding a token that no longer works how to get back in.
func (s *server) refuseCredential(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("x-huddle-token") != "" {
		s.writeJSON(w, 401, errorAnswer{Error: session401}, nil)
		return
	}
	s.writeJSON(w, 401, signinAnswer{Error: signedOut, Signin: true}, nil)
}

// serveHTTP routes one request: the sign-in link, the join, then the credential check every /api
// and /mcp request goes through. The guard middleware (guard.go) has already run.
func (s *server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	isLive := livePath.MatchString(r.URL.Path)
	if !isLive {
		release := s.flights.track()
		defer release()
	}

	query := r.URL.Query()
	path := r.URL.Path

	// the browser's sign-in: a one-time code becomes a cookie only this origin sends, then the
	// clean URL, so the code leaves the address bar
	if path == "/" && query.Has("code") {
		cred, ok := s.auth.Redeem(query.Get("code"))
		if !ok {
			writeText(w, 401, signedOutPage(s.o.Version), map[string]string{
				"Content-Type": "text/html; charset=utf-8", "Cache-Control": "no-store",
			})
			return
		}
		rest := urlWithoutCode(r.URL.RawQuery)
		location := "/"
		if rest != "" {
			location = "/?" + rest
		}
		// the sign-in is audited before the browser gets its cookie: a security event that
		// cannot be recorded never happens (SPEC 7.7)
		if err := s.auditSignin(r.Context(), cred); err != nil {
			writeText(w, 503, "the audit log is unavailable, so this sign-in is refused: open a fresh link once this machine's hub.db is healthy", map[string]string{"Cache-Control": "no-store"})
			return
		}
		secure := ""
		if servedOverHTTPS(r) {
			secure = "; Secure"
		}
		w.Header().Set("Set-Cookie",
			fmt.Sprintf("%s=%s; Path=/; HttpOnly; SameSite=Strict%s", s.cookie, urlEscape(cred), secure))
		w.Header().Set("Location", location)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(303)
		return
	}

	if path == "/api/join" && r.Method == http.MethodPost {
		s.joinRoute(w, r)
		return
	}

	// the satellite routes gate themselves: the join answers an invite, everything else the
	// machine's credential — neither is a browser session
	if strings.HasPrefix(path, satPathPrefix) {
		s.satRoute(w, r, path)
		return
	}

	var me auth.Who
	credentialed := false
	// the pairing and sign-in ceremonies are how a browser earns a session: they run before any
	// credential exists (every other /api and /mcp path demands one)
	if (strings.HasPrefix(path, "/api/") || path == "/mcp" || strings.HasPrefix(path, "/mcp/")) &&
		path != "/api/pair/begin" && path != "/api/pair/finish" && !strings.HasPrefix(path, "/api/login/") {
		credentialed = true
		who, ok := s.who(r)
		if !ok {
			s.refuseCredential(w, r)
			return
		}
		me = who
		s.seedZone(w, r, me)
		as := actingAs(r)
		if as != "" && !auth.MayAct(me, as) {
			s.writeJSON(w, 403, errorAnswer{Error: fmt.Sprintf(
				"this credential is %s's: it acts as %s or %s.<role>, not %s", me.Name, me.Name, me.Name, as)}, nil)
			return
		}
		// a device session in use rolls forward: a request more than an hour past its last
		// renewal buys seven more days, and the answer carries the cookie's fresh life. The
		// stream's own heartbeats never run here, so an open stream alone renews nothing.
		if s.pairs != nil && me.Device != "" && s.pairs.Renew(r.Context(), auth.PairCookie(r)) {
			w.Header().Set("Set-Cookie", auth.PairCookieSet(servedOverHTTPS(r), auth.PairCookie(r)))
		}
	}

	err := s.route(w, r, path, query, me, credentialed)
	if err != nil {
		var he *channel.HuddleError
		if errors.As(err, &he) {
			s.writeJSON(w, he.Status, errorAnswer{Error: he.Msg}, nil)
			return
		}
		s.writeJSON(w, 500, errorAnswer{Error: err.Error()}, nil)
	}
}

// urlWithoutCode renders the raw query with the code parameter dropped, the rest as it came.
func urlWithoutCode(rawQuery string) string {
	parts := []string{}
	for _, part := range strings.Split(rawQuery, "&") {
		if part == "" || strings.HasPrefix(part, "code=") {
			continue
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "&")
}

func urlEscape(s string) string {
	var out strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == '~':
			out.WriteRune(r)
		default:
			for _, b := range []byte(string(r)) {
				fmt.Fprintf(&out, "%%%02X", b)
			}
		}
	}
	return out.String()
}

// joinRoute takes an invite and a wished name, and answers a credential bound to a name nothing
// else holds.
func (s *server) joinRoute(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		raw = nil
	}
	body, err := jsfmt.DecodeValue(raw)
	if err != nil || body == nil {
		body = jsfmt.Object(nil, nil) // raw: the body would redact the token it carries
	}
	name := textOf(fieldOf(body, "name"))
	if !channel.NameRe.MatchString(name) || strings.Contains(name, ".") || name == channel.Owner {
		// the rule prints with its delimiters, the way a JavaScript RegExp reads itself
		s.writeJSON(w, 400, errorAnswer{Error: "name must match /" + channel.NameRe.String() + "/ (no subagent, not owner)"}, nil)
		return
	}
	token := textOf(fieldOf(body, "token"))
	// unique: the name is only a wish (the joiner did not choose it): one no member and no
	// session of the channel holds, name-2, name-3, …
	if truthy(fieldOf(body, "unique")) {
		if peek, ok := s.auth.Peek(token); ok {
			chName := ""
			if peek.Channel != nil {
				chName = *peek.Channel
			} else if c := textOf(fieldOf(body, "channel")); c != "" {
				chName = c
			}
			taken := map[string]bool{channel.Owner: true}
			for _, m := range peek.Members {
				taken[m] = true
			}
			if chName != "" {
				if exists, _ := s.hub.Exists(r.Context(), chName); exists {
					if ch, err := s.hub.GetExisting(r.Context(), chName); err == nil {
						if sessions, err := ch.Sessions(r.Context()); err == nil {
							for _, sess := range sessions {
								taken[sess.Name] = true
							}
						}
					}
				}
			}
			base := jsfmt.UTF16Slice(name, 0, 28)
			for i := 2; taken[name]; i++ {
				name = fmt.Sprintf("%s-%d", base, i)
			}
		}
	}
	result := s.auth.Join(token, name)
	if result.Err != "" {
		s.writeJSON(w, 401, errorAnswer{Error: "invalid or expired join token: ask the owner for a new join command"}, nil)
		return
	}
	s.writeJSON(w, 200, struct {
		Credential string  `json:"credential"`
		Name       string  `json:"name"`
		Channel    *string `json:"channel"`
	}{result.Credential, result.Name, result.Channel}, nil)
}

// route answers everything past the guards. credentialed says the request already proved who it
// is (every /api and /mcp path except /api/join).
func (s *server) route(w http.ResponseWriter, r *http.Request, path string, query map[string][]string,
	me auth.Who, credentialed bool) error {
	if credentialed && path == "/api/settings" {
		body, err := s.body(r)
		if err != nil {
			return err
		}
		state, err := extras.Settings(r.Method, s.notify, s.zones, me.Name, body)
		if err != nil {
			return err
		}
		s.writeJSON(w, 200, state, nil)
		return nil
	}
	if credentialed && strings.HasPrefix(path, "/api/") && !strings.HasPrefix(path, "/api/c/") {
		if answered, err := s.adminRoute(w, r, path, me); answered || err != nil {
			return err
		}
	}
	switch {
	case credentialed && path == "/api/registry/report" && r.Method == http.MethodPost:
		return s.reportRoute(w, r)
	case credentialed && path == "/api/registry/sessions" && r.Method == http.MethodGet:
		return s.registryListRoute(w, r)
	case credentialed && registrySessionPath.MatchString(path) && r.Method == http.MethodGet:
		return s.registrySessionRoute(w, r, path)
	// the audit log's read (SPEC 7.7): owner-only, newest first, one page at a time
	case credentialed && path == "/api/audit" && r.Method == http.MethodGet:
		return s.auditRoute(w, r, me)
	case credentialed && path == "/api/registry/live" && r.Method == http.MethodGet:
		return s.registryLiveRoute(w, r)
	case credentialed && path == "/api/registry/push-ack" && r.Method == http.MethodPost:
		return s.pushAckRoute(w, r)
	case credentialed && path == "/api/needs" && r.Method == http.MethodGet:
		return s.needsRoute(w, r)
	case credentialed && path == "/api/needs" && r.Method == http.MethodPost:
		return s.permitCreateRoute(w, r)
	case credentialed && permitItemPath.MatchString(path):
		return s.permitItemRoute(w, r, path, me)
	case credentialed && needsItemPath.MatchString(path) && r.Method == http.MethodPost:
		return s.needsItemRoute(w, r, path)
	// Web Push (SPEC 7.6): the browser's subscription, its notification choice, the public key
	// it subscribes with, and the focus report that keeps the other devices quiet
	case credentialed && path == "/api/push/key" && r.Method == http.MethodGet:
		return s.pushKeyRoute(w, r)
	case credentialed && path == "/api/push/subscribe" && r.Method == http.MethodPost:
		return s.pushSubscribeRoute(w, r, me)
	case credentialed && path == "/api/push/subscribe" && r.Method == http.MethodDelete:
		return s.pushUnsubscribeRoute(w, r, me)
	case credentialed && path == "/api/push/notify" && r.Method == http.MethodGet:
		return s.pushNotifyRoute(w, r, me)
	case credentialed && path == "/api/push/notify" && r.Method == http.MethodPost:
		return s.pushSetNotifyRoute(w, r, me)
	case credentialed && path == "/api/push/focus" && r.Method == http.MethodPost:
		return s.pushFocusRoute(w, r, me)
	case credentialed && sessionActPath.MatchString(path) && r.Method == http.MethodPost:
		return s.sessionActRoute(w, r, path, me)
	case credentialed && path == "/api/send" && r.Method == http.MethodPost:
		return s.sendRoute(w, r, me)
	// links (SPEC 8): the tie itself owes the step-up like a send, its level move and its read do
	// not, and the unlink ends it gated the same way
	case credentialed && path == "/api/links" && r.Method == http.MethodPost:
		return s.linkRoute(w, r, me)
	case credentialed && path == "/api/links" && r.Method == http.MethodGet:
		return s.linksListRoute(w, r)
	case credentialed && linkPath.MatchString(path) && r.Method == http.MethodPatch:
		return s.linkLevelRoute(w, r, path)
	case credentialed && linkPath.MatchString(path) && r.Method == http.MethodDelete:
		return s.unlinkRoute(w, r, path, me)
	case credentialed && linksOfPath.MatchString(path) && r.Method == http.MethodGet:
		return s.linksOfRoute(w, r, path)
	// brief and hand over (SPEC 8): a digest on demand, and a session's open work given away —
	// both carry a session's words or tasks into another, so both pass the step-up like a send
	case credentialed && path == "/api/brief" && r.Method == http.MethodPost:
		return s.briefRoute(w, r, me)
	case credentialed && path == "/api/handover" && r.Method == http.MethodPost:
		return s.handoverRoute(w, r, me)
	case path == "/health":
		names, err := s.hub.Names(r.Context())
		if err != nil {
			return err
		}
		// the answer names both processes: the hub's own pid and the router's beside it
		s.writeJSON(w, 200, struct {
			OK         bool         `json:"ok"`
			Version    string       `json:"version"`
			Started    string       `json:"started"`
			Channels   int          `json:"channels"`
			Home       string       `json:"home"`
			Machine    string       `json:"machine"`
			Name       string       `json:"name,omitempty"`
			Executable string       `json:"executable"`
			PID        int          `json:"pid"`
			Router     routerHealth `json:"router"`
		}{true, s.o.Version, s.started, len(names), s.o.Home, s.machine, hubName(s.o.Home), s.exe,
			os.Getpid(), routerOf(r.Context(), s.o.Environ)}, nil)
		return nil
	case path == "/" || path == "/index.html":
		return s.staticFile(w, "index.html", "text/html; charset=utf-8", "no-store")
	case strings.HasPrefix(path, "/static/") && hashedName.MatchString(path[len("/static/"):]):
		return s.hashedRoute(w, r, path[len("/static/"):])
	case strings.HasPrefix(path, "/static/") && !strings.Contains(path, ".."):
		name := path[len("/static/"):]
		info, err := fs.Stat(web.FS(), name)
		if err != nil || info.IsDir() {
			writeText(w, 404, "not found", nil)
			return nil
		}
		return s.staticFile(w, name, staticTypes[name[strings.LastIndex(name, "."):]], "no-store")
	case strings.HasPrefix(path, "/fonts/"):
		return s.fontRoute(w, path)
	case path == "/signed-out.css":
		writeText(w, 200, signedOutCSS, map[string]string{"Content-Type": staticTypes[".css"]})
		return nil
	case path == "/manifest.webmanifest":
		return s.staticFile(w, "manifest.webmanifest", staticTypes[".webmanifest"], shortCache)
	case path == "/ca.crt" || path == "/ca.mobileconfig":
		return s.caRoute(w, r, path == "/ca.mobileconfig")
	case path == "/sw.js":
		return s.swRoute(w)
	case strings.HasPrefix(path, "/icons/"):
		return s.iconRoute(w, path)
	case path == "/connect.md":
		writeText(w, 200, connectDoc, map[string]string{"Content-Type": staticTypes[".md"]})
		return nil
	case path == "/api/channels" && r.Method == http.MethodGet:
		list, err := s.hub.List(r.Context())
		if err != nil {
			return err
		}
		s.writeJSON(w, 200, list, nil)
		return nil
	case path == "/api/channels" && r.Method == http.MethodPost:
		return s.createChannel(w, r)
	case credentialed && channelNamePath.MatchString(path) && r.Method == http.MethodDelete:
		return s.removeChannel(w, r, channelNamePath.FindStringSubmatch(path)[1], me)
	case path == "/api/pair/begin" && r.Method == http.MethodPost:
		return s.pairBeginRoute(w, r)
	case path == "/api/pair/finish" && r.Method == http.MethodPost:
		return s.pairFinishRoute(w, r)
	case path == "/api/login/available" && r.Method == http.MethodGet:
		return s.loginAvailableRoute(w, r)
	case path == "/api/login/begin" && r.Method == http.MethodPost:
		return s.loginBeginRoute(w, r)
	case path == "/api/login/finish" && r.Method == http.MethodPost:
		return s.loginFinishRoute(w, r)
	case path == "/api/login/totp" && r.Method == http.MethodPost:
		return s.loginTotpRoute(w, r)
	case credentialed && path == "/api/totp/begin" && r.Method == http.MethodPost:
		return s.totpBeginRoute(w, r, me)
	case credentialed && path == "/api/totp/finish" && r.Method == http.MethodPost:
		return s.totpFinishRoute(w, r, me)
	case credentialed && path == "/api/totp" && r.Method == http.MethodDelete:
		return s.totpRemoveRoute(w, r, me)
	case credentialed && path == "/api/passkey/begin" && r.Method == http.MethodPost:
		return s.passkeyBeginRoute(w, r, me)
	case credentialed && path == "/api/passkey/finish" && r.Method == http.MethodPost:
		return s.passkeyFinishRoute(w, r, me)
	case credentialed && path == "/api/pair/mint" && r.Method == http.MethodPost:
		return s.pairMintRoute(w, r, me)
	case credentialed && path == "/api/stepup/begin" && r.Method == http.MethodPost:
		return s.stepUpBeginRoute(w, r, me)
	case credentialed && path == "/api/stepup/finish" && r.Method == http.MethodPost:
		return s.stepUpFinishRoute(w, r, me)
	case credentialed && path == "/api/devices" && r.Method == http.MethodGet:
		return s.devicesRoute(w, r)
	case credentialed && devicePath.MatchString(path) && r.Method == http.MethodDelete:
		return s.deviceRemoveRoute(w, r, path, me)
	// the phone guide the dashboard's "Add a phone" shows: started here, its QR served beside
	// it (phone.go)
	case credentialed && path == "/api/phone" && r.Method == http.MethodPost:
		return s.phoneStartRoute(w, r, me)
	case credentialed && path == "/api/phone/qr.svg" && r.Method == http.MethodGet:
		return s.phoneQRRoute(w, r)
	case path == "/pair":
		// the link `huddle pair` prints opens the app, which reads the token from the fragment
		// the browser never sends; the pair screen itself is the UI's to build
		return s.staticFile(w, "index.html", "text/html; charset=utf-8", "no-store")
	}
	if m := channelPath.FindStringSubmatch(path); m != nil {
		// a session's first op may create the channel (join); everything else needs it to exist
		rest := ""
		if len(m) > 2 {
			rest = m[2]
		}
		var ch *channel.Channel
		var err error
		if rest == "op/join" {
			ch, err = s.hub.Get(r.Context(), m[1])
		} else {
			ch, err = s.hub.GetExisting(r.Context(), m[1])
		}
		if err != nil {
			return err
		}
		return s.channelRoute(w, r, ch, rest, query)
	}
	if m := mcpPath.FindStringSubmatch(path); m != nil || path == "/mcp" {
		name := ""
		if m != nil {
			name = m[1]
		} else if values, ok := query["channel"]; ok && len(values) > 0 {
			name = values[0]
		} else {
			name = r.Header.Get("x-huddle-channel")
		}
		return s.mcpRoute(w, r, name)
	}
	s.writeJSON(w, 404, errorAnswer{Error: "not found"}, nil)
	return nil
}

// reportRoute records one session report from a hook (SPEC 5.1): one row in the home's hub.db
// per event, the session's row upserted. A report that names no session or no event is refused,
// so a torn body never reaches the registry. Once it lands, the session's channel hears its
// computed row on its live stream.
func (s *server) reportRoute(w http.ResponseWriter, r *http.Request) error {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return &channel.HuddleError{Status: 400, Msg: "body is not JSON"}
	}
	var rep hub.Report
	if err := json.Unmarshal(raw, &rep); err != nil ||
		rep.SessionID == "" || rep.LastEvent == "" || rep.LastEventAt == "" {
		return &channel.HuddleError{Status: 400, Msg: "a report needs session_id, last_event and last_event_at"}
	}
	if s.registry == nil {
		return &channel.HuddleError{Status: 500, Msg: "the registry's database is unavailable"}
	}
	if err := s.registry.Report(r.Context(), rep); err != nil {
		return err
	}
	if rep.Digest != "" {
		s.shareDigest(r.Context(), rep)
	}
	s.recordRadarHook(r.Context(), rep)
	s.emitRegistrySession(r.Context(), rep.SessionID)
	// the report may have grown a question or a turn item: the sweep hears of it at once
	s.wakePushes()
	// the session's next PostToolUse or Stop is its terminal answering the prompt its open
	// permission items wait on: they are withdrawn, and their polls answer with no decision
	if rep.LastEvent == "PostToolUse" || rep.LastEvent == "Stop" {
		s.withdrawPermissions(r.Context(), rep.SessionID)
	}
	s.writeJSON(w, 200, struct {
		OK bool `json:"ok"`
	}{true}, nil)
	return nil
}

// registryListRoute answers the registry's read (SPEC 5.3): every session with its state
// computed when read, filtered by machine, project and state.
func (s *server) registryListRoute(w http.ResponseWriter, r *http.Request) error {
	if s.registry == nil {
		return &channel.HuddleError{Status: 500, Msg: "the registry's database is unavailable"}
	}
	query := r.URL.Query()
	filter := hub.SessionsFilter{
		Machine: query.Get("machine"), Project: query.Get("project"), State: query.Get("state"),
	}
	if filter.State != "" && !hub.KnownState(filter.State) {
		return &channel.HuddleError{
			Status: 400, Msg: "state must be one of " + hub.StateNames(),
		}
	}
	// the transcripts can settle a row's state, so the state filter reads the rows they settled
	state := filter.State
	filter.State = ""
	now := s.now()
	stored, err := s.registry.Sessions(r.Context(), filter, now)
	if err != nil {
		return err
	}
	listed := make(map[string]bool, len(stored))
	ids := make([]string, 0, len(stored))
	for _, row := range stored {
		listed[row.SessionID] = true
		if row.State != hub.StateEnded {
			ids = append(ids, row.SessionID)
		}
	}
	var unreported []radartab.RunningSession
	if s.radar != nil {
		for _, running := range s.radar.RunningSessions(r.Context()) {
			if !listed[running.ID] {
				unreported = append(unreported, running)
				ids = append(ids, running.ID)
			}
		}
	}
	times := s.transcriptTimes(ids)
	for i := range stored {
		s.seeTranscript(r.Context(), times, now, &stored[i])
	}
	// a running session that never reported gets a row of its own once its transcript is on disk
	for _, running := range unreported {
		if at, ok := times[running.ID]; ok {
			stored = append(stored, hub.TranscriptRow(running.ID, running.Cwd, at, now))
		}
	}
	sessions := make([]hub.SessionRow, 0, len(stored))
	for _, row := range stored {
		if (filter.Machine != "" && row.Machine != filter.Machine) ||
			(filter.Project != "" && row.Project != filter.Project) || (state != "" && row.State != state) {
			continue
		}
		s.fillRadarFacts(r.Context(), &row)
		sessions = append(sessions, row)
	}
	s.writeJSON(w, 200, struct {
		Sessions []hub.SessionRow `json:"sessions"`
	}{sessions}, nil)
	return nil
}

// transcriptTimes reads when the sessions' transcripts last changed, by id; no tab mounted reads
// none.
func (s *server) transcriptTimes(ids []string) map[string]time.Time {
	if s.radar == nil {
		return nil
	}
	return s.radar.TranscriptTimes(ids)
}

// seeTranscript joins a row's transcript to it: a row whose transcript runs ahead of its reports
// while its pid lives reads hooks stale. The pid is probed only when the transcript runs ahead,
// and the probe only looks.
func (s *server) seeTranscript(ctx context.Context, times map[string]time.Time, now time.Time, row *hub.SessionRow) {
	at, ok := times[row.SessionID]
	if !ok {
		return
	}
	pid := row.Pid
	row.SeeTranscript(at, func() bool {
		if pid <= 0 {
			return false
		}
		_, alive := pidCommandOf(ctx, pid)
		return alive
	}, now)
}

// registrySessionRoute answers one session with its last events (SPEC 5.3). The read is the
// owner opening the session's screen: it marks the session opened, which is what a later Stop
// checks before growing a turn item (SPEC 6.1: a finished turn the owner has not opened). Each
// turn item the read answers streams as a needs.item, and the session's row after them, so every
// open list drops it at once.
func (s *server) registrySessionRoute(w http.ResponseWriter, r *http.Request, path string) error {
	if s.registry == nil {
		return &channel.HuddleError{Status: 500, Msg: "the registry's database is unavailable"}
	}
	id := registrySessionPath.FindStringSubmatch(path)[1]
	answered, err := s.registry.OpenTheSession(r.Context(), id, jsfmt.ISOTime(s.now()))
	if err != nil {
		return err
	}
	for _, item := range answered {
		s.emitItem(item)
	}
	if len(answered) > 0 {
		s.emitRegistrySession(r.Context(), id)
	}
	now := s.now()
	detail, err := s.registry.Session(r.Context(), id, now)
	if err != nil {
		return err
	}
	times := s.transcriptTimes([]string{id})
	if detail != nil {
		s.seeTranscript(r.Context(), times, now, &detail.SessionRow)
	} else if at, ok := times[id]; ok && s.radar != nil {
		// a running session that never reported reads the row its transcript gives it
		for _, running := range s.radar.RunningSessions(r.Context()) {
			if running.ID == id {
				detail = &hub.SessionDetail{
					SessionRow: hub.TranscriptRow(running.ID, running.Cwd, at, now),
					Events:     []hub.SessionEvent{},
				}
				break
			}
		}
	}
	if detail == nil {
		return &channel.HuddleError{Status: 404, Msg: "no session " + id}
	}
	s.fillRadarFacts(r.Context(), &detail.SessionRow)
	s.writeJSON(w, 200, detail, nil)
	return nil
}

// reportHookPayload is one reported event shaped as the Claude Code hook payload the radar
// spool records: the event, the session it belongs to and where it ran.
type reportHookPayload struct {
	HookEventName string `json:"hook_event_name"`
	SessionID     string `json:"session_id"`
	Cwd           string `json:"cwd,omitempty"`
}

// warn writes one line of the server's own warnings to its stderr: the line a recovered
// panic in a background goroutine leaves behind.
func (s *server) warn(line string) { fmt.Fprintln(s.o.Stderr, line) }

// recordRadarHook hands one reported event to the radar tab, so the registry's sessions appear
// in the dashboard even where the radar plugin's own hook never ran. A proxying tab records
// nothing (the owner's own hook does); a record that cannot land is logged once and never fails
// the report it rode on.
func (s *server) recordRadarHook(ctx context.Context, rep hub.Report) {
	if s.radar == nil || !s.radar.Owns {
		return
	}
	raw, err := domain.MarshalCompactJSON(reportHookPayload{
		HookEventName: rep.LastEvent, SessionID: rep.SessionID, Cwd: rep.Cwd,
	})
	if err != nil {
		fmt.Fprintf(s.o.Stderr, "radar: the report of %s could not be shaped for the spool: %v\n", rep.SessionID, err)
		return
	}
	if _, ok := s.radar.RecordHook(ctx, string(raw)); !ok {
		fmt.Fprintf(s.o.Stderr, "radar: the report of %s did not reach the spool\n", rep.SessionID)
	}
}

// fillRadarFacts reads each row's cost and provider from the radar tab in-process and writes
// them onto the row; no tab mounted or no answer for a session leaves both null.
func (s *server) fillRadarFacts(ctx context.Context, rows ...*hub.SessionRow) {
	if s.radar == nil {
		return
	}
	for _, row := range rows {
		if row == nil {
			continue
		}
		facts := s.radar.FactsOf(ctx, row.SessionID)
		row.CostUSD = facts.CostUSD
		row.Provider = facts.Provider
	}
}

// registryLiveRoute serves one SSE client over the whole registry (SPEC 5.3's live updates, for
// the Now list): every session's computed row as it changes, whatever channel the session is in
// or whether it is in one at all — the list the browser keeps spans every channel, so no single
// channel's stream can feed it.
func (s *server) registryLiveRoute(w http.ResponseWriter, r *http.Request) error {
	if s.registry == nil {
		return &channel.HuddleError{Status: 500, Msg: "the registry's database is unavailable"}
	}
	controller := http.NewResponseController(w)
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-store")
	header.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	write := func(frame string) bool {
		if _, err := io.WriteString(w, frame); err != nil {
			return false
		}
		_ = controller.Flush()
		return true
	}

	ctx, cancel := context.WithCancel(r.Context())
	stream := &liveStream{cancel: cancel}
	frames := make(chan string, 256)
	s.registrySubsMu.Lock()
	s.registrySubs[frames] = struct{}{}
	s.registrySubsMu.Unlock()
	s.streamsMu.Lock()
	s.streams[stream] = struct{}{}
	s.streamsMu.Unlock()
	defer func() {
		s.registrySubsMu.Lock()
		delete(s.registrySubs, frames)
		s.registrySubsMu.Unlock()
		s.streamsMu.Lock()
		delete(s.streams, stream)
		s.streamsMu.Unlock()
		cancel()
	}()

	hello, err := domain.MarshalCompactJSON(struct {
		Type string `json:"type"`
	}{"hello"})
	if err != nil || !write("retry: 3000\ndata: "+string(hello)+"\n\n") {
		return nil
	}
	// a data frame, not a comment: EventSource never surfaces a comment, and the page's
	// watchdog replaces a stream it hears nothing from for 40 s
	heartbeat := time.NewTicker(s.ssePingEvery())
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case frame := <-frames:
			if !write(frame) {
				return nil
			}
		case <-heartbeat.C:
			if !write(pingFrame) {
				return nil
			}
		}
	}
}

// pingFrame is the browser streams' heartbeat, a data frame the page's watchdog can count.
const pingFrame = "data: {\"type\":\"ping\"}\n\n"

// ssePingEvery is the browser streams' ping interval: SSEPingMS, or 15 s.
func (s *server) ssePingEvery() time.Duration {
	if s.o.SSEPingMS > 0 {
		return time.Duration(s.o.SSEPingMS) * time.Millisecond
	}
	return 15 * time.Second
}

// fanRegistryRow hands one encoded frame to every open registry stream; a client too slow to
// keep up loses that frame, not the stream.
func (s *server) fanRegistryRow(frame string) {
	s.registrySubsMu.Lock()
	defer s.registrySubsMu.Unlock()
	for frames := range s.registrySubs {
		select {
		case frames <- frame:
		default:
		}
	}
}

// emitItem fans one item's row out as a needs.item event to every open registry stream. Best
// effort, like every emit.
func (s *server) emitItem(item hub.Item) {
	encoded, err := domain.MarshalCompactJSON(struct {
		Type string   `json:"type"`
		Data hub.Item `json:"data"`
	}{"needs.item", item})
	if err != nil {
		return
	}
	s.fanRegistryRow("data: " + string(encoded) + "\n\n")
}

// emitRegistrySession fans one session's computed row out live: to every open registry stream
// (the Now list follows every channel), and to the session's own channel's stream. Best
// effort: a report already landed, and a stream that cannot take the event loses nothing but
// the event.
func (s *server) emitRegistrySession(ctx context.Context, sessionID string) {
	if s.registry == nil {
		return
	}
	row, err := s.registry.SessionRow(ctx, sessionID, s.now())
	if err != nil || row == nil {
		return
	}
	s.fillRadarFacts(ctx, row)
	encoded, err := domain.MarshalCompactJSON(struct {
		Type string          `json:"type"`
		Data *hub.SessionRow `json:"data"`
	}{"registry.session", row})
	if err != nil {
		return
	}
	s.fanRegistryRow("data: " + string(encoded) + "\n\n")
	if row.Channel == "" {
		return
	}
	ch, err := s.hub.GetExisting(ctx, row.Channel)
	if err != nil {
		return
	}
	ch.Emit("registry.session", row)
}

// watchPids ends the sessions whose claude process is gone (SPEC 5.2: "its machine reports the
// pid gone"): a session that dies without its SessionEnd — a crash, a power loss — would read
// at work forever otherwise. The probe only looks; nothing is ever signalled.
func (s *server) watchPids(ctx context.Context) {
	interval := time.Duration(s.o.PidCheckMS) * time.Millisecond
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			marked, err := s.registry.MarkEndedPids(ctx, jsfmt.ISOTime(s.now()), pidCommandOf)
			if err != nil {
				fmt.Fprintf(s.o.Stderr, "registry: pid check: %v\n", err)
				continue
			}
			// each end the checker found reaches the live streams too, so an open list does not
			// wait for a reconnect to drop the session
			for _, id := range marked {
				s.emitRegistrySession(ctx, id)
			}
		}
	}
}

// pidCommandOf answers a pid's command line and whether the pid lives: a pid ps cannot name is
// gone, so no signal is ever sent to find out. Each probe is bounded, so a wedged ps holds one
// pass only.
func pidCommandOf(ctx context.Context, pid int) (string, bool) {
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(probeCtx, "ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

// staticFile serves one embedded file under the Cache-Control it is given.
func (s *server) staticFile(w http.ResponseWriter, name, contentType, cache string) error {
	body, err := dashboardFile(name)
	if err != nil {
		s.writeJSON(w, 404, errorAnswer{Error: "not found: " + name}, nil)
		return nil
	}
	writeText(w, 200, string(body), map[string]string{"Content-Type": contentType, "Cache-Control": cache})
	return nil
}

// assetRoute serves one allowlisted artifact from the dashboard build: the part of path past
// prefix must match name, or the route answers the path's 404; the bytes answer with
// contentType under the Cache-Control it is given. The artifact's directory is the prefix
// without its leading slash, which is where the build lays it.
func (s *server) assetRoute(w http.ResponseWriter, path, prefix string, name *regexp.Regexp, contentType, cache string) error {
	file := path[len(prefix):]
	if !name.MatchString(file) {
		s.writeJSON(w, 404, errorAnswer{Error: "not found: " + path}, nil)
		return nil
	}
	body, err := dashboardFile(prefix[1:] + file)
	if err != nil {
		s.writeJSON(w, 404, errorAnswer{Error: "missing build artifact: " + prefix[1:] + file}, nil)
		return nil
	}
	writeText(w, 200, string(body), map[string]string{"Content-Type": contentType, "Cache-Control": cache})
	return nil
}

// fontRoute serves the bundled IBM Plex faces the dashboard's stylesheet points at. The build
// names each face by its content hash, so a face never changes under its name.
func (s *server) fontRoute(w http.ResponseWriter, path string) error {
	return s.assetRoute(w, path, "/fonts/", fontName, "font/woff2", immutable)
}

// swRoute serves the app-shell service worker. The browser revalidates it on every load
// (Cache-Control: no-cache) because a new shell must be seen here first, and
// Service-Worker-Allowed names the root scope the registration asks for.
func (s *server) swRoute(w http.ResponseWriter) error {
	body, err := dashboardFile("sw.js")
	if err != nil {
		s.writeJSON(w, 404, errorAnswer{Error: "missing build artifact: sw.js"}, nil)
		return nil
	}
	writeText(w, 200, string(body), map[string]string{
		"Content-Type":           "text/javascript",
		"Cache-Control":          "no-cache",
		"Service-Worker-Allowed": "/",
	})
	return nil
}

// iconRoute serves the PWA's icons the manifest names.
func (s *server) iconRoute(w http.ResponseWriter, path string) error {
	return s.assetRoute(w, path, "/icons/", iconName, "image/png", shortCache)
}

// createChannel makes a channel with its first configuration.
func (s *server) createChannel(w http.ResponseWriter, r *http.Request) error {
	body, err := s.body(r)
	if err != nil {
		return err
	}
	name := textOf(fieldOf(body, "name"))
	if exists, _ := s.hub.Exists(r.Context(), name); exists {
		return &channel.HuddleError{Status: 409, Msg: "channel " + name + " exists"}
	}
	ch, err := s.hub.Get(r.Context(), name)
	if err != nil {
		return err
	}
	config, err := ch.Configure(r.Context(), channel.ConfigureArgs{
		Title:        strPtrOf(fieldOf(body, "title")),
		Description:  strPtrOf(fieldOf(body, "description")),
		Start:        strPtrOf(fieldOf(body, "start")),
		Handover:     fieldOf(body, "handover"),
		Profile:      strPtrOf(fieldOf(body, "profile")),
		Repo:         strPtrOf(fieldOf(body, "repo")),
		Members:      fieldOf(body, "members"),
		Orchestrator: strPtrOf(fieldOf(body, "orchestrator")),
	})
	if err != nil {
		return err
	}
	s.writeJSON(w, 200, struct {
		Name   string         `json:"name"`
		Config channel.Config `json:"config"`
	}{ch.Name, config}, nil)
	return nil
}

// removeChannel takes a channel off the hub: the creator only, and never one with a session
// online. The file stays under channels/.removed/, so a mistake is undone by moving it back.
func (s *server) removeChannel(w http.ResponseWriter, r *http.Request, name string, me auth.Who) error {
	if !me.Root {
		s.writeJSON(w, 403, errorAnswer{Error: "only the session that started Huddle may do this"}, nil)
		return nil
	}
	ch, err := s.hub.GetExisting(r.Context(), name)
	if err != nil {
		return err
	}
	sessions, err := ch.Sessions(r.Context())
	if err != nil {
		return err
	}
	online := 0
	for _, sess := range sessions {
		// a crashed or kicked session never leaves; past the stale mark it no longer counts
		if sess.State != "left" && !sess.Stale {
			online++
		}
	}
	if online > 0 {
		s.writeJSON(w, 409, errorAnswer{Error: fmt.Sprintf("%s has %d session(s) online", name, online)}, nil)
		return nil
	}
	kept, err := s.hub.Remove(r.Context(), name, time.Now())
	if err != nil {
		return err
	}
	s.writeJSON(w, 200, struct {
		Removed string `json:"removed"`
		Kept    string `json:"kept"`
	}{name, kept}, nil)
	return nil
}

// strPtrOf reads one string member as a pointer (nil when it is not there).
func strPtrOf(v *jsfmt.Value) *string {
	if v == nil || v.Kind() == jsfmt.KindNull {
		return nil
	}
	text := textOf(v)
	return &text
}

// adminRoute answers the routes around membership: invites, members, the login code and whoami.
// answered says the path was one of these.
func (s *server) adminRoute(w http.ResponseWriter, r *http.Request, path string, me auth.Who) (bool, error) {
	rootOnly := func() (bool, error) {
		if me.Root {
			return false, nil
		}
		s.writeJSON(w, 403, errorAnswer{Error: "only the session that started Huddle may do this"}, nil)
		return true, nil
	}
	switch {
	case path == "/api/whoami":
		// the two facts the shell reads beside the identity: whether an authenticator is
		// enrolled (the first-run set-up gate), and whether this session's device row holds a
		// passkey (the biometrics offer after a code sign-in)
		s.writeJSON(w, 200, struct {
			auth.Who
			TOTP          bool `json:"totp"`
			DevicePasskey bool `json:"device_passkey"`
		}{me, s.totp != nil && s.totp.Enrolled(r.Context()), s.deviceHasPasskey(r, me)}, nil)
		return true, nil
	case path == "/api/tokens" && r.Method == http.MethodPost:
		if !me.Invite {
			s.writeJSON(w, 403, errorAnswer{Error: "this credential may not invite"}, nil)
			return true, nil
		}
		body, err := s.body(r)
		if err != nil {
			return true, err
		}
		var ttl *int64
		if v := fieldOf(body, "ttl"); v != nil && v.Kind() != jsfmt.KindNull {
			seconds, ok := numberOf(v)
			if !ok || seconds < 0 {
				return true, &channel.HuddleError{Status: 400, Msg: "ttl: seconds, 0 = never expires"}
			}
			ttl = &seconds
		}
		channelName := ""
		if v := fieldOf(body, "channel"); v != nil && v.Kind() == jsfmt.KindString {
			channelName = v.Text()
		}
		description := ""
		if v := fieldOf(body, "description"); v != nil && v.Kind() == jsfmt.KindString {
			description = jsfmt.UTF16Slice(v.Text(), 0, 200)
		}
		s.writeJSON(w, 200, s.auth.Create(auth.InviteOptions{
			TTL: ttl, Single: truthy(fieldOf(body, "single_use")),
			Invite: truthy(fieldOf(body, "can_invite")) && me.Root, Channel: channelName,
			Description: description, By: me.Name,
		}), nil)
		return true, nil
	case path == "/api/tokens" && r.Method == http.MethodGet:
		if answered, err := rootOnly(); answered || err != nil {
			return answered, err
		}
		s.writeJSON(w, 200, s.auth.List(), nil)
		return true, nil
	}
	if m := tokenPath.FindStringSubmatch(path); m != nil && r.Method == http.MethodDelete {
		if answered, err := rootOnly(); answered || err != nil {
			return answered, err
		}
		if s.auth.Revoke(m[1]) {
			s.writeJSON(w, 200, struct {
				Deleted string `json:"deleted"`
			}{m[1]}, nil)
		} else {
			s.writeJSON(w, 404, errorAnswer{Error: "no token " + m[1]}, nil)
		}
		return true, nil
	}
	if path == "/api/members" && r.Method == http.MethodGet {
		if answered, err := rootOnly(); answered || err != nil {
			return answered, err
		}
		s.writeJSON(w, 200, s.auth.MemberList(), nil)
		return true, nil
	}
	if m := memberPath.FindStringSubmatch(path); m != nil && r.Method == http.MethodDelete {
		if answered, err := rootOnly(); answered || err != nil {
			return answered, err
		}
		if s.auth.Kick(m[1]) {
			s.writeJSON(w, 200, struct {
				Kicked string `json:"kicked"`
			}{m[1]}, nil)
		} else {
			s.writeJSON(w, 404, errorAnswer{Error: "no member " + m[1]}, nil)
		}
		return true, nil
	}
	if path == "/api/login" && r.Method == http.MethodPost {
		if me.Browser != "" {
			s.writeJSON(w, 403, errorAnswer{Error: "a browser does not sign in other browsers: run huddle open (or /huddle:setup) in a session"}, nil)
			return true, nil
		}
		var by *string
		if !me.Root {
			name := me.Name
			by = &name
		}
		s.writeJSON(w, 200, struct {
			Code      string `json:"code"`
			ExpiresIn int64  `json:"expires_in"`
		}{s.auth.LoginCode(by), 300}, nil)
		return true, nil
	}
	return false, nil
}

// numberOf reads one member as JavaScript's Number does, when it is one.
func numberOf(v *jsfmt.Value) (int64, bool) {
	switch v.Kind() {
	case jsfmt.KindNumber:
		f, err := strconv.ParseFloat(v.EncodeJSON(), 64)
		if err != nil {
			return 0, false
		}
		return int64(f), true
	case jsfmt.KindString:
		f, err := strconv.ParseFloat(jsfmt.Trim(v.Text()), 64)
		if err != nil {
			return 0, false
		}
		return int64(f), true
	}
	return 0, false
}

// channelRoute answers /api/c/<channel>/…: the channel's views, its operations and its live
// stream.
func (s *server) channelRoute(w http.ResponseWriter, r *http.Request, ch *channel.Channel, rest string,
	query url.Values) error {
	ctx := r.Context()
	as := actingAs(r)
	if rest == "" {
		stats, err := ch.Stats(ctx)
		if err != nil {
			return err
		}
		turn, err := ch.Turn(ctx)
		if err != nil {
			return err
		}
		s.writeJSON(w, 200, struct {
			Name   string           `json:"name"`
			Config channel.Config   `json:"config"`
			Stats  channel.Stats    `json:"stats"`
			Turn   channel.TurnView `json:"turn"`
			Views  []string         `json:"views"`
		}{ch.Name, ch.Config(), stats, turn, s.views.Names(ch)}, nil)
		return nil
	}
	if m := opPath.FindStringSubmatch(rest); m != nil && r.Method == http.MethodPost {
		if as == "" {
			return &channel.HuddleError{Status: 400, Msg: "say who you are: ?as=<session> or header x-huddle-as"}
		}
		body, err := s.body(r)
		if err != nil {
			return err
		}
		result, err := channel.RunOp(ctx, m[1], ch, as, body)
		if err != nil {
			return err
		}
		if m[1] == "join" {
			if cs := fieldOf(body, "claude_session"); cs != nil && cs.Kind() != jsfmt.KindNull {
				channel.Link(ctx, ch, as, textOf(cs)) // Radar's session ids → Huddle names
			}
		}
		s.overhear(ctx, ch.Name, as, m[1], body)
		s.writeJSON(w, 200, struct {
			Result any     `json:"result"`
			Text   *string `json:"text"`
		}{result.Result, result.Text}, nil)
		return nil
	}
	switch rest {
	case "live":
		last, err := ch.LastSeq(ctx)
		if err != nil {
			return err
		}
		s.live(w, r, ch, as, last)
		return nil
	case "board":
		phases, err := ch.Phases(ctx)
		if err != nil {
			return err
		}
		steps, err := ch.Tasks(ctx, channel.TasksFilter{})
		if err != nil {
			return err
		}
		planAt := (*string)(nil)
		if v := ch.Meta("plan_at"); v != "" {
			planAt = &v
		}
		s.writeJSON(w, 200, struct {
			Phases []channel.PhaseView    `json:"phases"`
			Steps  []channel.TaskListItem `json:"steps"`
			Meta   boardMeta              `json:"meta"`
		}{phases, steps, boardMeta{Config: ch.Config(), FTS: ch.FTS, PlanAt: planAt}}, nil)
		return nil
	case "sessions":
		sessions, err := ch.Sessions(ctx)
		if err != nil {
			return err
		}
		turn, err := ch.Turn(ctx)
		if err != nil {
			return err
		}
		s.writeJSON(w, 200, struct {
			Sessions []channel.SessionView `json:"sessions"`
			Turn     channel.TurnView      `json:"turn"`
			Config   channel.Config        `json:"config"`
		}{sessions, turn, ch.Config()}, nil)
		return nil
	case "timeline":
		after := int64(0)
		if v, ok := queryInt(query, "after"); ok {
			after = v
		}
		limit := int64(300)
		if v, ok := queryInt(query, "limit"); ok {
			limit = v
		}
		var before *int64
		if v, ok := queryInt(query, "before"); ok {
			before = &v
		}
		events, err := ch.Events(ctx, channel.EventsFilter{After: &after, Before: before, Limit: &limit,
			Topic: query.Get("topic")})
		if err != nil {
			return err
		}
		s.writeJSON(w, 200, events, nil)
		return nil
	case "search":
		hits, err := ch.Search(ctx, query.Get("q"))
		if err != nil {
			return err
		}
		s.writeJSON(w, 200, hits, nil)
		return nil
	case "review":
		review, err := ch.Review(ctx)
		if err != nil {
			return err
		}
		s.writeJSON(w, 200, review, nil)
		return nil
	case "attention":
		attention, err := ch.Attention(ctx)
		if err != nil {
			return err
		}
		s.writeJSON(w, 200, attention, nil)
		return nil
	case "kb":
		limit := 50
		if v, ok := queryInt(query, "limit"); ok {
			limit = int(v)
		}
		items, err := channel.Recall(ctx, ch, query.Get("q"), channel.RecallOpts{
			Kind: query.Get("kind"), Limit: limit})
		if err != nil {
			return err
		}
		s.writeJSON(w, 200, items, nil)
		return nil
	case "knowledge.md":
		verified := regexp.MustCompile(`^(1|true)$`).MatchString(query.Get("verified"))
		text, err := channel.ExportMarkdown(ctx, ch, channel.ExportOpts{Verified: verified})
		if err != nil {
			return err
		}
		writeText(w, 200, text, map[string]string{
			"Content-Type": "text/markdown; charset=utf-8", "Cache-Control": "no-store"})
		return nil
	case "conflicts":
		view, err := channel.Conflicts(ctx, ch)
		if err != nil {
			return err
		}
		s.writeJSON(w, 200, view, nil)
		return nil
	case "reservations":
		view, err := channel.Reservations(ctx, ch, "")
		if err != nil {
			return err
		}
		s.writeJSON(w, 200, view, nil)
		return nil
	case "plan.json":
		phases, err := ch.Phases(ctx)
		if err != nil {
			return err
		}
		list, err := ch.Tasks(ctx, channel.TasksFilter{})
		if err != nil {
			return err
		}
		steps := []*channel.TaskView{}
		for _, t := range list {
			full, err := ch.Task(ctx, t.ID)
			if err != nil {
				return err
			}
			if full != nil {
				steps = append(steps, full)
			}
		}
		s.writeJSON(w, 200, struct {
			Meta   channel.Config      `json:"meta"`
			Phases []channel.PhaseView `json:"phases"`
			Steps  []*channel.TaskView `json:"steps"`
		}{ch.Config(), phases, steps}, nil)
		return nil
	case "export.md":
		text, err := s.views.ExportMd(ctx, ch)
		if err != nil {
			return err
		}
		writeText(w, 200, text, map[string]string{"Content-Type": "text/markdown; charset=utf-8"})
		return nil
	case "diagram":
		file := s.views.DiagramFile(ch, query.Get("name"))
		if file == "" {
			writeText(w, 400, "bad", nil)
			return nil
		}
		body, err := os.ReadFile(file)
		if err != nil {
			writeText(w, 400, "bad", nil)
			return nil
		}
		writeText(w, 200, string(body), map[string]string{"Content-Type": "image/png"})
		return nil
	}
	if m := kbPath.FindStringSubmatch(rest); m != nil {
		id, _ := strconv.ParseInt(m[1], 10, 64)
		entry, err := channel.KB(ctx, ch, id)
		if err != nil {
			return err
		}
		s.writeJSON(w, 200, entry, nil)
		return nil
	}
	if m := taskPath.FindStringSubmatch(rest); m != nil {
		task, err := ch.Task(ctx, m[1])
		if err != nil {
			return err
		}
		if task == nil {
			s.writeJSON(w, 404, errorAnswer{Error: "no such task"}, nil)
			return nil
		}
		s.writeJSON(w, 200, task, nil)
		return nil
	}
	if m := repoPath.FindStringSubmatch(rest); m != nil {
		answer, err := s.views.Route(ctx, ch, m[1], query)
		if err != nil {
			return err
		}
		s.writeJSON(w, 200, answer, nil)
		return nil
	}
	if m := xPath.FindStringSubmatch(rest); m != nil {
		body, err := s.body(r)
		if err != nil {
			return err
		}
		answer, ok, err := extras.Route(ctx, ch, m[1], r.Method, query.Get("since"), as, body)
		if err != nil {
			return err
		}
		if ok {
			s.writeJSON(w, 200, answer, nil)
			return nil
		}
	}
	s.writeJSON(w, 404, errorAnswer{Error: "not found"}, nil)
	return nil
}

// boardMeta is a channel's configuration with the two board-only facts folded in.
type boardMeta struct {
	channel.Config
	FTS    bool    `json:"fts"`
	PlanAt *string `json:"plan_at"`
}

// queryInt reads one integer query parameter.
func queryInt(query url.Values, key string) (int64, bool) {
	raw := query.Get(key)
	if raw == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// mcpRoute answers /mcp and /mcp/<channel>: Streamable HTTP, JSON responses, one batch at a time.
func (s *server) mcpRoute(w http.ResponseWriter, r *http.Request, name string) error {
	as := actingAs(r)
	if name == "" || as == "" {
		s.writeJSON(w, 400, errorAnswer{Error: "use /mcp/<channel>?as=<session>"}, nil)
		return nil
	}
	if r.Method == http.MethodGet {
		writeText(w, 405, "this endpoint answers POST; pushes come from bin/huddle-mcp", nil)
		return nil
	}
	if r.Method == http.MethodDelete {
		w.WriteHeader(204)
		return nil
	}
	body, err := s.body(r)
	if err != nil {
		return err
	}
	messages := []*jsfmt.Value{body}
	batch := body.Kind() == jsfmt.KindArray
	if batch {
		messages = body.Items()
	}
	replies := []*jsfmt.Value{}
	for _, message := range messages {
		if reply := s.mcp.Handle(r.Context(), s.hub, name, as, message); reply != nil {
			replies = append(replies, reply)
		}
	}
	if len(replies) == 0 {
		w.WriteHeader(202)
		return nil
	}
	extra := map[string]string{}
	if !batch && textOf(fieldOf(body, "method")) == "initialize" {
		extra["mcp-session-id"] = uuid()
	}
	if batch {
		s.writeJSON(w, 200, replies, extra)
		return nil
	}
	s.writeJSON(w, 200, replies[0], extra)
	return nil
}

// live serves one SSE client: every live message of the channel; with as, the events of everyone
// else (every member sees every message, also those addressed to another session).
func (s *server) live(w http.ResponseWriter, r *http.Request, ch *channel.Channel, as string, last int64) {
	controller := http.NewResponseController(w)
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-store")
	header.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	write := func(frame string) bool {
		if _, err := io.WriteString(w, frame); err != nil {
			return false
		}
		_ = controller.Flush()
		return true
	}

	ctx, cancel := context.WithCancel(r.Context())
	stream := &liveStream{cancel: cancel}
	s.streamsMu.Lock()
	s.streams[stream] = struct{}{}
	s.streamsMu.Unlock()
	defer func() {
		s.streamsMu.Lock()
		delete(s.streams, stream)
		s.streamsMu.Unlock()
		cancel()
	}()

	frames := make(chan string, 256)
	hello, err := domain.MarshalCompactJSON(struct {
		Ch   string `json:"ch"`
		Type string `json:"type"`
		Data struct {
			Last int64 `json:"last"`
		} `json:"data"`
	}{ch.Name, "hello", struct {
		Last int64 `json:"last"`
	}{last}})
	if err != nil || !write("retry: 3000\ndata: "+string(hello)+"\n\n") {
		return
	}
	unsubscribe := ch.Subscribe(func(m channel.Live) {
		if as != "" && as != channel.Owner {
			if m.Type != "event" {
				return
			}
			if ev, ok := m.Data.(channel.Ev); ok && ev.From == as {
				return
			}
		}
		encoded, err := domain.MarshalCompactJSON(m)
		if err != nil {
			return
		}
		select {
		case frames <- "data: " + string(encoded) + "\n\n":
		default: // a client too slow to keep up loses frames, not the stream
		}
	})
	defer unsubscribe()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-frames:
			if !write(frame) {
				return
			}
		case <-heartbeat.C:
			if !s.deviceAlive(r) {
				return // a removed device's stream ends with its session
			}
			if !write(": ping\n\n") {
				return
			}
		}
	}
}
