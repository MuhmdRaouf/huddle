// The environment a Huddle command runs in: its variables, working directory, clock, process
// lookups and the binary that runs the server. The zero value is the process's own, so each
// command reads what the plugin's launcher gave it, and the tests give each one a throwaway.
package cli

import (
	"context"
	"crypto/x509"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/outside"
	"github.com/MuhmdRaouf/huddle/internal/huddle/phone"
	"github.com/MuhmdRaouf/huddle/internal/pki"
)

// World is where one command runs. A nil World is the process's own.
type World struct {
	// Getenv reads one variable; nil reads the process's.
	Getenv func(string) string
	// Environ lists the variables a child process inherits; nil is the process's own.
	Environ func() []string
	// Dir is the working directory; "" is the process's.
	Dir string
	// Now is the clock; nil is the wall clock.
	Now func() time.Time
	// Proc holds the process lookups; the zero value reads the running machine.
	Proc Proc
	// Bin is the huddle binary a detached server runs; "" is the running executable.
	Bin string
	// Stdin is where the words that ask read; nil is the process's.
	Stdin io.Reader
	// Secrets resolves the home's secret store; nil takes the OS keystore with the home's
	// keys/ files behind it. Tests answer from a fake so no real keystore is ever read.
	Secrets func(ctx context.Context, h home.Home) home.SecretStore
	// DNSKnown answers whether a DNS provider name is one the certificate issuer knows; nil
	// takes the issuer's own list.
	DNSKnown func(name string) bool
	// CertIssue answers the issuer for a DNS provider, its token and the path the ACME account
	// key is kept at; nil takes the real one, which talks to Let's Encrypt.
	CertIssue func(provider, token, accountKey string) (home.ACME, error)
	// TrustStore answers the store the trust words reach; nil takes the machine's real stores
	// (the OS's and Firefox's, through smallstep/truststore). Tests answer from a fake so no
	// real trust store is ever touched.
	TrustStore func() pki.Store
	// SystemPool answers the certificate pool a root's trust is checked against; nil takes the
	// system pool — a read, never a write.
	SystemPool func() *x509.CertPool
	// LanAddress answers this machine's address on the network a phone shares with it; nil
	// walks the interfaces. Tests answer loopback so no test binds a real interface.
	LanAddress func() string
	// FileStat answers one file's stat the way the platform reports it; nil stats the
	// filesystem. The owner key check reads the file's owner from it, and tests fake a foreign
	// owner — no test may chown a file to another user.
	FileStat func(path string) (os.FileInfo, error)
	// ProbeClient sends the one request that leaves this machine on purpose, `huddle domain
	// --check`; nil is a client that ignores HTTPS_PROXY and HTTP_PROXY, so the probe reaches the
	// name the way the system resolver answers it. Tests point it at a fake proxy.
	ProbeClient *http.Client
}

// stdin is the world's input.
func (w *World) stdin() io.Reader {
	if w != nil && w.Stdin != nil {
		return w.Stdin
	}
	return os.Stdin
}

// secrets resolves the home's secret store.
func (w *World) secrets(ctx context.Context, h home.Home) home.SecretStore {
	if w != nil && w.Secrets != nil {
		return w.Secrets(ctx, h)
	}
	return outside.SecretsFor(ctx, h)
}

// dnsKnown answers whether the issuer knows the DNS provider name.
func (w *World) dnsKnown(name string) bool {
	if w != nil && w.DNSKnown != nil {
		return w.DNSKnown(name)
	}
	return outside.DNSProviderKnown(name)
}

// certIssue answers the issuer for a provider, its token and the account key's path.
func (w *World) certIssue(provider, token, accountKey string) (home.ACME, error) {
	if w != nil && w.CertIssue != nil {
		return w.CertIssue(provider, token, accountKey)
	}
	return outside.NewACME(provider, token, accountKey)
}

// trustStore answers the store the trust words reach.
func (w *World) trustStore() pki.Store {
	if w != nil && w.TrustStore != nil {
		return w.TrustStore()
	}
	return pki.SystemStore{}
}

// systemPool answers the pool a root's trust is checked against.
func (w *World) systemPool() *x509.CertPool {
	if w != nil && w.SystemPool != nil {
		return w.SystemPool()
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		return nil
	}
	return pool
}

