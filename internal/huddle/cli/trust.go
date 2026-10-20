package cli

// `huddle trust` and `huddle untrust`: the words that put huddle's local CA root into this
// machine's trust stores (the OS's and Firefox's) and take it out again, so browsers accept the
// hub's https address on a local name. Trust is never run automatically — not by setup, not by
// the server — because it writes outside the home and the OS asks for the password itself. Both
// words are idempotent: the root is verified against the system pool first, so a second run
// says so and touches nothing. `huddle trust --phone` instead serves the same root to a phone
// over plain HTTP for ten minutes: the phone needs the root before it can open the hub at all,
// and installs it by hand from the page the QR opens.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/phone"
	"github.com/MuhmdRaouf/huddle/internal/pki"
	"github.com/mdp/qrterminal/v3"
)

// trustUsage is the one line every wrong shape answers.
const trustUsage = "usage: huddle trust [--phone] | untrust"

// Trust installs huddle's local CA root in this machine's trust stores, creating the CA when the
// home holds none; with --phone it serves the root to a phone instead, installing nothing here.
// Exit: 0 trusted (or already trusted, or the phone guide served out its time), 1 the store
// refused, the CA cannot serve or the guide's address cannot bind, 2 a wrong line.
func Trust(ctx context.Context, w *World, argv []string, stdout, stderr io.Writer) int {
	root := Home(ctx, w)
	if root == "" {
		fmt.Fprintln(stderr, "huddle: no home: set HOME or HUDDLE_HOME")
		return 1
	}
	opt, pos := parseFlags(argv)
	if len(pos) > 0 && pos[0] == "trust" { // the whole line reaches here, word and all
		pos = pos[1:]
	}
	if opt.has("phone") {
		if len(pos) != 0 || len(opt.order) != 1 {
			fmt.Fprintln(stderr, trustUsage)
			return 2
		}
		return trustPhone(ctx, w, root, stdout, stderr)
	}
	if len(pos) != 0 || len(opt.order) != 0 {
		fmt.Fprintln(stderr, trustUsage)
		return 2
	}
	authority := pki.New(home.Home{Root: root}.PKI(), pki.Options{})
	cert, err := authority.Root()
	if err != nil {
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	if pki.TrustedBy(w.systemPool(), cert) {
		fmt.Fprintln(stdout, "Huddle's local CA root is already trusted")
		return 0
	}
	fmt.Fprintln(stdout, "Installing Huddle's local CA root; your OS may ask for your password.")
	if err := w.trustStore().Install(cert); err != nil {
		fmt.Fprintf(stderr, "huddle: installing the root failed: %v\n", err)
		return 1
	}
	if err := authority.MarkTrusted(); err != nil {
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Huddle's local CA root is trusted; browsers accept https://<name> on this machine (root in %s)\n",
		authority.RootFile())
	return 0
}

// trustPhone serves the guide a phone trusts the root with: a small plain-HTTP helper on the
// network the phone shares with this machine, its QR and the root's fingerprint printed here.
// The path installs nothing on this machine — `huddle trust` without the flag does that — and
// ends when its ten minutes are over or the terminal interrupts it.
func trustPhone(ctx context.Context, w *World, root string, stdout, stderr io.Writer) int {
	cfg, err := home.LoadConfig(home.Home{Root: root})
	if err != nil {
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	cert, err := pki.New(home.Home{Root: root}.PKI(), pki.Options{}).Root()
	if err != nil {
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	hubURL := ""
	if cfg.Hub.Host != "" {
		hubURL = hubBaseURL(cfg.Hub.Host, cfg.Hub.Listen)
	}
	helper, err := phone.Start(ctx, phone.HelperOptions{
		Address: phone.BindHost(cfg.Hub.Listen, func() string { return w.lanAddress() }),
		Root:    cert,
		HubURL:  hubURL,
	})
	if err != nil {
		fmt.Fprintf(stderr, "huddle: could not start the phone guide: %v\n", err)
		return 1
	}
	link := helper.URL()
	qrterminal.GenerateHalfBlock(link, qrterminal.L, stdout)
	fmt.Fprintf(stdout, "\nTrust huddle's root on a phone\n\n  %s\n\n", link)
	fmt.Fprintf(stdout, "  root fingerprint %s\n", phone.Fingerprint(cert))
	fmt.Fprintln(stdout, "  the phone page shows the same fingerprint — compare before installing")
	fmt.Fprintln(stdout, "  serving for 10 minutes · installs nothing on this machine")
	select {
	case <-ctx.Done():
		helper.Close()
	case <-helper.Done():
		fmt.Fprintln(stdout, "The 10 minutes are over — run huddle trust --phone again for another one.")
	}
	return 0
}

// Untrust removes huddle's local CA root from this machine's trust stores, and the marker that
// says huddle put it there; a home with no CA, or a root nothing vouches for, answers that
// nothing is trusted. Exit: 0 removed (or nothing to remove), 1 the store refused or the CA
// cannot serve, 2 a wrong line.
func Untrust(ctx context.Context, w *World, argv []string, stdout, stderr io.Writer) int {
	root := Home(ctx, w)
	if root == "" {
		fmt.Fprintln(stderr, "huddle: no home: set HOME or HUDDLE_HOME")
		return 1
	}
	opt, pos := parseFlags(argv)
	if len(pos) > 0 && pos[0] == "untrust" { // the whole line reaches here, word and all
		pos = pos[1:]
	}
	if len(pos) != 0 || len(opt.order) != 0 {
		fmt.Fprintln(stderr, trustUsage)
		return 2
	}
	authority := pki.New(home.Home{Root: root}.PKI(), pki.Options{})
	cert, err := authority.ReadRoot()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(stdout, "Huddle's local CA root is not installed")
			return 0
		}
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	// neither the system pool nor our own marker vouches for an install: nothing to remove
	if !pki.TrustedBy(w.systemPool(), cert) && !authority.TrustedByUs() {
		fmt.Fprintln(stdout, "Huddle's local CA root is not trusted")
		return 0
	}
	if err := w.trustStore().Uninstall(cert); err != nil {
		fmt.Fprintf(stderr, "huddle: removing the root failed: %v\n", err)
		return 1
	}
	if err := authority.UnmarkTrusted(); err != nil {
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Huddle's local CA root removed from this machine's trust stores")
	return 0
}
