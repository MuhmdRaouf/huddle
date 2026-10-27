package notifyicon

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// huddleICNS is the macOS applet's icon, the huddle icon as an icns. It is made once on a Mac
// and committed; regenerating it:
//
//	set=.agents/scratch/huddle.iconset
//	mkdir -p "$set"
//	sips -z 16 16   internal/huddle/web/dist/icons/icon-512.png --out "$set/icon_16x16.png"
//	sips -z 32 32   internal/huddle/web/dist/icons/icon-512.png --out "$set/icon_16x16@2x.png"
//	sips -z 32 32   internal/huddle/web/dist/icons/icon-512.png --out "$set/icon_32x32.png"
//	sips -z 64 64   internal/huddle/web/dist/icons/icon-512.png --out "$set/icon_32x32@2x.png"
//	sips -z 128 128 internal/huddle/web/dist/icons/icon-512.png --out "$set/icon_128x128.png"
//	sips -z 256 256 internal/huddle/web/dist/icons/icon-512.png --out "$set/icon_128x128@2x.png"
//	sips -z 256 256 internal/huddle/web/dist/icons/icon-512.png --out "$set/icon_256x256.png"
//	sips -z 512 512 internal/huddle/web/dist/icons/icon-512.png --out "$set/icon_256x256@2x.png"
//	sips -z 512 512 internal/huddle/web/dist/icons/icon-512.png --out "$set/icon_512x512.png"
//	iconutil -c icns "$set" -o internal/notifyicon/huddle.icns
//
//go:embed huddle.icns
var huddleICNS []byte

// BundleID is the applet's bundle id. macOS keys the one-time "allow notifications from
// Huddle" permission to it, so it must never change.
const BundleID = "dev.huddle.notify"

// lsregister re-registers the applet with Launch Services so it re-reads the icon.
const lsregister = "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"

// scriptLines is the applet's whole script: the text arrives as run arguments, never inside
// the compiled script.
var scriptLines = []string{
	"on run argv",
	"display notification (item 2 of argv) with title (item 1 of argv)",
	"end run",
}

// AppletPath is the applet bundle under home.
func AppletPath(home string) string {
	return filepath.Join(Dir(home), "notify", "Huddle.app")
}

// AppletStub is the executable inside the applet, the program a notifier runs.
func AppletStub(home string) string {
	return filepath.Join(AppletPath(home), "Contents", "MacOS", "applet")
}

// AppletDigestPath is the digest file beside the bundle (outside it, so a rebuild never
// leaves a stale digest inside a fresh bundle).
func AppletDigestPath(home string) string {
	return filepath.Join(Dir(home), "notify", "Huddle.app.sha256")
}

// AppletDigest is the hex SHA-256 of the script, the bundle id and the icon: what a built
// applet is made of.
func AppletDigest() string {
	h := sha256.New()
	for _, part := range []string{strings.Join(scriptLines, "\n"), BundleID} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	h.Write(huddleICNS)
	return hex.EncodeToString(h.Sum(nil))
}

// AppletReady answers the applet's stub when a built applet with exactly the current script,
// bundle id and icon sits under home.
func AppletReady(home string) (string, bool) {
	stub := AppletStub(home)
	info, err := os.Stat(stub)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	data, err := os.ReadFile(AppletDigestPath(home))
	if err != nil || string(data) != AppletDigest() {
		return "", false
	}
	return stub, true
}

// Runner runs one build command.
type Runner func(ctx context.Context, name string, args ...string) error

// ExecRunner runs a command and folds its output into the error when it fails.
func ExecRunner(ctx context.Context, name string, args ...string) error {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil && len(out) > 0 {
		return fmt.Errorf("%s: %v: %s", name, err, out)
	}
	if err != nil {
		return fmt.Errorf("%s: %v", name, err)
	}
	return nil
}

// EnsureApplet builds Huddle.app under home when it is missing or holds anything else: it
// compiles the script with osacompile, stamps the bundle id and name with plutil, writes the
// huddle icon over the applet's, ad-hoc signs it and registers it with Launch Services, and
// writes the digest file last. A ready applet answers its stub and runs nothing. Any step's
// error stops the build and leaves no digest, so the next call rebuilds.
func EnsureApplet(ctx context.Context, home string, run Runner) (string, error) {
	if home == "" {
		return "", errors.New("notifyicon: no home directory")
	}
	if stub, ok := AppletReady(home); ok {
		return stub, nil
	}
	app := AppletPath(home)
	plist := filepath.Join(app, "Contents", "Info.plist")
	icon := filepath.Join(app, "Contents", "Resources", "applet.icns")
	if err := os.RemoveAll(app); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(app), 0o700); err != nil {
		return "", err
	}
	args := []string{"-o", app}
	for _, line := range scriptLines {
		args = append(args, "-e", line)
	}
	if err := run(ctx, "osacompile", args...); err != nil {
		return "", err
	}
	if err := run(ctx, "plutil", "-replace", "CFBundleIdentifier", "-string", BundleID, plist); err != nil {
		return "", err
	}
	if err := run(ctx, "plutil", "-replace", "CFBundleName", "-string", "Huddle", plist); err != nil {
		return "", err
	}
	if err := os.WriteFile(icon, huddleICNS, 0o644); err != nil {
		return "", err
	}
	if err := run(ctx, "codesign", "--force", "--deep", "-s", "-", app); err != nil {
		return "", err
	}
	if err := run(ctx, lsregister, "-f", app); err != nil {
		return "", err
	}
	if err := os.WriteFile(AppletDigestPath(home), []byte(AppletDigest()), 0o600); err != nil {
		return "", err
	}
	return AppletStub(home), nil
}