// probeClient answers the client the domain check sends its request with.
func (w *World) probeClient() *http.Client {
	if w != nil && w.ProbeClient != nil {
		return w.ProbeClient
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{Transport: transport}
}

// lanAddress answers the machine's address on the network a phone shares with it.
func (w *World) lanAddress() string {
	if w != nil && w.LanAddress != nil {
		return w.LanAddress()
	}
	return phone.LANAddress()
}

// getenv reads one variable of the world.
func (w *World) getenv(key string) string {
	if w == nil || w.Getenv == nil {
		return os.Getenv(key)
	}
	return w.Getenv(key)
}

// environ lists the world's variables for a child process.
func (w *World) environ() []string {
	if w == nil || w.Environ == nil {
		return os.Environ()
	}
	return w.Environ()
}

// fileStat stats one file; nil on the world answers the filesystem's own.
func (w *World) fileStat(path string) (os.FileInfo, error) {
	if w != nil && w.FileStat != nil {
		return w.FileStat(path)
	}
	return os.Stat(path)
}

// dir is the world's working directory.
func (w *World) dir() string {
	if w != nil && w.Dir != "" {
		return w.Dir
	}
	if dir, err := os.Getwd(); err == nil {
		return dir
	}
	return "."
}

// now is the world's clock.
func (w *World) now() time.Time {
	if w != nil && w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// bin is the binary a detached server runs.
func (w *World) bin() string {
	if w != nil && w.Bin != "" {
		return w.Bin
	}
	bin, err := os.Executable()
	if err != nil {
		return "huddle"
	}
	return bin
}

// WithVar answers a world whose Getenv says value for key and leaves the rest alone: a --port
// flag overrides a variable for one call.
func (w *World) WithVar(key, value string) *World {
	base := w.getenv
	return &World{
		Getenv: func(k string) string {
			if k == key {
				return value
			}
			return base(k)
		},
		Environ:    w.environ,
		Dir:        w.Dir,
		Now:        w.Now,
		Proc:       w.Proc,
		Bin:        w.Bin,
		Stdin:      w.Stdin,
		Secrets:    w.Secrets,
		DNSKnown:   w.DNSKnown,
		CertIssue:  w.CertIssue,
		TrustStore: w.TrustStore,
		SystemPool: w.SystemPool,
		LanAddress: w.LanAddress,
		FileStat:   w.FileStat,
	}
}

// Proc is the two process lookups identity needs: whether a pid is this binary running the
// server, and which loopback port that process listens on. The zero value reads the running
// machine (the platform halves say how); the tests answer from fakes so no real session is ever
// read.
type Proc struct {
	// Command answers a pid's full command line, as `ps -o command=` prints it.
	Command func(pid int) (string, bool)
	// ListenOf answers the loopback port a pid listens on, as lsof reports it.
	ListenOf func(pid int) (int, bool)
}

// command looks a pid's command line up.
func (p Proc) command(pid int) (string, bool) {
	if p.Command != nil {
		return p.Command(pid)
	}
	return procCommand(pid)
}

// listenOf looks the port a pid listens on up.
func (p Proc) listenOf(pid int) (int, bool) {
	if p.ListenOf != nil {
		return p.ListenOf(pid)
	}
	return procListenOf(pid)
}

// huddleServe matches the command line of this binary running the serve entry: a stale pid file
// may name any process, so only a huddle server counts. The trailing newline is ps's own.
var huddleServe = regexp.MustCompile(`(?:^|/)huddle serve(?:[ \n]|$)`)

// isHuddleServe says whether a command line is a huddle binary running the serve entry: by its
// plain name (`/opt/huddle serve`) or through a symlink that resolves to one — the launcher's
// `current`, whose hub the pid file is still ours to stop. A link that no longer resolves
// names no server, and no other entry through a link does.
func isHuddleServe(command string) bool {
	command = strings.TrimSpace(command)
	if huddleServe.MatchString(command) {
		return true
	}
	bin, args, ok := strings.Cut(command, " ")
	if !ok || args != "serve" && !strings.HasPrefix(args, "serve ") {
		return false
	}
	resolved, err := filepath.EvalSymlinks(bin)
	return err == nil && filepath.Base(resolved) == "huddle"
}

// proc is the world's process lookups.
func (w *World) proc() Proc {
	if w == nil {
		return Proc{}
	}
	return w.Proc
}

// trimSlash strips one trailing slash, the way the url settings are read.
func trimSlash(u string) string { return strings.TrimSuffix(u, "/") }
