package main

// The phone path, end to end on an iOS Simulator: a temp hub behind a TLS proxy whose leaf a test
// root signs, the root trusted in the simulator's keychain, the one-time sign-in link opened in
// the Safari of a simulator the test creates and deletes, and the hub's audit log showing the
// code redeemed. The simulator shares this Mac's loopback, so the link names 127.0.0.1. It runs
// on a Mac with Xcode's simulators and skips anywhere else, and on CI, where a cold simulator
// boot does not fit the macOS job.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
	"github.com/MuhmdRaouf/huddle/internal/testkit/tlsproxy"
)

const (
	simulatorBootTimeout = 120 * time.Second
	simulatorOpenTimeout = 30 * time.Second
	signinSeenTimeout    = 30 * time.Second
)

func TestE2EPhoneSimulator(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("local only: a cold iOS Simulator boot does not fit the macOS CI job's 10 minutes")
	}
	if err := exec.Command("xcrun", "simctl", "help").Run(); err != nil {
		t.Skipf("no iOS Simulator here (xcrun simctl: %v)", err)
	}

	// a short root: the homes and sockets under it stay inside the unix path bounds
	root, err := os.MkdirTemp("", "hr-sim-")
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
		"HUDDLE_SESSION": "owner", "HUDDLE_URL": hubURL,
	})
	proj := filepath.Join(root, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _, _ = runBin(t, huddleBin, owner, proj, "down")
		if pollFor(5*time.Second, func() bool { return !argvHealthy(hubURL) }) {
			return
		}
		raw, err := os.ReadFile(filepath.Join(hubHome, "run", "huddle.pid"))
		if err != nil {
			return
		}
		if pid, _ := strconv.Atoi(strings.TrimSpace(string(raw))); pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGTERM)
			if !pollFor(3*time.Second, func() bool { return !argvHealthy(hubURL) }) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})

	if code, out, errOut := runBin(t, huddleBin, owner, proj, "up"); code != 0 {
		t.Fatalf("huddle up = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	until(t, 20*time.Second, func() bool { return argvHealthy(hubURL) }, "the hub to answer its health path")

	proxy := tlsproxy.Start(t, hubURL)

	code, printed, errOut := runBin(t, huddleBin, owner, proj, "open", "--print")
	if code != 0 {
		t.Fatalf("open --print = %d\nstdout:\n%s\nstderr:\n%s", code, printed, errOut)
	}
	signin := regexp.MustCompile(`\?code=(\S+)`).FindStringSubmatch(strings.TrimSpace(printed))
	if signin == nil {
		t.Fatalf("open --print printed %q, want a link with ?code=…", printed)
	}
	link := proxy.URL + "/?code=" + signin[1]

	// the owner key the hub leaves in its run directory reads the audit log on loopback
	key, err := os.ReadFile(filepath.Join(hubHome, "run", "owner.key"))
	if err != nil {
		t.Fatalf("the hub's owner key: %v", err)
	}
	rootToken := strings.TrimSpace(string(key))

	if n := signins(t, hubURL, rootToken); n != 0 {
		t.Fatalf("the audit log holds %d sign-ins before the simulator opened the link, want 0", n)
	}

	udid := throwawayIPhone(t)
	simctl(t, simulatorOpenTimeout, "keychain", udid, "add-root-cert", proxy.RootFile)
	simctl(t, simulatorOpenTimeout, "openurl", udid, link)
	until(t, signinSeenTimeout, func() bool { return signins(t, hubURL, rootToken) > 0 },
		"the hub's audit log to show the simulator's sign-in")

	response, err := proxy.Client.Get(proxy.URL + "/manifest.webmanifest")
	if err != nil {
		t.Fatalf("read the manifest through the proxy: %v", err)
	}
	defer response.Body.Close()
	var manifest struct {
		Display string `json:"display"`
	}
	if err := json.NewDecoder(response.Body).Decode(&manifest); err != nil || response.StatusCode != 200 {
		t.Fatalf("/manifest.webmanifest = %d, decode %v", response.StatusCode, err)
	}
	if manifest.Display != "standalone" {
		t.Fatalf("the manifest's display = %q, want standalone", manifest.Display)
	}
}

// signins counts the sign-in rows of the hub's audit log, read on loopback as the owner.
func signins(t *testing.T, hubURL, rootToken string) int {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, hubURL+"/api/audit", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("x-huddle-token", rootToken)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("read the audit log: %v", err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 {
		t.Fatalf("/api/audit = %d: %s", response.StatusCode, raw)
	}
	var page struct {
		Entries []struct {
			Action string `json:"action"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("decode the audit log %q: %v", raw, err)
	}
	n := 0
	for _, entry := range page.Entries {
		if entry.Action == "signin" {
			n++
		}
	}
	return n
}

// simRuntime is one runtime of `xcrun simctl list runtimes available -j`.
type simRuntime struct {
	Identifier     string `json:"identifier"`
	Version        string `json:"version"`
	Platform       string `json:"platform"`
	SupportedTypes []struct {
		Identifier    string `json:"identifier"`
		ProductFamily string `json:"productFamily"`
	} `json:"supportedDeviceTypes"`
}

// throwawayIPhone creates an iPhone simulator of its own (the newest iOS runtime, its first
// iPhone device type), boots it, and shuts it down and deletes it when the test ends, failed or
// not. The owner's simulators, booted or not, are never touched.
func throwawayIPhone(t *testing.T) string {
	t.Helper()
	var list struct {
		Runtimes []simRuntime `json:"runtimes"`
	}
	if err := json.Unmarshal([]byte(simctl(t, simulatorOpenTimeout, "list", "runtimes", "available", "-j")), &list); err != nil {
		t.Fatalf("decode simctl's runtime list: %v", err)
	}
	var runtime, deviceType string
	var newest []int
	for _, candidate := range list.Runtimes {
		if candidate.Platform != "iOS" {
			continue
		}
		version := versionParts(candidate.Version)
		if newest != nil && !versionAfter(version, newest) {
			continue
		}
		for _, kind := range candidate.SupportedTypes {
			if kind.ProductFamily == "iPhone" {
				runtime, deviceType, newest = candidate.Identifier, kind.Identifier, version
				break
			}
		}
	}
	if runtime == "" {
		t.Skip("no iOS runtime with an iPhone device type: add one in Xcode's Components settings")
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	name := "huddle-e2e-" + hex.EncodeToString(suffix)
	udid := strings.TrimSpace(simctl(t, simulatorOpenTimeout, "create", name, deviceType, runtime))
	t.Cleanup(func() {
		for _, word := range []string{"shutdown", "delete"} {
			ctx, cancel := context.WithTimeout(context.Background(), simulatorOpenTimeout)
			_ = exec.CommandContext(ctx, "xcrun", "simctl", word, udid).Run()
			cancel()
		}
	})
	// bootstatus -b boots the device and waits until it has finished booting
	simctl(t, simulatorBootTimeout, "bootstatus", udid, "-b")
	return udid
}

// versionParts splits a dotted runtime version ("27.1") into its numbers.
func versionParts(version string) []int {
	parts := []int{}
	for _, field := range strings.Split(version, ".") {
		n, _ := strconv.Atoi(field)
		parts = append(parts, n)
	}
	return parts
}

// versionAfter answers whether version a is newer than b.
func versionAfter(a, b []int) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return len(a) > len(b)
}

// simctl runs one `xcrun simctl` word within the timeout and answers its stdout.
func simctl(t *testing.T, within time.Duration, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	cmd := exec.CommandContext(ctx, "xcrun", append([]string{"simctl"}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("xcrun simctl %s did not finish within %s", strings.Join(args, " "), within)
	}
	if err != nil {
		t.Fatalf("xcrun simctl %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return string(out)
}
