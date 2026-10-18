// Package phone serves what a phone needs before it can open the hub: the local CA's public
// root for download and a one-page guide with the exact taps, over plain HTTP on the network the
// phone shares with the computer. Plain HTTP is the point — no public CA issues for the hub's
// local name and the phone holds no trust yet, so this is the one path that works at all. The
// helper holds the root's public bytes in memory, never reads the home per request, and answers
// exactly its three paths: nothing under the home, and never a key.
package phone

import (
	"context"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Life is how long one helper serves: the window the QR code stays honest for.
const Life = 10 * time.Minute

// HelperOptions shapes one helper run; every nil member takes the real one.
type HelperOptions struct {
	// Address is the host the helper binds on the network the phone shares; the port is always
	// the OS's choice. "" takes loopback.
	Address string
	// Root is the local CA's public root, served as DER, PEM and the iOS profile.
	Root *x509.Certificate
	// HubURL is the hub's https base the guide's Check proves trust against; "" hides the check.
	HubURL string
	// Now is the clock the deadline reads; nil takes the wall clock.
	Now func() time.Time
	// Sleep waits one stretch and answers whether the run goes on; nil waits for real, in
	// stretches that watch the context and the guide's own end.
	Sleep func(ctx context.Context, d time.Duration) bool
	// Log says one line when the guide ends on its deadline.
	Log func(line string)
}

// Helper is the guide one run serves on one address: it stops on its deadline, on its context,
// or on Close — whichever comes first.
type Helper struct {
	options  HelperOptions
	host     string
	listener net.Listener
	server   *http.Server
	deadline time.Time
	done     chan struct{}
	stopOnce sync.Once
}

// Start binds the address with a port of the OS's choice and serves the guide until its life
// ends, ctx ends, or Close runs. The root is held in memory from here on; the home is never
// read again.
func Start(ctx context.Context, o HelperOptions) (*Helper, error) {
	host := o.Address
	if host == "" {
		host = "127.0.0.1"
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return nil, err
	}
	h := &Helper{
		options:  o,
		host:     host,
		listener: listener,
		deadline: hclock(o.Now)().Add(Life),
		done:     make(chan struct{}),
	}
	h.server = &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       5 * time.Second,
	}
	go func() { _ = h.server.Serve(listener) }()
	go h.watch(ctx)
	return h, nil
}

// URL is the address the QR code carries.
func (h *Helper) URL() string {
	_, port, err := net.SplitHostPort(h.listener.Addr().String())
	if err != nil {
		return "http://" + h.host + "/"
	}
	return "http://" + net.JoinHostPort(h.host, port) + "/"
}

// Fingerprint is the SHA-256 of the root the helper serves, as the guide shows it.
func (h *Helper) Fingerprint() string { return Fingerprint(h.options.Root) }

// Live answers whether the guide still serves.
func (h *Helper) Live() bool {
	select {
	case <-h.done:
		return false
	default:
		return true
	}
}

// Done closes once the guide has ended, however it ended.
func (h *Helper) Done() <-chan struct{} { return h.done }

// Close ends the guide at once; a second Close changes nothing.
func (h *Helper) Close() { h.stop(false) }

// ServeHTTP answers the three paths the guide owns and refuses every other one.
func (h *Helper) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	securityHeaders(w, h.options.HubURL)
	switch r.URL.Path {
	case "/", "/index.html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(Page(PageOptions{Root: h.options.Root, HubURL: h.options.HubURL}))
	case "/ca.crt":
		WriteCA(w, r, h.options.Root, false)
	case "/ca.mobileconfig":
		WriteCA(w, r, h.options.Root, true)
	default:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	}
}

// watch ends the guide when its life is over: the whole wait runs through one sleep the caller
// may replace, so a movable clock proves the deadline without waiting it out.
func (h *Helper) watch(ctx context.Context) {
	sleep := h.options.Sleep
	if sleep == nil {
		sleep = h.sleepStretches
	}
	if wait := h.deadline.Sub(hclock(h.options.Now)()); wait > 0 && !sleep(ctx, wait) {
		h.stop(false)
		return
	}
	h.stop(true)
}

// stop ends the guide once; onDeadline says whether the line names the deadline's own end.
func (h *Helper) stop(onDeadline bool) {
	h.stopOnce.Do(func() {
		close(h.done)
		_ = h.server.Close()
		if onDeadline && h.options.Log != nil {
			h.options.Log("the phone guide ended after its ten minutes")
		}
	})
}

// sleepStretches waits d in stretches of a second that each watch the context and the guide's
// own end, so a stop is never held hostage by one long wait.
func (h *Helper) sleepStretches(ctx context.Context, d time.Duration) bool {
	end := time.Now().Add(d)
	for {
		left := time.Until(end)
		if left <= 0 {
			return true
		}
		if left > time.Second {
			left = time.Second
		}
		timer := time.NewTimer(left)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-h.done:
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}

// hclock answers the options' clock, or the wall clock.
func hclock(now func() time.Time) func() time.Time {
	if now != nil {
		return now
	}
	return time.Now
}

// LANAddress answers this machine's address on the network a phone shares with it: the first
// non-loopback IPv4 the interfaces hold, "" when none does.
func LANAddress() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		if v4 := ipNet.IP.To4(); v4 != nil {
			return v4.String()
		}
	}
	return ""
}

// BindHost answers the host a phone reaches this machine on: the hub listener's own address when
// the config names a concrete one, else the LAN walk's answer, else loopback — a machine with no
// outside interface still gets a helper, if only for a browser on itself.
func BindHost(listen string, lan func() string) string {
	if host, _, err := net.SplitHostPort(listen); err == nil {
		if ip := net.ParseIP(host); ip != nil && !ip.IsUnspecified() {
			return host
		}
	}
	if lan == nil {
		lan = LANAddress
	}
	if address := lan(); address != "" {
		return address
	}
	return "127.0.0.1"
}

// HubURL answers the hub's public https base: the name alone when the hub listener serves 443,
// the port appended when it serves anything else — a link without the port opens nothing.
func HubURL(host, listen string) string {
	if _, port, err := net.SplitHostPort(listen); err == nil {
		if n, err := strconv.Atoi(port); err == nil && n > 0 && n < 65536 && n != 443 {
			return fmt.Sprintf("https://%s:%d", host, n)
		}
	}
	return "https://" + host
}
