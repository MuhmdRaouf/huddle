package gateway

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// The words: the router daemon serves the gateway under /gateway, so every
// word only reports where — start, the bare word and status answer the router's
// health probe, and stop explains that the gateway ends with the router. The
// health probe is the real one against a loopback server the test started; no
// row starts a process or writes a file.

// routerHealth is one fake router: it answers the health path the words probe.
func routerHealth(t *testing.T, up bool) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"name":"huddle-router","providers":[]}`))
	}))
	t.Cleanup(server.Close)
	return server
}

// portOfServer is one loopback server's port.
func portOfServer(t *testing.T, server *httptest.Server) string {
	t.Helper()
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatalf("read the port of %s: %v", server.URL, err)
	}
	return portText
}

func TestRunReportsTheRouter(t *testing.T) {
	for _, argv := range [][]string{nil, {"start"}, {"status"}} {
		t.Run("the router answers: "+strings.Join(argv, " ")+" names the /gateway address and exits 0", func(t *testing.T) {
			up := routerHealth(t, true)
			env := domain.EnvLookup{"HUDDLE_ROUTER_PORT": portOfServer(t, up)}
			var stdout, stderr bytes.Buffer
			if code := Run(argv, &stdout, &stderr, RunOptions{Env: env}); code != 0 {
				t.Fatalf("Run(%v) = %d, stderr %q", argv, code, stderr.String())
			}
			want := "gateway: served by huddle-router at http://127.0.0.1:" + portOfServer(t, up) + "/gateway\n"
			if stdout.String() != want {
				t.Errorf("stdout = %q, want %q", stdout.String(), want)
			}
			if stderr.String() != "" {
				t.Errorf("stderr = %q, want silence", stderr.String())
			}
		})

		t.Run("the router is down: "+strings.Join(argv, " ")+" is exit 6 with the way out", func(t *testing.T) {
			env := domain.EnvLookup{"HUDDLE_ROUTER_PORT": "1"} // a port nothing answers on
			var stdout, stderr bytes.Buffer
			if code := Run(argv, &stdout, &stderr, RunOptions{Env: env}); code != 6 {
				t.Fatalf("Run(%v) = %d, want 6", argv, code)
			}
			if stdout.String() != "" {
				t.Errorf("stdout = %q, want silence", stdout.String())
			}
			want := "gateway: huddle-router is not running; huddle router start starts it\n"
			if stderr.String() != want {
				t.Errorf("stderr = %q, want %q", stderr.String(), want)
			}
		})
	}
}

func TestRunStopIsOnlyWords(t *testing.T) {
	up := routerHealth(t, true)
	env := domain.EnvLookup{"HUDDLE_ROUTER_PORT": portOfServer(t, up)}
	asked := 0
	var stdout, stderr bytes.Buffer
	code := Run([]string{"stop"}, &stdout, &stderr, RunOptions{
		Env:         env,
		FetchHealth: func(string) bool { asked++; return true },
	})
	if code != 0 {
		t.Fatalf("stop = %d, want 0", code)
	}
	if want := "gateway: served by huddle-router; it stops with the router\n"; stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if asked != 0 {
		t.Errorf("stop probed the router %d times, want none: it stops nothing", asked)
	}
}

func TestRunUsageAndHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"bogus"}, &stdout, &stderr, RunOptions{Env: domain.EnvLookup{"HUDDLE_ROUTER_PORT": "1"}}); code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), usage) {
		t.Errorf("stderr = %q, want the usage line", stderr.String())
	}
	stdout, stderr = bytes.Buffer{}, bytes.Buffer{}
	if code := Run([]string{"help"}, &stdout, &stderr, RunOptions{Env: domain.EnvLookup{"HUDDLE_ROUTER_PORT": "1"}}); code != 0 {
		t.Errorf("help exit = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "/gateway") {
		t.Errorf("help stdout = %q, want it to name the router's /gateway", stdout.String())
	}
}

func TestRunRefusesABadRouterPort(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"status"}, &stdout, &stderr, RunOptions{Env: domain.EnvLookup{"HUDDLE_ROUTER_PORT": "gateway"}}); code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "HUDDLE_ROUTER_PORT") {
		t.Errorf("stderr = %q, want the bad variable named", stderr.String())
	}
}

// The probe a real run takes is bounded: a router that never answers cannot
// hold a word.
func TestFetchHealthIsBounded(t *testing.T) {
	hung := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(3 * time.Second)
	})
	server := httptest.NewServer(hung)
	t.Cleanup(server.Close)
	started := time.Now()
	if fetchHealth(server.URL) {
		t.Error("a hung endpoint reads as healthy")
	}
	if elapsed := time.Since(started); elapsed > 2500*time.Millisecond {
		t.Errorf("the probe took %v, want it bounded at its two-second budget", elapsed)
	}
}
