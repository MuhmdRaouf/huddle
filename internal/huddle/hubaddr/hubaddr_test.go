package hubaddr_test

// The hub address lookup kanban and radar share: the huddle home's port file
// names the port, the health probe says whether a hub really answers it.

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hubaddr"
)

// hubEnv is one environment whose huddle home is root.
func hubEnv(root string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		if key == "HUDDLE_HOME" {
			return root, true
		}
		return "", false
	}
}

// writePort puts one port in the home's run directory, the way a running
// server leaves it.
func writePort(t *testing.T, root, port string) {
	t.Helper()
	if err := os.MkdirAll(home.Home{Root: root}.Run(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(home.Home{Root: root}.PortFile(), []byte(port), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBase(t *testing.T) {
	t.Run("no port file is no base", func(t *testing.T) {
		if got := hubaddr.Base(hubEnv(t.TempDir())); got != "" {
			t.Errorf("Base = %q, want empty", got)
		}
	})

	t.Run("a garbage port file is no base", func(t *testing.T) {
		root := t.TempDir()
		writePort(t, root, "not a port")
		if got := hubaddr.Base(hubEnv(root)); got != "" {
			t.Errorf("Base = %q, want empty", got)
		}
	})

	t.Run("a written port answers its loopback base url", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(server.Close)
		root := t.TempDir()
		_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
		writePort(t, root, port)
		if got, want := hubaddr.Base(hubEnv(root)), server.URL; got != want {
			t.Errorf("Base = %q, want %q", got, want)
		}
	})
}

func TestHealthy(t *testing.T) {
	ctx := context.Background()

	t.Run("a hub answering its health path is healthy", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/health" {
				t.Errorf("the probe hit %s, want /health", r.URL.Path)
			}
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(server.Close)
		if !hubaddr.Healthy(ctx, server.URL) {
			t.Error("a hub answering /health with 200 counts as unhealthy")
		}
	})

	t.Run("a closed port is not healthy", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("draw a free port: %v", err)
		}
		_, port, _ := net.SplitHostPort(listener.Addr().String())
		base := "http://127.0.0.1:" + port
		_ = listener.Close()
		if hubaddr.Healthy(ctx, base) {
			t.Errorf("%s answers with nothing listening, want unhealthy", base)
		}
	})
}
