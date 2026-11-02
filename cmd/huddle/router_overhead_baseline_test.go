package main

// The router overhead guard's references: one child process of this test binary that does
// what a router does to a stream and nothing else. It holds two listeners, a plain TCP relay
// for the provider lane and a TLS twin for the CONNECT path, both forwarding to the fake
// upstream. The router is a process of its own, so a reference in the test process would
// leave the process, scheduler and loopback costs of a hop out of the baseline; one child
// process serves both paths the way the router does, so their streams contend inside the
// reference as they do inside the router. The twin terminates TLS with a leaf the router
// CA's code mints from the test's own CA directory, then forwards the decrypted bytes.

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/pki"
	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
)

const (
	// referenceUpstreamEnv carries the fake's host:port to the reference child, and
	// referenceCADirEnv the CA directory its twin signs with. The test binary's sandbox drops
	// every HUDDLE_* variable and keeps E2E_* ones.
	referenceUpstreamEnv = "E2E_OVERHEAD_REFERENCE_UPSTREAM"
	referenceCADirEnv    = "E2E_OVERHEAD_REFERENCE_CA_DIR"
	// referenceReady is the first word of the child's one line on stdout.
	referenceReady = "ready"
)

// overheadReference is the running reference child: its pid and the two addresses the paths
// that stand in for the router dial.
type overheadReference struct {
	pid       int
	relayAddr string
	twinAddr  string
}

// TestOverheadReferenceHelper is the reference child: the e2e re-executes this test binary
// with the upstream in its environment. It serves the relay and the twin, prints one ready
// line (pid, relay address, twin address), and runs until its stdin closes.
func TestOverheadReferenceHelper(t *testing.T) {
	upstream := os.Getenv(referenceUpstreamEnv)
	if upstream == "" {
		t.Skip("the e2e's helper process, not a test")
	}
	relay, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	ca, err := pki.LoadOrCreateRouterCA(context.Background(), os.Getenv(referenceCADirEnv), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := ca.Leaf(pki.RouterHost, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	plainTwin, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	twin := tls.NewListener(plainTwin, &tls.Config{Certificates: []tls.Certificate{*leaf}, MinVersion: tls.VersionTLS12})
	defer twin.Close()
	go relayTo(relay, upstream)
	go relayTo(twin, upstream)
	fmt.Println(referenceReady, os.Getpid(), relay.Addr(), plainTwin.Addr())
	_, _ = io.Copy(io.Discard, os.Stdin)
}

// relayTo forwards every connection listener accepts to upstream byte for byte, the cheapest
// hop there is. Closing a side ends both directions.
func relayTo(listener net.Listener, upstream string) {
	for {
		client, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			server, err := net.Dial("tcp", upstream)
			if err != nil {
				_ = client.Close()
				return
			}
			go func() {
				_, _ = io.Copy(server, client)
				_ = server.Close()
			}()
			_, _ = io.Copy(client, server)
			_ = client.Close()
		}()
	}
}

// startOverheadReference starts the reference child in front of upstream (host:port), with
// caDir as the CA its twin's certificate comes from, and stops it when the test ends.
func startOverheadReference(t *testing.T, upstream, caDir string) overheadReference {
	t.Helper()

	pkgDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestOverheadReferenceHelper$")
	child.Env = isoenv.Environ(t, filepath.Join(t.TempDir(), "reference"),
		referenceUpstreamEnv+"="+upstream, referenceCADirEnv+"="+caDir)
	// the child's TestMain builds the binary from the module root, found from this directory
	child.Dir = pkgDir
	child.Stderr = os.Stderr
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		exited := make(chan struct{})
		go func() { _ = child.Wait(); close(exited) }()
		select {
		case <-exited:
		case <-time.After(15 * time.Second):
			_ = child.Process.Kill()
			<-exited
		}
	})

	// the child's TestMain may print before the test runs: the ready line is the one that parses
	ready := make(chan overheadReference, 1)
	go func() {
		lines := bufio.NewScanner(stdout)
		for lines.Scan() {
			fields := strings.Fields(lines.Text())
			if len(fields) != 4 || fields[0] != referenceReady {
				continue
			}
			pid, err := strconv.Atoi(fields[1])
			if err != nil {
				continue
			}
			ready <- overheadReference{pid: pid, relayAddr: fields[2], twinAddr: fields[3]}
			return
		}
	}()
	select {
	case reference := <-ready:
		return reference
	case <-time.After(90 * time.Second):
		t.Fatal("the reference process never said it was ready")
		return overheadReference{}
	}
}

// routerCAPool is the pool that trusts the CA in caDir, created there on first use.
func routerCAPool(t *testing.T, caDir string) *x509.CertPool {
	t.Helper()
	ca, err := pki.LoadOrCreateRouterCA(context.Background(), caDir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca.CertPEM()) {
		t.Fatal("the CA's PEM did not parse")
	}
	return pool
}

// twinClient reaches the TLS twin at addr as the router's name: every dial goes to addr, and
// the certificate is checked for pki.RouterHost against pool.
func twinClient(pool *x509.CertPool, addr string) *http.Client {
	dialer := &net.Dialer{}
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		},
		TLSClientConfig:    &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		DisableCompression: true,
	}}
}

func TestOverheadReferenceRunsInAnotherProcess(t *testing.T) {
	base := time.Now()
	fake := overheadFake(t, base)
	caDir := filepath.Join(t.TempDir(), "ca")
	pool := routerCAPool(t, caDir)
	reference := startOverheadReference(t, strings.TrimPrefix(fake.URL, "http://"), caDir)
	if reference.pid <= 0 || reference.pid == os.Getpid() {
		t.Fatalf("the reference runs as pid %d, the test as %d: want another process", reference.pid, os.Getpid())
	}
	plain := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DisableCompression: true}}
	for _, path := range []struct {
		name   string
		client *http.Client
		target string
	}{
		{"relay", plain, "http://" + reference.relayAddr + "/v1/messages"},
		{"twin", twinClient(pool, reference.twinAddr), "https://" + pki.RouterHost + "/v1/messages"},
	} {
		timing, err := readStream(path.client, path.target, "claude-opus-5-5", 0, base)
		if err != nil {
			t.Fatalf("%s: %v", path.name, err)
		}
		if timing.firstByte <= 0 || timing.chunk <= 0 {
			t.Errorf("%s: measured first byte %v, chunk %v: want both timed", path.name, timing.firstByte, timing.chunk)
		}
	}
}
