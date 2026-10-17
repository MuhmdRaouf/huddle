package cli

// The role words (SPEC 3): `huddle role` writes the machine's role into the home's config.toml.
// Solo, the default, is one machine on loopback. Hub adds the TLS listener on hub.listen for the
// owner's paired devices and satellites, under the name hub.host. Satellite serves its own
// sessions on loopback and reaches the hub at hub.url. The words run on the machine itself, in a
// terminal the owner holds, so no passkey step-up guards them: whoever owns the terminal owns the
// config. A restart (huddle up, or the service) takes the role up.

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"strconv"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// hubNameRe is the shape a hub's own name takes: a domain-style word, never a bare address — a
// passkey's relying party is a name (SPEC 7.2).
var hubNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*[A-Za-z0-9]$`)

// roleUsage is the one line every wrong shape answers.
const roleUsage = "usage: huddle role solo | hub --listen <addr:port> --host <name> | satellite --hub https://<host>:<port>"

// Role writes the machine's role into config.toml, keeping every other key the file holds.
func Role(ctx context.Context, w *World, argv []string, stdout, stderr io.Writer) int {
	root := Home(ctx, w)
	if root == "" {
		fmt.Fprintln(stderr, "huddle: no home: set HOME or HUDDLE_HOME")
		return 1
	}
	opt, pos := parseFlags(argv)
	if len(pos) > 0 && pos[0] == "role" { // the whole line reaches here, word and all
		pos = pos[1:]
	}
	if len(pos) != 1 {
		fmt.Fprintln(stderr, roleUsage)
		return 2
	}
	cfg, err := home.LoadConfig(home.Home{Root: root})
	if err != nil {
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	switch pos[0] {
	case "solo":
		cfg.Role = "solo"
	case "hub":
		listen, hasListen := opt.text("listen")
		host, hasHost := opt.text("host")
		if !hasListen || !hasHost {
			fmt.Fprintln(stderr, roleUsage)
			return 2
		}
		if _, port, err := net.SplitHostPort(listen); err != nil || port == "0" {
			fmt.Fprintf(stderr, "huddle: --listen %s: an address and a fixed port, as 127.0.0.2:8443\n", listen)
			return 2
		} else if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			fmt.Fprintf(stderr, "huddle: --listen %s: a port from 1 to 65535\n", listen)
			return 2
		}
		if net.ParseIP(host) != nil || !hubNameRe.MatchString(host) {
			fmt.Fprintf(stderr, "huddle: --host %s: the hub's name, not an address — the passkey's relying party is a name\n", host)
			return 2
		}
		cfg.Role = "hub"
		cfg.Hub.Listen = listen
		cfg.Hub.Host = host
	case "satellite":
		hubURL, hasURL := opt.text("hub")
		if !hasURL {
			fmt.Fprintln(stderr, roleUsage)
			return 2
		}
		parsed, err := url.Parse(hubURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			fmt.Fprintf(stderr, "huddle: --hub %s: the hub's https address, as https://hub.mine.test:8443\n", hubURL)
			return 2
		}
		cfg.Role = "satellite"
		cfg.Hub.URL = parsed.String()
	default:
		fmt.Fprintln(stderr, roleUsage)
		return 2
	}
	if err := home.SaveConfig(home.Home{Root: root}, cfg); err != nil {
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	roleLines(cfg, home.Home{Root: root}, stdout)
	return 0
}

// roleLines says what the machine now is and what the role still needs: one line of state, one
// of what the owner does next.
func roleLines(cfg home.Config, h home.Home, stdout io.Writer) {
	switch cfg.Role {
	case "hub":
		fmt.Fprintf(stdout, "role hub: paired devices and satellites answer on %s, as %s\n",
			cfg.Hub.Listen, cfg.Hub.Host)
		fmt.Fprintf(stdout, "restart huddle for the role to take effect; the listener serves the certificate in %s\n", h.TLS())
	case "satellite":
		fmt.Fprintf(stdout, "role satellite: this machine reaches the hub at %s and listens on loopback only\n", cfg.Hub.URL)
		fmt.Fprintln(stdout, "restart huddle for the role to take effect")
	default:
		fmt.Fprintln(stdout, "role solo: this machine's huddle answers loopback only")
		fmt.Fprintln(stdout, "restart huddle for the role to take effect")
	}
}
