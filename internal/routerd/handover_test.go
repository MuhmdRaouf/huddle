package routerd

// The handover's parts on their own: a taker gets a working listener over the unix socket, a
// missing or silent daemon is an error within five seconds, and the rss column of ps output
// parses into bytes. The daemon-level behaviour is the scenario file's; the real binary's is
// the e2e in cmd/huddle.

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// shortHome is a home dir short enough for a unix socket under its run dir (macOS caps a
// socket path at 104 bytes, and a test temp dir's path is longer): a fresh, tersely named
// directory under the system's temp location, removed at cleanup.
func shortHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(os.TempDir(), "hr-ho-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestTakeoverPassesAWorkingListener(t *testing.T) {
	t.Parallel()
	home := shortHome(t)
	port := ports.Free(t)
	tcp, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tcp.Close() })
	handed := make(chan int, 1)
	stop := serveHandover(home, port, tcp, handed, func(string) {})
	t.Cleanup(stop)

	taker, err := Takeover(context.Background(), home)
	if err != nil {
		t.Fatalf("takeover: %v", err)
	}
	defer taker.Close()

	// the handed-over listener serves: one HTTP round trip through the same port answers
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "handed over")
	})}
	served := make(chan struct{})
	go func() { _ = server.Serve(taker); close(served) }()
	t.Cleanup(func() { _ = server.Close() })
	answer, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		t.Fatalf("get through the handed-over listener: %v", err)
	}
	defer answer.Body.Close()
	body := make([]byte, 32)
	n, _ := answer.Body.Read(body)
	if string(body[:n]) != "handed over" {
		t.Errorf("the handed-over listener answered %q, want %q", body[:n], "handed over")
	}
	select {
	case pid := <-handed:
		if pid != os.Getpid() {
			t.Errorf("the taker's pid = %d, want %d", pid, os.Getpid())
		}
	case <-time.After(time.Second):
		t.Error("the server side never named the taker on handed")
	}
	// the socket file is retired at once, so the taker can open its own
	if _, err := os.Stat(filepath.Join(home, "run", handoverName)); !os.IsNotExist(err) {
		t.Error("the handover socket file survived the takeover")
	}
}

func TestTakeoverNamesTheErrors(t *testing.T) {
	t.Parallel()
	t.Run("a missing socket is an error at once", func(t *testing.T) {
		t.Parallel()
		home := shortHome(t)
		start := time.Now()
		listener, err := Takeover(context.Background(), home)
		if err == nil {
			_ = listener.Close()
			t.Fatal("takeover with no daemon = nil, want an error")
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("a missing socket took %s to answer, want at once", elapsed)
		}
	})
	t.Run("a silent socket is an error within five seconds", func(t *testing.T) {
		t.Parallel()
		home := shortHome(t)
		if err := os.MkdirAll(filepath.Join(home, "run"), 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(home, "run", handoverName)
		unix, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = unix.Close(); _ = os.Remove(path) })
		go func() {
			for {
				conn, err := unix.Accept()
				if err != nil {
					return
				}
				go func(conn net.Conn) {
					defer conn.Close()
					reader := bufio.NewReader(conn)
					if _, err := reader.ReadString('\n'); err != nil {
						return
					}
					// the question reads, the answer never comes
					time.Sleep(10 * time.Second)
				}(conn)
			}
		}()
		start := time.Now()
		listener, err := Takeover(context.Background(), home)
		if err == nil {
			_ = listener.Close()
			t.Fatal("takeover from a silent socket = nil, want an error")
		}
		if elapsed := time.Since(start); elapsed > 6*time.Second {
			t.Errorf("a silent socket took %s to answer, want within 5 s", elapsed)
		}
	})
}

func TestParseRSS(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		out     string
		want    int64
		wantErr bool
	}{
		{"one line in KiB", "  12345\n", 12345 << 10, false},
		{"zero", "0\n", 0, false},
		{"the first line wins", "1024\n2048\n", 1024 << 10, false},
		{"no output", "", 0, true},
		{"not a number", "soon\n", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseRSS(tt.out)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseRSS(%q) = %d, want an error", tt.out, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRSS(%q): %v", tt.out, err)
			}
			if got != tt.want {
				t.Errorf("parseRSS(%q) = %d, want %d", tt.out, got, tt.want)
			}
		})
	}
}

func TestRSSOfReadsThisProcess(t *testing.T) {
	t.Parallel()
	rss, err := rssOf(os.Getpid())
	if err != nil {
		t.Fatalf("rss of this process: %v", err)
	}
	if rss <= 0 {
		t.Errorf("this process's rss = %d, want more", rss)
	}
}

func TestRSSCeilingOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		env  string
		set  int64
		want int64
	}{
		{"no option and no variable is one GiB", "", 0, 1 << 30},
		{"the variable overrides in MiB", "256", 0, 256 << 20},
		{"the option overrides the variable", "256", 64 << 20, 64 << 20},
		{"a bad variable is the default", "many", 0, 1 << 30},
		{"a zero variable is the default", "0", 0, 1 << 30},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := domain.EnvLookup{}
			if tt.env != "" {
				env["HUDDLE_ROUTER_RSS_CEILING_MB"] = tt.env
			}
			if got := rssCeilingOf(env, Options{RSSCeiling: tt.set}); got != tt.want {
				t.Errorf("rssCeilingOf = %d, want %d", got, tt.want)
			}
		})
	}
}
