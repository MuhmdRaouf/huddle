package cli

// The authenticator words: `huddle totp setup` is the headless hub's own path — the QR and the
// secret on the terminal, the confirming code read back, the recovery codes answered once — and
// `huddle totp off` removes the enrolment with every session a code minted. Both write the same
// hub database the server reads, so no server restart sits between them.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/mdp/qrterminal/v3"
)

// TOTPCommand runs the authenticator words: `huddle totp setup` and `huddle totp off`.
func TOTPCommand(ctx context.Context, w *World, argv []string, stdout, stderr io.Writer) int {
	root := Home(ctx, w)
	if root == "" {
		fmt.Fprintln(stderr, "huddle: no home: set HOME or HUDDLE_HOME")
		return 1
	}
	sub := ""
	if len(argv) > 1 {
		sub = argv[1]
	}
	switch sub {
	case "setup":
		return totpSetup(ctx, w, root, argv[2:], stdout, stderr)
	case "off":
		return totpOff(ctx, root, stdout, stderr)
	}
	fmt.Fprintln(stderr, "usage: huddle totp setup | huddle totp off")
	return 2
}

// totpCore opens the home's hub database and the authenticator core over it.
func totpCore(ctx context.Context, root string) (t *auth.TOTP, done func(), err error) {
	db, err := hub.Open(ctx, home.Home{Root: root}.HubDB())
	if err != nil {
		return nil, nil, fmt.Errorf("could not open the home's hub database: %w", err)
	}
	done = func() { _ = db.Close() }
	machine := "huddle"
	if host, hostErr := os.Hostname(); hostErr == nil && host != "" {
		machine = host
	}
	return auth.NewTOTP(db, auth.TOTPOptions{Machine: machine}), done, nil
}

// totpSetup enrols the authenticator from the terminal: the QR to scan, the secret for manual
// entry, then the code the app answers — three tries, and nothing stored until one works.
func totpSetup(ctx context.Context, w *World, root string, argv []string, stdout, stderr io.Writer) int {
	if len(argv) > 0 {
		fmt.Fprintln(stderr, "usage: huddle totp setup")
		return 2
	}
	t, done, err := totpCore(ctx, root)
	if err != nil {
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	defer done()
	if t.Enrolled(ctx) {
		fmt.Fprintln(stderr, "huddle: an authenticator is already enrolled: run `huddle totp off` first")
		return 2
	}
	begun, err := t.BeginEnrol(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	qrterminal.GenerateHalfBlock(begun.URI, qrterminal.L, stdout)
	fmt.Fprintf(stdout, "\nAuthenticator sign-in for this hub\n\n  %s\n\n  secret %s\n\n", begun.URI, secretSpaced(begun.Secret))
	fmt.Fprintf(stdout, "  Scan the code (or type the secret) in your authenticator app,\n  then enter the 6-digit code it shows.\n")
	reader := bufio.NewReader(w.stdin())
	for try := 0; try < 3; try++ {
		fmt.Fprint(stdout, "  code: ")
		line, readErr := reader.ReadString('\n')
		code := strings.TrimSpace(line)
		if code == "" && readErr != nil {
			break
		}
		recovery, err := t.FinishEnrol(ctx, "terminal", code)
		if err == nil {
			fmt.Fprintf(stdout, "\n  Recovery codes — each works once, and they show this one time:\n\n")
			for _, one := range recovery {
				fmt.Fprintf(stdout, "    %s\n", one)
			}
			fmt.Fprintf(stdout, "\n  Keep them somewhere safe: they are the way in when the app is not at hand.\n")
			return 0
		}
		fmt.Fprintf(stdout, "  that code did not work\n")
	}
	fmt.Fprintln(stderr, "huddle: no code that worked; nothing was stored")
	return 2
}

// totpOff removes the enrolment with every session a code minted.
func totpOff(ctx context.Context, root string, stdout, stderr io.Writer) int {
	t, done, err := totpCore(ctx, root)
	if err != nil {
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	defer done()
	removed, err := t.Remove(ctx, "terminal")
	if err != nil {
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	if !removed {
		fmt.Fprintln(stderr, "huddle: no authenticator is enrolled")
		return 4
	}
	fmt.Fprintln(stdout, "authenticator sign-in removed; its sessions ended")
	return 0
}

// secretSpaced groups the secret by four for the reading: "xxxx xxxx …", the way the apps take
// manual entry.
func secretSpaced(secret string) string {
	groups := make([]string, 0, len(secret)/4)
	for i := 0; i < len(secret); i += 4 {
		end := i + 4
		if end > len(secret) {
			end = len(secret)
		}
		groups = append(groups, secret[i:end])
	}
	return strings.Join(groups, " ")
}
