package main

// Signing out through the real binary's hub: one browser's sign-out ends its own session alone,
// and sign-out-everywhere ends every browser's, while the hub keeps serving the owner's token
// and records both acts in its audit log.

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// signinCode answers the one-time code an `open --print` link carries.
func signinCode(t *testing.T, printed string) string {
	t.Helper()
	match := regexp.MustCompile(`\?code=(\S+)`).FindStringSubmatch(strings.TrimSpace(printed))
	if match == nil {
		t.Fatalf("open --print printed %q, want a link with ?code=…", printed)
	}
	return match[1]
}

func TestE2ESignOut(t *testing.T) {
	// a short root: the homes and sockets under it stay inside the unix path bounds
	root, err := os.MkdirTemp("", "hr-signout-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	hubHome := filepath.Join(root, "huddle-home")
	hubPort := ports.Free(t)
	hubURL := fmt.Sprintf("http://127.0.0.1:%d", hubPort)
	env := isoenv.Lookup(isoenv.Environ(t, filepath.Join(root, "user"),
		"RADAR_HOME="+filepath.Join(root, "radar"),
		"HUDDLE_HOME="+hubHome,
		"HUDDLE_DATA="+filepath.Join(root, "huddle-data"),
		"HUDDLE_ROUTER_HOME="+filepath.Join(root, "router"),
		"HUDDLE_PORT="+strconv.Itoa(hubPort),
	))
	owner := argvLayer(env, map[string]string{
		"HUDDLE_SESSION": "owner", "HUDDLE_URL": hubURL, "HUDDLE_TOKEN": "signout-e2e-root",
	})
	proj := filepath.Join(root, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _, _ = runBin(t, huddleBin, owner, proj, "down")
		pollFor(5*time.Second, func() bool { return !argvHealthy(hubURL) })
	})

	if code, out, errOut := runBin(t, huddleBin, owner, proj, "up"); code != 0 {
		t.Fatalf("huddle up = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	until(t, 20*time.Second, func() bool { return argvHealthy(hubURL) }, "the hub to answer its health path")

	// two browsers sign in, each through its own one-time link; the jar follows the redemption's
	// redirect, and the read after it proves the session is live
	signedIn := func(name, printed string) *browserClient {
		t.Helper()
		jar := newBrowser(hubURL)
		if got, _ := jar.get(t, "/?code="+signinCode(t, printed)); got != 200 {
			t.Fatalf("%s's link redemption landed on %d, want the dashboard", name, got)
		}
		if got, _ := jar.get(t, "/api/devices"); got != 200 {
			t.Fatalf("%s's first read = %d, want the session live", name, got)
		}
		return jar
	}
	code, printed, errOut := runBin(t, huddleBin, owner, proj, "open", "--print")
	if code != 0 {
		t.Fatalf("open --print = %d\nstdout:\n%s\nstderr:\n%s", code, printed, errOut)
	}
	desktop := signedIn("the desktop", printed)
	code, printed, errOut = runBin(t, huddleBin, owner, proj, "open", "--print")
	if code != 0 {
		t.Fatalf("the second open --print = %d\nstdout:\n%s\nstderr:\n%s", code, printed, errOut)
	}
	laptop := signedIn("the laptop", printed)

	// the desktop signs out; only its own session ends
	if got, body := desktop.post(t, "/api/signout", "{}"); got != 200 || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("the desktop's sign-out = (%d, %s), want 200 ok true", got, body)
	}
	if got, body := desktop.get(t, "/api/devices"); got != 401 || !strings.Contains(body, `"signin":true`) {
		t.Errorf("the desktop's replay = (%d, %s), want 401 naming the sign-in", got, body)
	}
	if got, _ := laptop.get(t, "/api/devices"); got != 200 {
		t.Errorf("the laptop's read after the sign-out = %d, want it still signed in", got)
	}

	// sign out everywhere ends the laptop's session too
	if got, body := laptop.post(t, "/api/signout/all", "{}"); got != 200 || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("sign out everywhere = (%d, %s), want 200 ok true", got, body)
	}
	if got, body := laptop.get(t, "/api/devices"); got != 401 || !strings.Contains(body, `"signin":true`) {
		t.Errorf("the laptop's replay = (%d, %s), want 401 naming the sign-in", got, body)
	}

	// the owner's token still answers, and both acts sit in the audit log
	request, err := http.NewRequest(http.MethodGet, hubURL+"/api/audit", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("x-huddle-token", "signout-e2e-root")
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("read the audit log: %v", err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 {
		t.Fatalf("/api/audit = %d: %s", response.StatusCode, raw)
	}
	for _, action := range []string{`"action":"signout"`, `"action":"signout.all"`} {
		if !strings.Contains(string(raw), action) {
			t.Errorf("the audit log holds no %s row: %s", action, raw)
		}
	}
}
