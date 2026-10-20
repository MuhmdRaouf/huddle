// The gateway words: the huddle-router daemon serves the gateway under /gateway, so every
// word here only reports where. start, the bare word and status answer the router's health
// probe and name the base URL a harness points its model traffic at; stop is words, because
// the gateway ends with the router. Exit codes: 0 ok, 2 usage, 6 the router is not running.
package gateway

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// usage is the one line every misuse prints.
const usage = "usage: huddle gateway [start|stop|status]"

// RunOptions carries what a real run reads from the world; the tests fake the probe.
type RunOptions struct {
	Env domain.EnvLookup
	// FetchHealth answers whether one url answers 200; nil is the real bounded probe.
	FetchHealth func(url string) bool
}

// Run runs one gateway command line and answers the process exit code.
func Run(args []string, stdout, stderr io.Writer, opts RunOptions) int {
	env := opts.Env
	if env == nil {
		env = domain.EnvLookup{}
	}
	fetch := opts.FetchHealth
	if fetch == nil {
		fetch = fetchHealth
	}
	command := ""
	if len(args) > 0 {
		command = args[0]
	}
	switch command {
	case "", "start", "status":
		port, err := router.PortOf(env)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		if !fetch(routerHealthURL(port)) {
			fmt.Fprintln(stderr, "gateway: huddle-router is not running; huddle router start starts it")
			return 6
		}
		fmt.Fprintf(stdout, "gateway: served by huddle-router at %s\n", gatewayAddress(port))
		return 0
	case "stop":
		fmt.Fprintln(stdout, "gateway: served by huddle-router; it stops with the router")
		return 0
	case "help", "--help", "-h":
		fmt.Fprintln(stdout, usage)
		fmt.Fprintln(stdout, "the huddle-router daemon serves the gateway under /gateway; huddle router start starts it")
		return 0
	default:
		fmt.Fprintf(stderr, "gateway: unknown command: %s\n", command)
		fmt.Fprintln(stderr, usage)
		return 2
	}
}

// gatewayAddress is the base URL a harness points its model traffic at.
func gatewayAddress(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", port, router.GatewayPrefix)
}

// routerHealthURL is the router health path the words probe.
func routerHealthURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", port, router.MultiHealthPath)
}

// fetchHealth answers whether the url answers 200, with a two-second budget.
func fetchHealth(url string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		return false
	}
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode == http.StatusOK
}
