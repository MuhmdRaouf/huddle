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
	"strings"
	"syscall"
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
	tcp := ports.Listen(t)
	port := tcp.Addr().(*net.TCPAddr).Port
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

// fakeHandover is one handover socket that answers every takeover with the given line and file
// descriptors, the way a broken daemon would.
func fakeHandover(t *testing.T, answer func(conn *net.UnixConn)) string {
	t.Helper()
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
				unixConn, ok := conn.(*net.UnixConn)
				if !ok {
					return
				}
				_ = unixConn.SetDeadline(time.Now().Add(5 * time.Second))
				_, _ = bufio.NewReader(unixConn).ReadString('\n')
				answer(unixConn)
			}(conn)
		}
	}()
	return home
}

// sendRights writes one message with the given file descriptors as SCM_RIGHTS.
func sendRights(conn *net.UnixConn, text string, fds ...int) error {
	rights := syscall.UnixRights(fds...)
	_, _, err := conn.WriteMsgUnix([]byte(text), rights, nil)
	return err
}

func TestTakeoverRefusesAnAnswerItCannotUse(t *testing.T) {
	t.Parallel()
	pipe := func(t *testing.T) int {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
		return int(w.Fd())
	}

	t.Run("an answer naming no port", func(t *testing.T) {
		t.Parallel()
		fd := pipe(t)
		home := fakeHandover(t, func(conn *net.UnixConn) {
			_ = sendRights(conn, "ok not a port\n", fd)
		})
		if _, err := Takeover(context.Background(), home); err == nil || !strings.Contains(err.Error(), "answered") {
			t.Fatalf("Takeover = %v, want the bad-answer error", err)
		}
	})

	t.Run("an answer carrying no socket", func(t *testing.T) {
		t.Parallel()
		home := fakeHandover(t, func(conn *net.UnixConn) {
			_, _ = conn.Write([]byte("ok 20000\n"))
		})
		if _, err := Takeover(context.Background(), home); err == nil || !strings.Contains(err.Error(), "handed over 0 sockets") {
			t.Fatalf("Takeover = %v, want the missing-socket error", err)
		}
	})

	t.Run("an answer carrying two sockets", func(t *testing.T) {
		t.Parallel()
		first, second := pipe(t), pipe(t)
		home := fakeHandover(t, func(conn *net.UnixConn) {
			_ = sendRights(conn, "ok 20000\n", first, second)
		})
		if _, err := Takeover(context.Background(), home); err == nil || !strings.Contains(err.Error(), "handed over 2 sockets") {
			t.Fatalf("Takeover = %v, want the two-socket error", err)
		}
	})
}

func TestServeHandoverSkipsASocketItCannotServe(t *testing.T) {
	t.Parallel()
	var lines []string
	log := func(line string) { lines = append(lines, line) }
	handed := make(chan int, 1)
	tcp := ports.Listen(t)

	t.Run("a listener that is no TCP socket", func(t *testing.T) {
		home := shortHome(t)
		if err := os.MkdirAll(filepath.Join(home, "run"), 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(home, "run", "other.sock")
		unix, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = unix.Close(); _ = os.Remove(path) })
		stop := serveHandover(home, 1, unix, handed, log)
		stop()
		if !strings.Contains(strings.Join(lines, "\n"), "not a TCP socket") {
			t.Errorf("the log = %q, want the refusal", strings.Join(lines, "\n"))
		}
	})

	t.Run("a run dir that cannot be made", func(t *testing.T) {
		base := t.TempDir()
		blocker := filepath.Join(base, "blocker")
		if err := os.WriteFile(blocker, []byte("a file"), 0o600); err != nil {
			t.Fatal(err)
		}
		stop := serveHandover(filepath.Join(blocker, "home"), 1, tcp, handed, log)
		stop()
		if !strings.Contains(strings.Join(lines, "\n"), "handover socket:") {
			t.Errorf("the log = %q, want the socket's failure", strings.Join(lines, "\n"))
		}
	})

	t.Run("a socket path past the OS's limit", func(t *testing.T) {
		deep := t.TempDir()
		for i := 0; i < 8; i++ {
			deep = filepath.Join(deep, "a-very-long-directory-name-beyond-any-socket-path")
		}
		stop := serveHandover(deep, 1, tcp, handed, log)
		stop()
		if !strings.Contains(strings.Join(lines, "\n"), "handover socket:") {
			t.Errorf("the log = %q, want the listen's failure", strings.Join(lines, "\n"))
		}
	})
}

// TestAnswerHandoverKeepsServingOnAQuestionItCannotRead dials the daemon's handover socket with
// questions that lead nowhere: the daemon closes the connection, hands nothing over, and keeps
// serving.
func TestAnswerHandoverKeepsServingOnAQuestionItCannotRead(t *testing.T) {
	t.Parallel()
	home := shortHome(t)
	tcp := ports.Listen(t)
	port := tcp.Addr().(*net.TCPAddr).Port
	handed := make(chan int, 1)
	stop := serveHandover(home, port, tcp, handed, func(string) {})
	t.Cleanup(stop)
	path := filepath.Join(home, "run", handoverName)

	t.Run("a question that is no takeover line", func(t *testing.T) {
		conn, err := net.Dial("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write([]byte("hello\n")); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		one := make([]byte, 16)
		if _, err := conn.Read(one); err == nil {
			t.Error("the daemon answered a bad question, want the connection closed")
		}
		_ = conn.Close()
	})

	t.Run("a connection that hangs up mid-question", func(t *testing.T) {
		conn, err := net.Dial("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
		time.Sleep(100 * time.Millisecond)
	})

	select {
	case taker := <-handed:
		t.Errorf("the daemon handed its socket to %d on a question it cannot read", taker)
	default:
	}
	// the handover serves on: a proper question still gets the socket
	taker, err := Takeover(context.Background(), home)
	if err != nil {
		t.Fatalf("the takeover after the bad questions: %v", err)
	}
	_ = taker.Close()
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
	if _, err := rssOf(-1); err == nil {
		t.Error("rssOf(-1) = nil error, want ps's failure")
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

func TestRSSIntervalOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
		want time.Duration
	}{
		{"no variable is thirty seconds", "", rssInterval},
		{"a millisecond value wins", "250", 250 * time.Millisecond},
		{"a junk value is the default", "soon", rssInterval},
		{"zero is the default", "0", rssInterval},
		{"a negative is the default", "-5", rssInterval},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := domain.EnvLookup{}
			if tt.raw != "" {
				env["HUDDLE_ROUTER_RSS_INTERVAL_MS"] = tt.raw
			}
			if got := rssIntervalOf(env); got != tt.want {
				t.Errorf("rssIntervalOf(%q) = %s, want %s", tt.raw, got, tt.want)
			}
		})
	}
}
