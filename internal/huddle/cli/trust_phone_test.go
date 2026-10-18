package cli

// `huddle trust --phone` against a fake world: it starts the guide on loopback, prints its QR
// link and the root's fingerprint, mints the CA when the home holds none, and installs nothing —
// the fake store never hears of it. The word ends on its context, like a Ctrl-C does.

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// phoneWorld is one trust --phone run's world: a temp home, a fake store that must stay silent,
// and a LAN walk pinned to loopback so no test ever binds a real interface.
func phoneWorld(t *testing.T) *trustWorld {
	t.Helper()
	world := newTrustWorld(t)
	world.w.LanAddress = func() string { return "127.0.0.1" }
	return world
}

// trustPhoneRun starts `huddle trust --phone` on ctx, answering its exit-code channel and both
// streams as thread-safe logs the run keeps printing into. The caller defers the context's
// cancel, so a failure above the run's own end still ends it.
func trustPhoneRun(t *testing.T, world *trustWorld, ctx context.Context, argv ...string) (chan int, *mcpLog, *mcpLog) {
	t.Helper()
	out, errOut := &mcpLog{}, &mcpLog{}
	code := make(chan int, 1)
	go func() { code <- Trust(ctx, world.w, argv, out, errOut) }()
	return code, out, errOut
}

// waitCode waits for a run's exit code, bounded.
func waitCode(t *testing.T, code chan int) int {
	t.Helper()
	select {
	case c := <-code:
		return c
	case <-time.After(15 * time.Second):
		t.Fatal("the run never ended")
		return -1
	}
}

// waitUntilLink polls the run's output until it carries the guide's link.
func waitUntilLink(t *testing.T, out *mcpLog) string {
	t.Helper()
	re := regexp.MustCompile(`http://127\.0\.0\.1:\d+/`)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if link := re.FindString(out.text()); link != "" {
			return link
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("trust --phone printed no loopback link:\n%s", out.text())
	return ""
}

// phoneGet answers one GET over the guide the word started (status 0 when the connection is
// refused).
func phoneGet(t *testing.T, url, accept string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	response, err := client.Do(request)
	if err != nil {
		return 0, ""
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return response.StatusCode, string(body)
}

// sha256Paired answers the DER's SHA-256 as paired hex, beside the phone package's spelling.
func sha256Paired(der []byte) string {
	sum := sha256.Sum256(der)
	pairs := make([]string, len(sum))
	for i, b := range sum {
		pairs[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(pairs, ":")
}

func TestTrustPhoneServesTheGuidePrintsTheLinkAndInstallsNothing(t *testing.T) {
	world := phoneWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	code, out, errOut := trustPhoneRun(t, world, ctx, "trust", "--phone")

	link := waitUntilLink(t, out)

	// the run mints the CA the guide serves, and prints the root's fingerprint beside the QR
	wantFp := sha256Paired(trustRootOf(t, world).Raw)
	fp := regexp.MustCompile(`fingerprint ([0-9A-F:]{95})`).FindStringSubmatch(out.text())
	if fp == nil {
		t.Fatalf("the run prints no fingerprint:\n%s", out.text())
	}
	if fp[1] != wantFp {
		t.Errorf("the printed fingerprint is %s, want the root's %s", fp[1], wantFp)
	}
	if !strings.Contains(out.text(), "installs nothing") {
		t.Errorf("the run does not say it installs nothing:\n%s", out.text())
	}

	// the guide answers the phone's paths, and the fingerprint it shows is the printed one
	if status, _ := phoneGet(t, link+"ca.crt", ""); status != 200 {
		t.Errorf("the guide's /ca.crt answered %d, want 200", status)
	}
	if _, page := phoneGet(t, link, ""); !strings.Contains(page, fp[1]) {
		t.Error("the guide does not show the fingerprint the terminal printed")
	}
	if status, _ := phoneGet(t, link+"root.key", ""); status != 404 {
		t.Errorf("the guide answered /root.key with %d, want 404", status)
	}

	// the word ends on its context, exit 0, with the store never asked
	cancel()
	if c := waitCode(t, code); c != 0 {
		t.Errorf("trust --phone ended %d, want 0", c)
	}
	if errOut.text() != "" {
		t.Errorf("the run wrote to stderr:\n%s", errOut.text())
	}
	if world.store.installs != 0 {
		t.Errorf("the store was asked %d times, want never", world.store.installs)
	}
	if _, err := os.Stat(filepath.Join(world.home, "pki", "trusted")); !os.IsNotExist(err) {
		t.Error("the phone path wrote the trusted marker anyway")
	}
	if status, _ := phoneGet(t, link+"ca.crt", ""); status != 0 {
		t.Errorf("the guide answered %d after the word ended, want the connection refused", status)
	}
}

func TestTrustPhoneBindsTheAddressTheHubListenerUses(t *testing.T) {
	world := phoneWorld(t)
	if err := os.WriteFile(filepath.Join(world.home, "config.toml"),
		[]byte("role = \"hub\"\n[hub]\nlisten = \"127.0.0.1:0\"\nhost = \"hub.raouf.test\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	code, out, _ := trustPhoneRun(t, world, ctx, "trust", "--phone")
	link := waitUntilLink(t, out)
	// the hub's address is what the guide's check proves trust against
	if _, page := phoneGet(t, link, ""); !strings.Contains(page, "https://hub.raouf.test") {
		t.Error("the guide does not carry the hub's https address for the check")
	}
	cancel()
	waitCode(t, code)
}

func TestTrustPhoneRefusesEveryWrongShapeAndABrokenCA(t *testing.T) {
	for _, argv := range [][]string{
		{"trust", "extra", "--phone"},
		{"trust", "--phone", "--force"},
	} {
		world := phoneWorld(t)
		var out, errOut = &mcpLog{}, &mcpLog{}
		code := Trust(context.Background(), world.w, argv, out, errOut)
		if code != 2 || out.text() != "" || !strings.Contains(errOut.text(), "usage: huddle trust") {
			t.Fatalf("huddle %v = (%d, %q, %q), want 2 with the usage", argv, code, out.text(), errOut.text())
		}
	}
	// a broken CA is refused before anything binds
	world := phoneWorld(t)
	if code, _, _ := trustRun(t, world, "trust", "trust"); code != 0 {
		t.Fatal("the first trust failed")
	}
	if err := os.WriteFile(filepath.Join(world.home, "pki", "root.key"), []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut = &mcpLog{}, &mcpLog{}
	code := Trust(context.Background(), world.w, []string{"trust", "--phone"}, out, errOut)
	if code != 1 || out.text() != "" || !strings.Contains(errOut.text(), "cannot serve") {
		t.Fatalf("trust --phone over a broken CA = (%d, %q, %q), want 1 naming the CA",
			code, out.text(), errOut.text())
	}
}
