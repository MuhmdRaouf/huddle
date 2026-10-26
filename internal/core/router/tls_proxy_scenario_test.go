package router

// The blind tunnel's answer: a CONNECT is answered only once its target is open, so a target
// that cannot be reached — or one that turns out to be this machine — comes back as the
// refusal's reason instead of a 200 the client's TLS handshake then fails on. Written as
// scenarios over the rig in tls_proxy_test.go.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

func TestTLSProxyConnectScenarios(t *testing.T) {
	t.Parallel()
	bdd.Feature(t, "a blind tunnel dials its target before it answers", func(f *bdd.F) {
		f.Scenario("a CONNECT whose dial is refused as local gets the refusal and no 200", func(s *bdd.S) {
			var (
				rig  *tlsProxyRig
				got  *http.Response
				body string
			)
			s.Given("a proxy whose dial refuses a name that is not spelled local", func(t *testing.T) {
				rig = newTLSProxyRig(s.T, func(p *TLSProxy) {
					p.Dial = func(context.Context, string, string) (net.Conn, error) {
						return nil, &localDialError{addr: "huddle-hub.internal:443"}
					}
				})
			})
			s.When("a CONNECT to that name arrives", func(t *testing.T) {
				conn, answer := connectThrough(t, rig.port, "huddle-hub.internal:443")
				defer conn.Close()
				got, body = answer, refuseBody(t, answer)
			})
			s.Then("it is refused 403 naming this machine, and no 200 went first", func(t *testing.T) {
				if got.StatusCode != http.StatusForbidden {
					t.Fatalf("the CONNECT = %d %q, want 403", got.StatusCode, body)
				}
				if !strings.Contains(body, "may not name this machine") {
					t.Errorf("body = %q, want the local-name refusal", body)
				}
			})
		})

		f.Scenario("the direct dialer's refusal keeps its type through the dial", func(s *bdd.S) {
			var dialErr error
			s.Given("the production dial dialing a loopback address", func(t *testing.T) {
				addr := fmt.Sprintf("127.0.0.1:%d", ports.Free(s.T))
				_, dialErr = (&TLSProxy{}).tunnelDial(context.Background(), "tcp", addr)
			})
			s.Then("the error a CONNECT handler sees is still the local refusal", func(t *testing.T) {
				var local *localDialError
				if !errors.As(dialErr, &local) {
					t.Fatalf("dial = %v, want a *localDialError", dialErr)
				}
			})
		})

		f.Scenario("a CONNECT to a target nothing listens on gets 502 and no 200", func(s *bdd.S) {
			var (
				rig  *tlsProxyRig
				got  *http.Response
				body string
			)
			s.Given("a proxy whose dial reaches a port nothing listens on", func(t *testing.T) {
				dead := ports.Free(s.T)
				rig = newTLSProxyRig(s.T, func(p *TLSProxy) {
					p.Dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
						var dialer net.Dialer
						return dialer.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", dead))
					}
				})
			})
			s.When("a CONNECT to a remote name arrives", func(t *testing.T) {
				conn, answer := connectThrough(t, rig.port, "down-target.example:443")
				defer conn.Close()
				got, body = answer, refuseBody(t, answer)
			})
			s.Then("it is answered 502 naming the unreachable target, and no 200 went first", func(t *testing.T) {
				if got.StatusCode != http.StatusBadGateway {
					t.Fatalf("the CONNECT = %d %q, want 502", got.StatusCode, body)
				}
				if !strings.Contains(body, "cannot be reached") {
					t.Errorf("body = %q, want the unreachable-target message", body)
				}
			})
		})

		f.Scenario("a CONNECT to a live target is established and carries bytes both ways", func(s *bdd.S) {
			var rig *tlsProxyRig
			s.Given("a proxy whose dial reaches an echo server", func(t *testing.T) {
				listener := ports.Listen(s.T)
				go func() {
					for {
						conn, err := listener.Accept()
						if err != nil {
							return
						}
						go func() {
							_, _ = io.Copy(conn, conn)
							_ = conn.Close()
						}()
					}
				}()
				rig = newTLSProxyRig(s.T, func(p *TLSProxy) {
					p.Dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
						var dialer net.Dialer
						return dialer.DialContext(ctx, "tcp", listener.Addr().String())
					}
				})
			})
			var (
				conn net.Conn
				got  *http.Response
			)
			s.When("a CONNECT to a remote name arrives", func(t *testing.T) {
				c, answer := connectThrough(t, rig.port, "live-target.example:443")
				conn, got = c, answer
			})
			s.Then("it is established", func(t *testing.T) {
				if got.StatusCode != 200 {
					t.Fatalf("the CONNECT = %d, want 200 established", got.StatusCode)
				}
			})
			s.And("the bytes pass both ways", func(t *testing.T) {
				defer conn.Close()
				if _, err := conn.Write([]byte("a client hello")); err != nil {
					t.Fatal(err)
				}
				_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
				back := make([]byte, len("a client hello"))
				if _, err := io.ReadFull(conn, back); err != nil {
					t.Fatalf("the tunnel never answered: %v", err)
				}
				if string(back) != "a client hello" {
					t.Errorf("the tunnel carried %q", back)
				}
			})
		})
	})
}
