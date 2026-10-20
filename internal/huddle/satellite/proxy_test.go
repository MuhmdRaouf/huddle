package satellite

// The link's own transport against a hub on a local name: a hub on this machine is reached
// direct even though the environment points every request at a proxy, and a hub on another
// machine still goes through it.

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/netproxy"
)

// TestMain pins one unreachable proxy for the whole test binary before any test runs:
// http.ProxyFromEnvironment reads the environment once per process, and the tests below need it
// set from the first request on. No other test in the package changes: a loopback address skips
// a proxy on its own, and every fake hub here sits on 127.0.0.1.
func TestMain(m *testing.M) {
	_ = os.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	_ = os.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	os.Exit(m.Run())
}

// nameResolver answers the names its table holds, the way the OS resolver answers a hub's name
// on this machine; a name it does not hold resolves nowhere.
type nameResolver map[string][]net.IPAddr

func (r nameResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return r[host], nil
}

// hubTransport builds the transport the link builds for its board reads, over a resolver the
// test controls instead of the OS's.
func hubTransport(resolver netproxy.Resolver) *http.Transport {
	client := readClientOf(nil)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		panic("the link's own client carries no *http.Transport")
	}
	transport.Proxy = netproxy.New(resolver).FromEnvironment
	return transport
}

// loopbackDialer dials every address on loopback, keeping the port: the resolver's answer names
// this machine, so the name's route is pinned here.
func loopbackDialer(ctx context.Context, network, addr string) (net.Conn, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
}

func startPlainHub(t *testing.T) (port string, hits func() int) {
	t.Helper()
	count := 0
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		_, _ = io.WriteString(w, "hub")
	}))
	t.Cleanup(hub.Close)
	_, port, err := net.SplitHostPort(hub.Listener.Addr().String())
	if err != nil {
		t.Fatalf("read the fake hub's port: %v", err)
	}
	return port, func() int { return count }
}

func TestTheLinkTransportReachesALocalHubDirect(t *testing.T) {
	port, hits := startPlainHub(t)
	transport := hubTransport(nameResolver{
		"hub.huddle.test": {net.IPAddr{IP: net.ParseIP("127.0.0.1")}},
	})
	transport.DialContext = loopbackDialer

	request, err := http.NewRequest(http.MethodGet, "http://hub.huddle.test:"+port+"/", nil)
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		t.Fatalf("the hub on a local name was not reached direct: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read the hub's answer: %v", err)
	}
	if string(body) != "hub" || hits() != 1 {
		t.Fatalf("the hub answered %q with %d hits, want \"hub\" and one hit", body, hits())
	}
}

func TestTheLinkTransportKeepsTheProxyForARemoteHub(t *testing.T) {
	port, _ := startPlainHub(t)
	transport := hubTransport(nameResolver{
		"hub.other.test": {net.IPAddr{IP: net.ParseIP("93.184.216.34")}},
	})
	transport.DialContext = loopbackDialer

	request, err := http.NewRequest(http.MethodGet, "http://hub.other.test:"+port+"/", nil)
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	if _, err := (&http.Client{Transport: transport}).Do(request); err == nil {
		t.Fatalf("the hub on another machine skipped the environment's proxy")
	}
}

func TestTheLinkTransportSendsLoopbackDirect(t *testing.T) {
	transport, ok := readClientOf(nil).Transport.(*http.Transport)
	if !ok || transport.Proxy == nil {
		t.Fatalf("the link's own transport carries no proxy function")
	}
	request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:9443/", nil)
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	proxy, err := transport.Proxy(request)
	if err != nil {
		t.Fatalf("the proxy for loopback: %v", err)
	}
	if proxy != nil {
		t.Fatalf("the proxy for loopback is %s, want direct", proxy)
	}
}
