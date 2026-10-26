// The port one user's huddle answers on. The first server takes 18860; when another program
// holds it, the next free port in 18861 to 18869, then one the OS picks. The choice is saved in
// the home's config.toml (port, port_auto = true), so later starts, the join commands and the
// dashboard keep one address. A port the owner set is never moved, only refused when a stranger
// holds it; a port a huddle already answers its health path on is reused, not skipped.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// PortFirst and PortLast bound the ports huddle picks for itself; kanban's boards sit just below
// (18800 to 18849) and huddle-router below them (18786).
const PortFirst = 18860

const PortLast = 18869

// bindable answers whether a server could bind port on host right now.
func bindable(port int, host string) bool {
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

// PortFree answers whether nothing listens on port, on loopback or on every interface (binding
// is the only honest check).
func PortFree(port int) bool {
	return bindable(port, "127.0.0.1") && bindable(port, "0.0.0.0")
}

// PortPrefs is the state ChoosePort decides from: Port is the port saved in the home's config (or
// the one someone chose), Auto whether huddle picked that one itself.
type PortPrefs struct {
	Port int
	Auto bool
}

// PortProbe says whether a huddle answers its health path on port: a busy port one of our servers
// holds is reused, a busy port anything else holds is moved past (or refused, when the owner chose
// it).
type PortProbe func(ctx context.Context, port int) bool

// ChosenPort is ChoosePort's answer.
type ChosenPort struct {
	Port int
	// Auto says huddle picked this port itself, so it may move when another program takes it.
	Auto bool
	// Huddle says a huddle already answers on this port: reuse its server, start nothing.
	Huddle bool
}

// PortTakenError names a port the owner chose that another program holds.
type PortTakenError struct{ Port int }

func (e PortTakenError) Error() string {
	return fmt.Sprintf("port %d is taken by another program, not Huddle: free it, or choose another port with huddle setup --port <n>", e.Port)
}

// ChoosePort answers the port the next server start uses. A saved or chosen port comes first:
// still free it is kept; held by a huddle it is reused; held by another program it moves (Auto) or
// is refused (a choice). Then a huddle already answering somewhere in the range is reused (one
// server per home, never a second beside the first), else the walk takes the first free port in
// 18860 to 18869, else the OS picks.
func ChoosePort(ctx context.Context, cfg PortPrefs, probe PortProbe) (ChosenPort, error) {
	huddle := func(port int) bool {
		if probe == nil {
			return false
		}
		if err := ctx.Err(); err != nil {
			return false
		}
		return probe(ctx, port)
	}
	if cfg.Port > 0 {
		switch {
		case PortFree(cfg.Port):
			return ChosenPort{Port: cfg.Port, Auto: cfg.Auto}, nil
		case huddle(cfg.Port):
			return ChosenPort{Port: cfg.Port, Huddle: true}, nil
		case !cfg.Auto:
			return ChosenPort{}, PortTakenError{Port: cfg.Port}
		}
	}
	for port := PortFirst; port <= PortLast; port++ {
		if huddle(port) {
			return ChosenPort{Port: port, Huddle: true}, nil
		}
	}
	for port := PortFirst; port <= PortLast; port++ {
		if PortFree(port) {
			return ChosenPort{Port: port, Auto: true}, nil
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return ChosenPort{}, fmt.Errorf("no free port: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return ChosenPort{Port: port, Auto: true}, nil
}

// chooseAndServePort is the port a server start with no PORT in its environment takes: the same
// choice huddle up makes — the home's saved port first (an owner's kept, an auto one moved when
// a stranger took it), else the first free port in the range — saved back the same way, so a
// service restart comes back on the same address.
func chooseAndServePort(ctx context.Context, o Options) (int, error) {
	prefs := PortPrefs{}
	if o.Home != "" {
		if cfg, err := home.LoadConfig(home.Home{Root: o.Home}); err == nil {
			prefs = PortPrefs{Port: cfg.Port, Auto: cfg.PortAuto}
		}
	}
	chosen, err := ChoosePort(ctx, prefs, func(ctx context.Context, port int) bool {
		return oursAnswers(ctx, o, port)
	})
	if err != nil {
		return 0, err
	}
	if !chosen.Huddle && o.Home != "" {
		_, _ = home.SavePort(home.Home{Root: o.Home}, chosen.Port, chosen.Auto)
	}
	return chosen.Port, nil
}

// oursAnswers says whether the huddle answering on port serves this start's home: its health
// path names the home it serves, so a server of another home (or another program) is never
// mistaken for ours.
func oursAnswers(ctx context.Context, o Options, port int) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/health", port), nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 750 * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}
	var answer struct {
		Home string `json:"home"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return false
	}
	return answer.Home != "" && answer.Home == o.Home
}
