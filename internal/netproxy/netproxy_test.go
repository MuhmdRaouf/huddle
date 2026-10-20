package netproxy

// The decider against a stub resolver: a host on this machine — a literal loopback, unspecified
// or link-local address, or a name that resolves to one — goes direct, everything else keeps the
// environment's proxy, and a failed lookup changes nothing. Every test pins one proxy, so a
// nil, nil answer is the only way a request skips it.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
)

// proxyAt is one proxy no test ever dials; the assertions read the answer, never the hop.
const proxyAt = "http://proxy.example.test:9"

// stubResolver answers the names its map holds, counting the lookups it took.
type stubResolver struct {
	addrs   map[string][]net.IPAddr
	err     error
	lookups int
}

func (s *stubResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	s.lookups++
	if s.err != nil {
		return nil, s.err
	}
	return s.addrs[host], nil
}

// behindProxy pins the proxy environment the tests decide under.
func behindProxy(t *testing.T) {
	t.Helper()
	t.Setenv("HTTPS_PROXY", proxyAt)
	t.Setenv("NO_PROXY", "")
}

// requestOf builds one HTTPS request to the host on the hub port.
func requestOf(t *testing.T, host string) *http.Request {
	t.Helper()
	url := "https://" + host + ":9443/"
	if strings.Contains(host, ":") {
		url = "https://[" + host + "]:9443/"
	}
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build the request to %s: %v", host, err)
	}
	return request
}

func TestFromEnvironmentSendsThisMachineDirect(t *testing.T) {
	behindProxy(t)
	resolver := &stubResolver{}
	decider := New(resolver)
	for _, host := range []string{"127.0.0.1", "127.0.0.2", "::1", "0.0.0.0", "fe80::1"} {
		proxy, err := decider.FromEnvironment(requestOf(t, host))
		if err != nil {
			t.Fatalf("the proxy for %s: %v", host, err)
		}
		if proxy != nil {
			t.Fatalf("the proxy for %s is %s, want direct", host, proxy)
		}
	}
	if resolver.lookups != 0 {
		t.Fatalf("the literal addresses cost %d lookups, want none", resolver.lookups)
	}
}

func TestFromEnvironmentSendsANameThatResolvesLocalDirect(t *testing.T) {
	behindProxy(t)
	resolver := &stubResolver{addrs: map[string][]net.IPAddr{
		"hub.raouf.test": {net.IPAddr{IP: net.ParseIP("127.0.0.2")}},
	}}
	proxy, err := New(resolver).FromEnvironment(requestOf(t, "hub.raouf.test"))
	if err != nil {
		t.Fatalf("the proxy for the local name: %v", err)
	}
	if proxy != nil {
		t.Fatalf("the proxy for the local name is %s, want direct", proxy)
	}
}

func TestFromEnvironmentKeepsTheProxyForARemoteName(t *testing.T) {
	behindProxy(t)
	resolver := &stubResolver{addrs: map[string][]net.IPAddr{
		"www.example.test": {net.IPAddr{IP: net.ParseIP("93.184.216.34")}},
	}}
	proxy, err := New(resolver).FromEnvironment(requestOf(t, "www.example.test"))
	if err != nil {
		t.Fatalf("the proxy for the remote name: %v", err)
	}
	if proxy == nil || proxy.String() != proxyAt {
		t.Fatalf("the proxy for the remote name is %v, want %s", proxy, proxyAt)
	}
}

func TestFromEnvironmentKeepsTheProxyWhenTheLookupFails(t *testing.T) {
	behindProxy(t)
	resolver := &stubResolver{err: errors.New("no resolver here")}
	proxy, err := New(resolver).FromEnvironment(requestOf(t, "hub.raouf.test"))
	if err != nil {
		t.Fatalf("the proxy after a failed lookup: %v", err)
	}
	if proxy == nil || proxy.String() != proxyAt {
		t.Fatalf("the proxy after a failed lookup is %v, want %s", proxy, proxyAt)
	}
}

func TestFromEnvironmentKeepsTheProxyForAPrivateAddress(t *testing.T) {
	behindProxy(t)
	resolver := &stubResolver{addrs: map[string][]net.IPAddr{
		"box.lan": {net.IPAddr{IP: net.ParseIP("100.64.0.1")}},
	}}
	decider := New(resolver)
	for _, host := range []string{"10.1.2.3", "box.lan"} {
		proxy, err := decider.FromEnvironment(requestOf(t, host))
		if err != nil {
			t.Fatalf("the proxy for %s: %v", host, err)
		}
		if proxy == nil || proxy.String() != proxyAt {
			t.Fatalf("the proxy for %s is %v, want %s", host, proxy, proxyAt)
		}
	}
}

func TestFromEnvironmentAnswersFromTheCache(t *testing.T) {
	behindProxy(t)
	resolver := &stubResolver{addrs: map[string][]net.IPAddr{
		"hub.raouf.test": {net.IPAddr{IP: net.ParseIP("127.0.0.2")}},
	}}
	decider := New(resolver)
	for i := 0; i < 2; i++ {
		proxy, err := decider.FromEnvironment(requestOf(t, "hub.raouf.test"))
		if err != nil {
			t.Fatalf("the proxy on call %d: %v", i+1, err)
		}
		if proxy != nil {
			t.Fatalf("the proxy on call %d is %s, want direct", i+1, proxy)
		}
	}
	if resolver.lookups != 1 {
		t.Fatalf("two calls cost %d lookups, want one", resolver.lookups)
	}
}
