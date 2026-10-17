// The gateway's own run: the foreground serve the detached start leaves behind, its port walk,
// and the two files of `$HUDDLE_HOME` a run keeps — `gateway.port`, the bare port a client
// points a base URL at, and `gateway-run.json`, the pid-and-port record the lifecycle follows.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/spool"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// The port walk sits above the hub (18860-18869) and kanban (18800-18849), below nothing yet.
const (
	PortFirst = 18870
	PortLast  = 18879
)

// portFile and stateFile are the two names a run keeps in the home. The port file is the bare
// number a client reads; the state record's name differs from the config's (`gateway.json`) so
// neither can shadow the other.
const (
	portFile  = "gateway.port"
	stateFile = "gateway-run.json"
)

// logFile is where a detached run appends its output, so a failed start leaves its trace.
const logFile = "gateway.log"

// runState is the lifecycle record: who serves and where.
type runState struct {
	Pid  int64 `json:"pid"`
	Port int   `json:"port"`
}

// listenPort binds the first free port in the gateway's walk.
func listenPort() (net.Listener, error) {
	for port := PortFirst; port <= PortLast; port++ {
		if listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			return listener, nil
		}
	}
	return nil, fmt.Errorf("no free port in %d..%d", PortFirst, PortLast)
}

// writeState lays both files down, private modes, the home made if missing.
func writeState(root string, port int) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	state, err := json.Marshal(runState{Pid: int64(os.Getpid()), Port: port})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, stateFile), state, 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, portFile), []byte(strconv.Itoa(port)+"\n"), 0o600)
}

// readState answers the lifecycle record; none or unreadable is none.
func readState(root string) *runState {
	data, err := os.ReadFile(filepath.Join(root, stateFile))
	if err != nil {
		return nil
	}
	var state runState
	if json.Unmarshal(data, &state) != nil || state.Port <= 0 {
		return nil
	}
	return &state
}

// readPortFile answers the bare port file a client reads; none or unreadable is none.
func readPortFile(root string) (int, bool) {
	data, err := os.ReadFile(filepath.Join(root, portFile))
	if err != nil {
		return 0, false
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || port <= 0 {
		return 0, false
	}
	return port, true
}

// removeState forgets both files; a missing one is fine.
func removeState(root string) {
	_ = os.Remove(filepath.Join(root, stateFile))
	_ = os.Remove(filepath.Join(root, portFile))
}

// gatewayURL is the base URL a client points its model traffic at.
func gatewayURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

// serve runs the foreground gateway until its signal or its context ends: bind, write the state,
// print the one line a user or a script reads, serve, clean up.
func serve(ctx context.Context, run *runIO, config Config) int {
	root, err := resolveHome(run.env)
	if err != nil {
		run.errf("gateway: %v", err)
		return 1
	}
	listener, err := listenPort()
	if err != nil {
		run.errf("gateway: %v", err)
		return 1
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := writeState(root, port); err != nil {
		run.errf("gateway: %v", err)
		_ = listener.Close()
		return 1
	}
	defer removeState(root)

	gateway := NewServer(config, recordFor(run.env), nil)
	httpServer := &http.Server{Handler: gateway, ReadHeaderTimeout: 30 * time.Second}
	served := make(chan error, 1)
	go func() { served <- httpServer.Serve(listener) }()

	run.outf("gateway: %s", gatewayURL(port))
	select {
	case <-run.signal:
	case <-ctx.Done():
	case err := <-served:
		if err != nil && err != http.ErrServerClosed {
			run.errf("gateway: %v", err)
			return 1
		}
		return 0
	}
	done, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(done)
	return 0
}

// recordFor wires the spool writer radar's own readers drain: one route line per request under
// `$RADAR_HOME/spool`, the same files the provider routers append to.
func recordFor(env domain.EnvLookup) func(domain.RouteEvent) {
	write := spool.CreateSpoolWriter(env, nil, nil)
	return func(event domain.RouteEvent) { write(event) }
}
