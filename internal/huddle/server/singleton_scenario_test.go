package server

// One server per home: the home's serve lock, what a second start meets, and what a start
// finds left behind.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// serving is one in-process server a scenario started: its address, its stop and the error its
// Serve answers (nil while it serves).
type serving struct {
	base   string
	cancel context.CancelFunc
	done   <-chan error
}

// startAServer runs one Serve over home on a port of its own and waits for it to answer
// /health. It takes the scenario's s.T, so the server and its stop outlive every step; the
// error carries the server's own words, and the step's t reports it.
func startAServer(scenario *testing.T, home string) (*serving, error) {
	log := &syncBuffer{}
	srv := &serving{}
	port, err := aFreePort()
	if err != nil {
		return nil, err
	}
	srv.base = fmt.Sprintf("http://127.0.0.1:%d", port)
	o := Options{
		Version: "0.0.0", Home: home, Data: home, Token: "scenario-root",
		Port: port, PortNamed: true, Stdout: log, Stderr: log,
	}
	ctx, cancel := context.WithCancel(context.Background())
	srv.cancel = cancel
	done := make(chan error, 1)
	srv.done = done
	scenario.Cleanup(cancel)
	go func() { done <- Serve(ctx, o) }()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if answers(srv.base) {
			return srv, nil
		}
		if time.Now().After(deadline) {
			cancel()
			return nil, fmt.Errorf("the server never came up: %s", log.joined())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// aFreePort answers a loopback port that is free now.
func aFreePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port, nil
}

// answers says whether a Huddle answers its health path at url.
func answers(url string) bool {
	if url == "" {
		return false
	}
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(url + "/health")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func TestOneHuddleServerPerHome(t *testing.T) {
	bdd.Feature(t, "one Huddle server per home", func(f *bdd.F) {
		f.Scenario("a second server for the same home exits without serving", func(s *bdd.S) {
			var (
				home     string
				first    *serving
				serveErr error
				heldErr  *HeldError
			)
			s.Given("a server serving a temp home", func(t *testing.T) {
				home = s.T.TempDir()
				var err error
				first, err = startAServer(s.T, home)
				if err != nil {
					t.Fatalf("start a server: %v", err)
				}
			})
			s.When("a second server starts on the same home", func(t *testing.T) {
				port, err := aFreePort()
				if err != nil {
					t.Fatal(err)
				}
				log := &syncBuffer{}
				serveErr = Serve(context.Background(), Options{
					Version: "0.0.0", Home: home, Data: home, Token: "scenario-root",
					Port: port, PortNamed: true, Stdout: log, Stderr: log,
				})
			})
			s.Then("it is refused naming the first server's pid", func(t *testing.T) {
				if !errors.As(serveErr, &heldErr) {
					t.Fatalf("Serve = %v, want a *HeldError", serveErr)
				}
				if heldErr.PID != os.Getpid() {
					t.Errorf("HeldError pid = %d, want %d (the first server's pid)", heldErr.PID, os.Getpid())
				}
			})
			s.And("the first server still answers", func(t *testing.T) {
				if !answers(first.base) {
					t.Fatalf("the first server stopped answering at %s", first.base)
				}
			})
		})

		f.Scenario("servers for two different homes run side by side", func(s *bdd.S) {
			var one, two *serving
			s.Given("a server serving one home", func(t *testing.T) {
				var err error
				one, err = startAServer(s.T, s.T.TempDir())
				if err != nil {
					t.Fatalf("start a server: %v", err)
				}
			})
			s.When("another server starts on another home", func(t *testing.T) {
				var err error
				two, err = startAServer(s.T, s.T.TempDir())
				if err != nil {
					t.Fatalf("start a server: %v", err)
				}
			})
			s.Then("both answer", func(t *testing.T) {
				if !answers(one.base) {
					t.Errorf("the first server does not answer at %s", one.base)
				}
				if !answers(two.base) {
					t.Errorf("the second server does not answer at %s", two.base)
				}
			})
		})

		f.Scenario("a lock file left behind by a dead process does not block a start", func(s *bdd.S) {
			var (
				home string
				srv  *serving
			)
			s.Given("a home whose run directory holds a lock file naming pid 999999 and no live holder", func(t *testing.T) {
				home = s.T.TempDir()
				if err := os.MkdirAll(filepath.Join(home, "run"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(home, "run", "serve.lock"), []byte("999999\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			})
			s.When("a server starts on that home", func(t *testing.T) {
				var err error
				srv, err = startAServer(s.T, home)
				if err != nil {
					t.Fatalf("start a server: %v", err)
				}
			})
			s.Then("it serves", func(t *testing.T) {
				if !answers(srv.base) {
					t.Fatalf("the server does not answer at %s", srv.base)
				}
			})
		})

		f.Scenario("the lock is released when the server stops", func(s *bdd.S) {
			var (
				home  string
				first *serving
			)
			s.Given("a server serving a temp home", func(t *testing.T) {
				home = s.T.TempDir()
				var err error
				first, err = startAServer(s.T, home)
				if err != nil {
					t.Fatalf("start a server: %v", err)
				}
			})
			s.When("that server stops", func(t *testing.T) {
				first.cancel()
				select {
				case err := <-first.done:
					if err != nil {
						t.Fatalf("Serve = %v", err)
					}
				case <-time.After(15 * time.Second):
					t.Fatal("Serve never returned")
				}
			})
			s.And("a new one starts on the same home", func(t *testing.T) {
				second, err := startAServer(s.T, home)
				if err != nil {
					t.Fatalf("start a server: %v", err)
				}
				if !answers(second.base) {
					t.Fatalf("the new server does not answer at %s", second.base)
				}
			})
		})
	})
}
