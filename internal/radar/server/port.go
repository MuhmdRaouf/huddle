// Package server is the dashboard's loopback server: port selection and binding.
package server

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
	"syscall"
)

// Port selection: a random 5-digit port, never a fixed default (the app tries the saved port
// first). A pinned port is tried once and its failure surfaces at once; a random draw retries
// on EADDRINUSE/EACCES with a fresh draw, bounded so a hopeless machine fails with a message
// instead of spinning.
const (
	// PortMin is the lowest port a random draw may pick.
	PortMin = 10_000
	// PortMax is one past the highest port a random draw may pick, like the draw itself.
	PortMax = 65_536
	// MaxAttempts bounds the random draws one bind may spend.
	MaxAttempts = 50
)

// The codes a bind failure carries; AddrInUse and PermissionDenied earn a fresh draw, every
// other failure surfaces at once.
const (
	// AddrInUse: another program holds the port right now.
	AddrInUse = "EADDRINUSE"
	// PermissionDenied: the OS refuses that port for this process.
	PermissionDenied = "EACCES"
	// AlreadyListening: the server is bound and was never closed.
	AlreadyListening = "ERR_SERVER_ALREADY_LISTEN"
)

// RandomSource draws a port from [min, max); injectable so tests fix the draws.
type RandomSource func(min, max int) int

// NativeRandom is the production source: eight crypto bytes taken modulo the span. crypto/rand
// never fails a Read, so a broken source draws one port forever rather than refusing to pick.
func NativeRandom(min, max int) int {
	var bytes [8]byte
	_, _ = rand.Read(bytes[:])
	return min + int(binary.BigEndian.Uint64(bytes[:])%uint64(max-min))
}

// Server is the dashboard's loopback socket: unbound until ListenLocal binds it, then served
// through Listener.
type Server struct {
	listener net.Listener
}

// NewServer is an unbound server.
func NewServer() *Server { return &Server{} }

// Listener is the bound socket, nil until a bind succeeds.
func (s *Server) Listener() net.Listener { return s.listener }

// ListenError is a bind failure with its errno-style code; the code decides whether a fresh
// draw could dodge it.
type ListenError struct {
	// Code is AddrInUse, PermissionDenied, AlreadyListening, or empty when the failure carries
	// no code of its own.
	Code string
	// Message is the failure text the user reads; a coded failure carries its code and the
	// address refused.
	Message string
}

// Error is the failure text the user reads.
func (e *ListenError) Error() string { return e.Message }

// Retryable says whether a fresh draw could dodge the failure: a port another program holds or
// one the OS refuses may be free on the next draw; nothing else ever will be.
func (e *ListenError) Retryable() bool {
	return e.Code == AddrInUse || e.Code == PermissionDenied
}

// Options picks how ListenLocal chooses a port.
type Options struct {
	// Port pins one port, tried exactly once; zero draws randomly.
	Port int
	// Random draws candidate ports; nil is NativeRandom.
	Random RandomSource
	// Attempts bounds the random draws; below one is MaxAttempts.
	Attempts int
}

// ListenLocal binds server to 127.0.0.1 and answers the port bound. A pinned Port gets one
// attempt and its failure is returned as is; otherwise up to Attempts draws from Random run
// over [PortMin, PortMax), every retryable failure retried with a fresh draw, and the spent
// budget reported as one failure naming the last error.
func ListenLocal(ctx context.Context, s *Server, options Options) (int, error) {
	if options.Port != 0 {
		if err := listenOnce(ctx, s, options.Port); err != nil {
			return 0, err
		}
		return options.Port, nil
	}
	random := options.Random
	if random == nil {
		random = NativeRandom
	}
	attempts := options.Attempts
	if attempts < 1 {
		attempts = MaxAttempts
	}
	return listenRandom(ctx, s, random, attempts)
}

// BoundPort answers the port a bound server is on — the draw chose it, not the caller — or 0
// while it has never listened.
func BoundPort(s *Server) int {
	if s == nil || s.listener == nil {
		return 0
	}
	return s.listener.Addr().(*net.TCPAddr).Port
}

// listenRandom draws ports until one binds or the budget runs out; only a retryable failure
// spends another draw.
func listenRandom(ctx context.Context, s *Server, random RandomSource, attempts int) (int, error) {
	var last *ListenError
	for range attempts {
		port := random(PortMin, PortMax)
		err := listenOnce(ctx, s, port)
		if err == nil {
			return port, nil
		}
		listenErr, ok := err.(*ListenError)
		if !ok || !listenErr.Retryable() {
			return 0, err
		}
		last = listenErr
	}
	return 0, &ListenError{
		Message: fmt.Sprintf("no free port after %d random draws: %s", attempts, last.Message),
	}
}

// listenOnce binds one port on the loopback; a server that is already bound never binds again.
func listenOnce(ctx context.Context, s *Server, port int) error {
	if s.listener != nil {
		return &ListenError{
			Code:    AlreadyListening,
			Message: "Listen method has been called more than once without close",
		}
	}
	var config net.ListenConfig
	listener, err := config.Listen(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return listenError(err, port)
	}
	s.listener = listener
	return nil
}

// listenError maps a bind failure onto its code and message, so the retry decision and the
// text the user reads stay the same on every platform.
func listenError(err error, port int) *ListenError {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case syscall.EADDRINUSE:
			return &ListenError{Code: AddrInUse, Message: listenMessage(AddrInUse, errno, port)}
		case syscall.EACCES:
			return &ListenError{Code: PermissionDenied, Message: listenMessage(PermissionDenied, errno, port)}
		}
	}
	return &ListenError{Message: err.Error()}
}

// listenMessage renders a coded bind failure: the code, the OS's text for it, then the address
// refused.
func listenMessage(code string, errno syscall.Errno, port int) string {
	return fmt.Sprintf("listen %s: %s 127.0.0.1:%d", code, errno, port)
}
