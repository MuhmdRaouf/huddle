// Package netproxy answers the proxy a huddle HTTP request goes through. Inside a Claude Code
// session whose provider plugin runs in proxy mode, HTTPS_PROXY points at a localhost router
// that refuses tunnels to this machine, so a request whose host is this machine goes direct and
// every other request keeps honouring the environment's proxy.
package netproxy

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// The lookup budget bounds one name lookup, so a slow resolver costs a request a quarter second
// at most, and the cache holds one name's answer for a minute, so a busy link does not resolve
// its hub on every request.
const (
	lookupBudget = 250 * time.Millisecond
	cacheTTL     = 60 * time.Second
)

// Resolver is the name lookup a decider takes; net.DefaultResolver satisfies it.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// FromEnvironment is the proxy function huddle's HTTP transports use: a request whose host is
// this machine goes direct, every other request honours the environment's proxy.
var FromEnvironment = New(net.DefaultResolver).FromEnvironment

// Decider answers one request's proxy over one resolver, holding each name's answer for a
// minute.
type Decider struct {
	resolver Resolver
	now      func() time.Time

	mu    sync.Mutex
	cache map[string]cacheEntry
}

// cacheEntry is one name's answer: whether it is this machine, and when it goes stale.
type cacheEntry struct {
	local   bool
	expires time.Time
}

// New answers a decider over the given resolver.
func New(resolver Resolver) *Decider {
	return &Decider{resolver: resolver, now: time.Now, cache: make(map[string]cacheEntry)}
}

// FromEnvironment answers the proxy the request goes through: nil, nil sends it direct.
func (d *Decider) FromEnvironment(req *http.Request) (*url.URL, error) {
	if d.isLocal(req.Context(), req.URL.Hostname()) {
		return nil, nil
	}
	return http.ProxyFromEnvironment(req)
}

// isLocal answers whether the host is this machine: a literal loopback, unspecified or
// link-local address, or a name that resolves to one. A failed or timed-out lookup answers
// false — an unknown name is not known local, and the request keeps its ordinary path.
func (d *Decider) isLocal(ctx context.Context, host string) bool {
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return isLocalIP(ip)
	}
	name := strings.ToLower(host)
	if local, ok := d.cached(name); ok {
		return local
	}
	lookupCtx, cancel := context.WithTimeout(ctx, lookupBudget)
	defer cancel()
	addrs, err := d.resolver.LookupIPAddr(lookupCtx, name)
	if err != nil || len(addrs) == 0 {
		return false
	}
	local := false
	for _, addr := range addrs {
		if isLocalIP(addr.IP) {
			local = true
			break
		}
	}
	d.remember(name, local)
	return local
}

// isLocalIP answers whether the address is this machine's own loopback, the unspecified address
// or a link-local one. Private ranges are not local: a hub on a VPN or tailnet keeps the proxy.
func isLocalIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast()
}

// cached answers the name's cached answer while one stands.
func (d *Decider) cached(name string) (local, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	entry, held := d.cache[name]
	if !held || !d.now().Before(entry.expires) {
		return false, false
	}
	return entry.local, true
}

// remember holds the name's answer for the cache's minute.
func (d *Decider) remember(name string, local bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cache[name] = cacheEntry{local: local, expires: d.now().Add(cacheTTL)}
}
