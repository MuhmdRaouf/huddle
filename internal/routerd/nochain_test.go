package routerd

// CleanProxyEnv keeps the no-chain rule: the engine environment carries no proxy variable that
// names a router — one on the known ports, this router on its own port, or any loopback proxy
// that answers a router health path. Every other variable survives.

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// countingClient answers a client whose transport counts every dial it makes and never goes
// through a proxy, so a test can prove no probe was sent at all.
func countingClient() (*http.Client, func() int) {
	mu := sync.Mutex{}
	dials := 0
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			mu.Lock()
			dials++
			mu.Unlock()
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	return &http.Client{Transport: transport}, func() int {
		mu.Lock()
		defer mu.Unlock()
		return dials
	}
}

// healthServer starts one fake router answering the given health paths with 200 and every
// other path with 404, counting what it was asked.
func healthServer(t *testing.T, ok map[string]bool) (url string, hits func() int) {
	t.Helper()
	mu := sync.Mutex{}
	asked := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked++
		mu.Unlock()
		if ok[r.URL.Path] {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true,"name":"huddle-router"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	return server.URL, func() int {
		mu.Lock()
		defer mu.Unlock()
		return asked
	}
}

// withProxy layers proxy variables over a base environment.
func withProxy(pairs ...string) domain.EnvLookup {
	env := domain.EnvLookup{"HOME": "/h", "ZAI_API_KEY": "k"}
	for i := 0; i+1 < len(pairs); i += 2 {
		env[pairs[i]] = pairs[i+1]
	}
	return env
}

func TestCleanProxyEnv(t *testing.T) {
	t.Run("a loopback proxy answering the huddle-router health path is dropped", func(t *testing.T) {
		url, _ := healthServer(t, map[string]bool{"/huddle-router/health": true})
		lines := []string{}
		cleaned := CleanProxyEnv(context.Background(), withProxy("HTTPS_PROXY", url), 20999, func(line string) {
			lines = append(lines, line)
		})
		if _, present := cleaned["HTTPS_PROXY"]; present {
			t.Errorf("HTTPS_PROXY = %q, want it dropped", cleaned["HTTPS_PROXY"])
		}
		if cleaned["HOME"] != "/h" || cleaned["ZAI_API_KEY"] != "k" {
			t.Errorf("cleaned = %v, want every other variable kept", cleaned)
		}
		if len(lines) != 1 || !strings.Contains(lines[0], "is a router; going direct") {
			t.Errorf("log = %v, want one going-direct line", lines)
		}
	})

	t.Run("a loopback proxy answering only a provider's health path is dropped", func(t *testing.T) {
		url, _ := healthServer(t, map[string]bool{"/zai-router/health": true})
		cleaned := CleanProxyEnv(context.Background(), withProxy("https_proxy", url), 20999, nil)
		if _, present := cleaned["https_proxy"]; present {
			t.Error("https_proxy survived a zai-router proxy")
		}
	})

	t.Run("a loopback proxy that answers no health path is kept", func(t *testing.T) {
		url, hits := healthServer(t, map[string]bool{})
		cleaned := CleanProxyEnv(context.Background(), withProxy("HTTPS_PROXY", url), 20999, nil)
		if cleaned["HTTPS_PROXY"] != url {
			t.Errorf("HTTPS_PROXY = %q, want %q kept", cleaned["HTTPS_PROXY"], url)
		}
		if hits() == 0 {
			t.Error("the proxy was never asked for a health answer")
		}
	})

	t.Run("a non-loopback proxy is kept and never contacted", func(t *testing.T) {
		client, dials := countingClient()
		cleaned := cleanProxyEnv(context.Background(), withProxy("HTTPS_PROXY", "http://proxy.invalid:3128"), 20999, nil, client)
		if cleaned["HTTPS_PROXY"] != "http://proxy.invalid:3128" {
			t.Errorf("HTTPS_PROXY = %q, want it kept", cleaned["HTTPS_PROXY"])
		}
		if dials() != 0 {
			t.Error("the non-loopback proxy was dialled")
		}
	})

	t.Run("a proxy on the router's own port is dropped without a request", func(t *testing.T) {
		client, dials := countingClient()
		cleaned := cleanProxyEnv(context.Background(), withProxy("HTTPS_PROXY", "http://127.0.0.1:20999", "HTTP_PROXY", "127.0.0.1:20999"),
			20999, nil, client)
		if _, present := cleaned["HTTPS_PROXY"]; present {
			t.Error("HTTPS_PROXY survived the router's own port")
		}
		if _, present := cleaned["HTTP_PROXY"]; present {
			t.Error("HTTP_PROXY survived the router's own port")
		}
		if dials() != 0 {
			t.Error("the router's own port was dialled to recognise itself")
		}
	})
}
