package cli

// The pairing words (SPEC 7.3): `huddle pair` mints one one-use link from a trusted machine —
// the terminal writes the pairing into the same hub database the server reads, so no credential
// is asked for — and `huddle devices` lists what paired and removes it. Removal works whether
// the server runs or not: the sessions die with the device row, and the next request carrying
// one of its cookies is refused.

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/huddle/phone"
	"github.com/mdp/qrterminal/v3"
)

// hubBaseURL answers the hub's public https base: the name alone when the hub listener serves
// 443, the port appended when it serves anything else — a printed link without the port opens
// nothing. A listen whose port cannot be read leaves the URL bare.
func hubBaseURL(host, listen string) string {
	return phone.HubURL(host, listen)
}

// Pair mints one pairing link and prints it: a QR code to scan, the link itself, the code both
// sides compare, and the name the device pairs as.
func Pair(ctx context.Context, w *World, argv []string, stdout, stderr io.Writer) int {
	root := Home(ctx, w)
	if root == "" {
		fmt.Fprintln(stderr, "huddle: no home: set HOME or HUDDLE_HOME")
		return 1
	}
	name := "phone"
	opt, _ := parseFlags(argv)
	if v, ok := opt.text("name"); ok {
		name = v
	}
	if !auth.DeviceNameRe.MatchString(name) {
		fmt.Fprintf(stderr, "huddle: --name %s: a letter or digit first, up to 32 of them, spaces and dashes\n", name)
		return 2
	}
	if opt.has("satellite") {
		return pairSatellite(ctx, w, root, stdout, stderr)
	}
	cfg, err := home.LoadConfig(home.Home{Root: root})
	if err != nil {
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	base, port := "", 0
	if cfg.Hub.Host == "" {
		port = pairPort(ctx, w, root)
		if port <= 0 {
			fmt.Fprintln(stderr, "huddle: no server to pair with: run `huddle up` first (or set hub.host in config.toml)")
			return 6
		}
		// a passkey needs a domain relying party: browsers refuse one on a bare IP address
		base = fmt.Sprintf("http://localhost:%d", port)
	} else {
		base = hubBaseURL(cfg.Hub.Host, cfg.Hub.Listen)
	}
	p, done, err := pairCore(ctx, root, cfg.Hub.Host, port)
	if err != nil {
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	defer done()
	invite, err := p.Mint(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "huddle: could not mint the pairing link: %v\n", err)
		return 1
	}
	link := fmt.Sprintf("%s/pair#%s", base, invite.Token)
	qrterminal.GenerateHalfBlock(link, qrterminal.L, stdout)
	fmt.Fprintf(stdout, "\nPair this device as %s\n\n  %s\n\n  code %s · expires in 5 minutes · one use\n", name, link, invite.Code)
	fmt.Fprintf(stdout, "  open the link (or scan the code), check the code matches, create the passkey\n")
	return 0
}

// Devices lists the paired devices, or removes one: `huddle devices rm <name>` revokes it and
// every session it holds at once.
func Devices(ctx context.Context, w *World, argv []string, stdout, stderr io.Writer) int {
	root := Home(ctx, w)
	if root == "" {
		fmt.Fprintln(stderr, "huddle: no home: set HOME or HUDDLE_HOME")
		return 1
	}
	_, pos := parseFlags(argv)
	if len(pos) > 0 && pos[0] == "devices" { // the whole line reaches here, word and all
		pos = pos[1:]
	}
	if len(pos) > 0 && pos[0] == "rm" {
		if len(pos) != 2 {
			fmt.Fprintln(stderr, "usage: huddle devices rm <name>")
			return 2
		}
		cfg, err := home.LoadConfig(home.Home{Root: root})
		if err != nil {
			fmt.Fprintf(stderr, "huddle: %v\n", err)
			return 1
		}
		p, done, err := pairCore(ctx, root, cfg.Hub.Host, 0)
		if err != nil {
			fmt.Fprintf(stderr, "huddle: %v\n", err)
			return 1
		}
		defer done()
		if !p.Remove(ctx, pos[1]) {
			fmt.Fprintf(stderr, "huddle: no device named %s\n", pos[1])
			return 4
		}
		fmt.Fprintf(stdout, "%s removed; its sessions ended\n", pos[1])
		return 0
	}
	cfg, err := home.LoadConfig(home.Home{Root: root})
	if err != nil {
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	p, done, err := pairCore(ctx, root, cfg.Hub.Host, 0)
	if err != nil {
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	defer done()
	list, err := p.Devices(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	if len(list) == 0 {
		fmt.Fprintln(stdout, "no devices paired; `huddle pair` prints a link to pair one")
		return 0
	}
	fmt.Fprintln(stdout, "NAME\tADDED\tLAST SEEN")
	for _, d := range list {
		fmt.Fprintf(stdout, "%s\t%s\t%s\n", d.Name, whenOf(d.Added), whenOf(d.Seen))
	}
	return 0
}

// pairSatellite mints a satellite invite (SPEC 6.7: one use, 10 minutes) and prints the line
// the other machine runs. The invite names no device — the other machine's own join names
// itself — and lives in the same hub database the server spends it from, so no server restart
// is needed.
func pairSatellite(ctx context.Context, w *World, root string, stdout, stderr io.Writer) int {
	cfg, err := home.LoadConfig(home.Home{Root: root})
	if err != nil {
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	url := ""
	if cfg.Hub.Host != "" {
		url = hubBaseURL(cfg.Hub.Host, cfg.Hub.Listen)
	} else if port := pairPort(ctx, w, root); port > 0 {
		url = fmt.Sprintf("http://localhost:%d", port)
	} else {
		fmt.Fprintln(stderr, "huddle: no server to join this hub: run `huddle up` first (or set hub.host in config.toml)")
		return 6
	}
	db, err := hub.Open(ctx, home.Home{Root: root}.HubDB())
	if err != nil {
		fmt.Fprintf(stderr, "huddle: could not open the home's hub database: %v\n", err)
		return 1
	}
	defer func() { _ = db.Close() }()
	invite, err := db.MintSatelliteInvite(ctx, time.Now().UnixMilli())
	if err != nil {
		fmt.Fprintf(stderr, "huddle: could not mint the satellite invite: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Satellite invite · one use · expires in 10 minutes\n\n  huddle join-hub %s %s\n\n  Run that line on the other machine: it becomes that machine's credential,\n  and this hub then carries its sessions on the Now list.\n", url, invite)
	return 0
}

// pairCore opens the home's hub database and the pairing core over it; done closes the database.
// hubHost and port shape the relying party the same way the server does, so the minting side and
// the verifying side speak of one relying party.
func pairCore(ctx context.Context, root, hubHost string, port int) (p *auth.Pairing, done func(), err error) {
	db, err := hub.Open(ctx, home.Home{Root: root}.HubDB())
	if err != nil {
		return nil, nil, fmt.Errorf("could not open the home's hub database: %w", err)
	}
	done = func() { _ = db.Close() }
	rpID, origins := "localhost", []string{"http://localhost"}
	if port > 0 {
		origins = []string{
			fmt.Sprintf("http://localhost:%d", port),
			fmt.Sprintf("http://127.0.0.1:%d", port),
		}
	}
	if hubHost != "" {
		rpID = hubHost
		origins = append([]string{"https://" + hubHost}, origins...)
	}
	p, err = auth.NewPairing(db, auth.PairOptions{RPID: rpID, Origins: origins})
	if err != nil {
		done()
		return nil, nil, err
	}
	return p, done, nil
}

// pairPort answers the port the link points at: the saved one, else the running server's.
func pairPort(ctx context.Context, w *World, root string) int {
	if choice, has := SavedPort(root); has {
		return choice.Port
	}
	if pid := PidOf(ctx, w); pid > 0 {
		return ListeningPort(ctx, w, pid)
	}
	return 0
}

// whenOf trims one of the device's moments to the minute it happened.
func whenOf(iso string) string {
	if len(iso) >= 16 {
		return iso[:16] + "Z"
	}
	return iso
}
