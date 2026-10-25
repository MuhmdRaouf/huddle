package cli

// The domain word names the hub for the reverse-proxy flow in one step: `huddle domain <name>`
// writes the same role `huddle role hub --host <name>` writes (no listener of its own, the port
// pinned) and prints the Caddy site block the owner pastes; huddle never writes Caddy's config.
// `domain off` is `role solo`. `domain --check` is the one network step, run only when asked.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// domainUsage is the one line every wrong shape answers.
const domainUsage = "usage: huddle domain [<name> | off | --check]"

// domainProbeWait bounds the proxy probe.
const domainProbeWait = 3 * time.Second

// Domain sets, shows, clears or checks the name the hub answers as behind a TLS reverse proxy.
func Domain(ctx context.Context, w *World, argv []string, stdout, stderr io.Writer) int {
	root := Home(ctx, w)
	if root == "" {
		fmt.Fprintln(stderr, "huddle: no home: set HOME or HUDDLE_HOME")
		return 1
	}
	opt, pos := parseFlags(argv)
	if len(pos) > 0 && pos[0] == "domain" { // the whole line reaches here, word and all
		pos = pos[1:]
	}
	check, hasCheck := opt.value("check")
	switch {
	case hasCheck && len(opt.order) == 1 && !check.isText && len(pos) == 0:
		return domainCheck(ctx, w, root, stdout, stderr)
	case len(opt.order) > 0 || len(pos) > 1:
		fmt.Fprintln(stderr, domainUsage)
		return 2
	case len(pos) == 0:
		cfg, err := home.LoadConfig(home.Home{Root: root})
		if err != nil {
			fmt.Fprintf(stderr, "huddle: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, domainState(cfg))
		return 0
	case pos[0] == "off":
		cfg, code := setRole(w, root, "solo", nil, stderr)
		if code != 0 {
			return code
		}
		roleLines(cfg, home.Home{Root: root}, w, stdout)
		return 0
	}
	hostFlag, _ := parseFlags([]string{"--host", pos[0]})
	cfg, code := setRole(w, root, "hub", hostFlag, stderr)
	if code != 0 {
		return code
	}
	fmt.Fprintln(stdout, proxyStateLine(cfg))
	fmt.Fprintln(stdout, "paste this site block into your Caddyfile, then reload Caddy:")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, caddyBlock(cfg.Hub.Host, cfg.Port))
	fmt.Fprintln(stdout, tlsLine(cfg.Hub.Host))
	fmt.Fprintln(stdout, proxyRestartLine(cfg))
	fmt.Fprintln(stdout, "then `huddle domain --check` says whether the proxy reaches this hub")
	return 0
}

// domainState is what the bare word says: the hub's name and how it is served, or loopback alone.
func domainState(cfg home.Config) string {
	if cfg.Role != "hub" || cfg.Hub.Host == "" {
		return "localhost only"
	}
	if cfg.Hub.Listen != "" {
		return fmt.Sprintf("%s: huddle's own listener on %s", cfg.Hub.Host, cfg.Hub.Listen)
	}
	return fmt.Sprintf("%s: behind a TLS reverse proxy that forwards to %s", cfg.Hub.Host, Local(cfg.Port))
}

// caddyBlock is the Caddyfile site block for the hub's name: TLS at Caddy, every request forwarded
// to the hub's pinned loopback port with the scheme the browser used, so the sign-in cookie
// carries Secure. A private name takes Caddy's own CA.
func caddyBlock(host string, port int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s {\n", host)
	if privateName(host) {
		b.WriteString("\ttls internal\n")
	}
	fmt.Fprintf(&b, "\treverse_proxy 127.0.0.1:%d {\n", port)
	b.WriteString("\t\theader_up X-Forwarded-Proto https\n")
	b.WriteString("\t}\n")
	b.WriteString("}\n")
	return b.String()
}

// tlsLine says where the proxy's certificate comes from.
func tlsLine(host string) string {
	if privateName(host) {
		return fmt.Sprintf("tls: %s is a private name, so Caddy signs it with its own CA (tls internal); "+
			"run `caddy trust` once so this machine's browsers accept it", host)
	}
	return fmt.Sprintf("tls: Caddy gets the certificate for %s from Let's Encrypt on its own; when the name has "+
		"no public A record, use a DNS challenge instead (tls { dns <provider> … } with Caddy's module for your DNS)", host)
}

// privateName answers whether no public CA issues for the name: one without a dot, or one under
// a reserved suffix (RFC 2606, RFC 6761, RFC 8375) or one home and office networks use.
func privateName(host string) bool {
	name := strings.ToLower(host)
	if strings.HasSuffix(name, ".home.arpa") {
		return true
	}
	dot := strings.LastIndex(name, ".")
	if dot < 0 {
		return true
	}
	switch name[dot+1:] {
	case "test", "example", "invalid", "localhost", "local", "internal", "lan", "home", "corp", "intranet", "private":
		return true
	}
	return false
}

// domainCheck asks the proxy for the hub's health at the saved name: the answer names this home
// when the proxy forwards to this hub.
func domainCheck(ctx context.Context, w *World, root string, stdout, stderr io.Writer) int {
	cfg, err := home.LoadConfig(home.Home{Root: root})
	if err != nil {
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	if cfg.Role != "hub" || cfg.Hub.Host == "" {
		fmt.Fprintln(stderr, "huddle: no domain is set (localhost only): run `huddle domain <name>` first")
		return 2
	}
	base := hubBaseURL(cfg.Hub.Host, cfg.Hub.Listen)
	probe, cancel := context.WithTimeout(ctx, domainProbeWait)
	defer cancel()
	req, err := http.NewRequestWithContext(probe, http.MethodGet, base+"/health", nil)
	if err != nil {
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	resp, err := w.probeClient().Do(req)
	if err != nil {
		fmt.Fprintf(stdout, "%s does not answer: %v\n", base, err)
		return 1
	}
	defer resp.Body.Close()
	var health hubHealth
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&health) != nil || !health.OK {
		fmt.Fprintf(stdout, "%s answers %d, but not as a huddle hub: is huddle up, and does the block forward to %s?\n",
			base, resp.StatusCode, Local(cfg.Port))
		return 1
	}
	if health.Home != root {
		fmt.Fprintf(stdout, "%s answers, but forwards to another huddle (home %s): point the block at %s\n",
			base, health.Home, Local(cfg.Port))
		return 1
	}
	fmt.Fprintf(stdout, "%s answers and forwards to this hub (huddle %s)\n", base, health.Version)
	return 0
}
