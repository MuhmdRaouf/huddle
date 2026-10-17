package server

import (
	"errors"
	"net"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// occupiedPort binds an ephemeral port and holds it for the test's lifetime.
func occupiedPort(t *testing.T) int {
	t.Helper()
	holder, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind holder: %v", err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	return holder.Addr().(*net.TCPAddr).Port
}

// recentlyFreePort binds an ephemeral port, then releases it — a port that was free a moment ago.
func recentlyFreePort(t *testing.T) int {
	t.Helper()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind probe: %v", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	if err := probe.Close(); err != nil {
		t.Fatalf("release probe: %v", err)
	}
	return port
}

// closeServer releases what a test bound.
func closeServer(s *Server) {
	if listener := s.Listener(); listener != nil {
		_ = listener.Close()
	}
}

// listenErrorOf is the ListenError a failed bind answers.
func listenErrorOf(t *testing.T, err error) *ListenError {
	t.Helper()
	var listenErr *ListenError
	if !errors.As(err, &listenErr) {
		t.Fatalf("bind failed with %v, not a *ListenError", err)
	}
	return listenErr
}

func TestListenLocal(t *testing.T) {
	t.Run("exposes the random-port policy constants", func(t *testing.T) {
		if PortMin != 10_000 {
			t.Errorf("PortMin = %d, want 10000", PortMin)
		}
		if PortMax != 65_536 {
			t.Errorf("PortMax = %d, want 65536", PortMax)
		}
		if MaxAttempts != 50 {
			t.Errorf("MaxAttempts = %d, want 50", MaxAttempts)
		}
		draw := NativeRandom(PortMin, PortMax)
		if draw < PortMin || draw >= PortMax {
			t.Errorf("nativeRandom() = %d, want one in [%d, %d)", draw, PortMin, PortMax)
		}
	})

	t.Run("binds a random port on the loopback and reports it", func(t *testing.T) {
		srv := NewServer()
		defer closeServer(srv)
		port, err := ListenLocal(t.Context(), srv, Options{
			Random:   func(_, _ int) int { return 25_000 },
			Attempts: 3,
		})
		if err != nil {
			t.Fatalf("listenLocal: %v", err)
		}
		if port != 25_000 {
			t.Errorf("port = %d, want 25000", port)
		}
		if got := BoundPort(srv); got != 25_000 {
			t.Errorf("boundPort() = %d, want 25000", got)
		}
	})

	t.Run("uses the native draw and the full budget when nothing is injected", func(t *testing.T) {
		srv := NewServer()
		defer closeServer(srv)
		port, err := ListenLocal(t.Context(), srv, Options{})
		if err != nil {
			t.Fatalf("listenLocal: %v", err)
		}
		if port < PortMin || port >= PortMax {
			t.Errorf("port = %d, want one in [%d, %d)", port, PortMin, PortMax)
		}
		if got := BoundPort(srv); got != port {
			t.Errorf("boundPort() = %d, want %d", got, port)
		}
	})

	t.Run("retries EADDRINUSE with a fresh draw in range", func(t *testing.T) {
		occupied := occupiedPort(t)
		free := recentlyFreePort(t)
		var draws []int
		random := func(min, _max int) int {
			draws = append(draws, min)
			if len(draws) <= 2 {
				return occupied
			}
			return free
		}
		srv := NewServer()
		defer closeServer(srv)
		port, err := ListenLocal(t.Context(), srv, Options{Random: random, Attempts: 5})
		if err != nil {
			t.Fatalf("listenLocal: %v", err)
		}
		if port != free {
			t.Errorf("port = %d, want %d", port, free)
		}
		if want := []int{PortMin, PortMin, PortMin}; !slices.Equal(draws, want) {
			t.Errorf("draws = %v, want %v", draws, want)
		}
	})

	t.Run("gives up after the attempt budget with a clear message", func(t *testing.T) {
		occupied := occupiedPort(t)
		draws := 0
		random := func(int, int) int {
			draws += 1
			return occupied
		}
		srv := NewServer()
		defer closeServer(srv)
		_, err := ListenLocal(t.Context(), srv, Options{Random: random, Attempts: 4})
		listenErr := listenErrorOf(t, err)
		if !strings.Contains(listenErr.Message, "no free port after 4 random draws") {
			t.Errorf("message = %q, want it to name the budget", listenErr.Message)
		}
		if !strings.Contains(listenErr.Message, "EADDRINUSE") {
			t.Errorf("message = %q, want it to name EADDRINUSE", listenErr.Message)
		}
		if draws != 4 {
			t.Errorf("draws = %d, want 4", draws)
		}
	})

	t.Run("surfaces non-retryable errors without redrawing", func(t *testing.T) {
		busy := NewServer()
		defer closeServer(busy)
		if err := listenOnce(t.Context(), busy, 0); err != nil {
			t.Fatalf("first bind: %v", err)
		}
		draws := 0
		random := func(int, int) int {
			draws += 1
			return 25_000
		}
		_, err := ListenLocal(t.Context(), busy, Options{Random: random, Attempts: 5})
		listenErr := listenErrorOf(t, err)
		if listenErr.Code != AlreadyListening {
			t.Errorf("code = %q, want %q", listenErr.Code, AlreadyListening)
		}
		if draws != 1 {
			t.Errorf("draws = %d, want 1", draws)
		}
	})

	t.Run("tries a pinned port exactly once, success or failure", func(t *testing.T) {
		free := recentlyFreePort(t)
		first := NewServer()
		defer closeServer(first)
		port, err := ListenLocal(t.Context(), first, Options{Port: free})
		if err != nil {
			t.Fatalf("pinned bind: %v", err)
		}
		if port != free {
			t.Errorf("port = %d, want %d", port, free)
		}

		occupied := occupiedPort(t)
		never := func(_, _ int) int { panic("pinned ports must not draw") }
		failed := NewServer()
		defer closeServer(failed)
		_, err = ListenLocal(t.Context(), failed, Options{Port: occupied, Random: never})
		listenErr := listenErrorOf(t, err)
		if listenErr.Code != AddrInUse {
			t.Errorf("code = %q, want %q", listenErr.Code, AddrInUse)
		}
	})

	t.Run("reports no port for a server that never listened", func(t *testing.T) {
		if got := BoundPort(NewServer()); got != 0 {
			t.Errorf("boundPort() = %d, want 0", got)
		}
	})
}

func TestListenError(t *testing.T) {
	t.Run("maps the address-in-use and refused errnos", func(t *testing.T) {
		cases := []struct {
			err  error
			want string
		}{
			{syscall.EADDRINUSE, AddrInUse},
			{syscall.EACCES, PermissionDenied},
		}
		for _, tt := range cases {
			got := listenError(tt.err, 20_000)
			if got.Code != tt.want {
				t.Errorf("listenError(%v).Code = %q, want %q", tt.err, got.Code, tt.want)
			}
			if !strings.Contains(got.Message, tt.want) {
				t.Errorf("listenError(%v).Message = %q, want it to name %q", tt.err, got.Message, tt.want)
			}
		}
		// a failure with no errno of its own carries no code
		if got := listenError(errors.New("bind: nope"), 20_000); got.Code != "" {
			t.Errorf("listenError(plain error).Code = %q, want empty", got.Code)
		}
	})
}
