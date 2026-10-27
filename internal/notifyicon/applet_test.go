package notifyicon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// recordingRunner records every command and, for osacompile, lays down the bundle files the
// real one creates, so the steps after it have a bundle to edit.
type recordingRunner struct {
	calls [][]string
	fail  string // the command whose run answers an error
}

func (r *recordingRunner) run(ctx context.Context, name string, args ...string) error {
	r.calls = append(r.calls, append([]string{name}, args...))
	if name == r.fail {
		return fmt.Errorf("%s: failed", name)
	}
	if name == "osacompile" {
		app := args[slices.Index(args, "-o")+1]
		for _, parts := range [][]string{
			{"Contents", "MacOS", "applet"},
			{"Contents", "Info.plist"},
			{"Contents", "Resources", "applet.icns"},
		} {
			path := filepath.Join(append([]string{app}, parts...)...)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte("placeholder"), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

func TestEnsureApplet(t *testing.T) {
	t.Run("builds the applet once: the tools in order, the embedded icon in the bundle, the digest last", func(t *testing.T) {
		home := t.TempDir()
		var run recordingRunner
		stub, err := EnsureApplet(context.Background(), home, run.run)
		if err != nil {
			t.Fatalf("EnsureApplet: %v", err)
		}
		app := AppletPath(home)
		if stub != AppletStub(home) {
			t.Errorf("EnsureApplet = %q, want the stub %q", stub, AppletStub(home))
		}
		plist := filepath.Join(app, "Contents", "Info.plist")
		want := [][]string{
			{"osacompile", "-o", app, "-e", "on run argv",
				"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
				"-e", "end run"},
			{"plutil", "-replace", "CFBundleIdentifier", "-string", BundleID, plist},
			{"plutil", "-replace", "CFBundleName", "-string", "Huddle", plist},
			{"codesign", "--force", "--deep", "-s", "-", app},
			{"/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister",
				"-f", app},
		}
		if len(run.calls) != len(want) {
			t.Fatalf("ran %d commands, want %d: %v", len(run.calls), len(want), run.calls)
		}
		for i, argv := range want {
			if !slices.Equal(run.calls[i], argv) {
				t.Errorf("command %d = %v, want %v", i, run.calls[i], argv)
			}
		}
		icon, err := os.ReadFile(filepath.Join(app, "Contents", "Resources", "applet.icns"))
		if err != nil {
			t.Fatalf("read applet.icns: %v", err)
		}
		if !bytes.Equal(icon, huddleICNS) {
			t.Error("applet.icns is not the embedded huddle.icns bytes")
		}
		digest, err := os.ReadFile(AppletDigestPath(home))
		if err != nil {
			t.Fatalf("read the digest file: %v", err)
		}
		if string(digest) != AppletDigest() {
			t.Errorf("digest file = %q, want %q", digest, AppletDigest())
		}
		info, err := os.Stat(AppletDigestPath(home))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("digest file mode = %v, want 0600", info.Mode().Perm())
		}
		got, ok := AppletReady(home)
		if !ok || got != stub {
			t.Errorf("AppletReady = %q, %v, want %q, true", got, ok, stub)
		}
	})

	t.Run("a ready applet answers its stub and runs nothing", func(t *testing.T) {
		home := t.TempDir()
		var run recordingRunner
		first, err := EnsureApplet(context.Background(), home, run.run)
		if err != nil {
			t.Fatal(err)
		}
		run.calls = nil
		second, err := EnsureApplet(context.Background(), home, run.run)
		if err != nil {
			t.Fatalf("second EnsureApplet: %v", err)
		}
		if second != first {
			t.Errorf("second EnsureApplet = %q, want %q", second, first)
		}
		if len(run.calls) != 0 {
			t.Errorf("the second build ran %v, want nothing", run.calls)
		}
	})

	t.Run("a digest file holding other bytes builds again", func(t *testing.T) {
		home := t.TempDir()
		var run recordingRunner
		if _, err := EnsureApplet(context.Background(), home, run.run); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(AppletDigestPath(home), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		run.calls = nil
		if _, err := EnsureApplet(context.Background(), home, run.run); err != nil {
			t.Fatalf("EnsureApplet over a stale digest: %v", err)
		}
		if len(run.calls) == 0 {
			t.Error("a stale digest left nothing to run, want a rebuild")
		}
	})

	t.Run("a failed codesign answers its error, writes no digest, and nothing is ready", func(t *testing.T) {
		home := t.TempDir()
		var run recordingRunner
		run.fail = "codesign"
		stub, err := EnsureApplet(context.Background(), home, run.run)
		if err == nil {
			t.Fatalf("EnsureApplet over a failing codesign = %q, want an error", stub)
		}
		if !strings.Contains(err.Error(), "codesign") {
			t.Errorf("error = %v, want it to name codesign", err)
		}
		if _, err := os.Stat(AppletDigestPath(home)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the digest file: %v, want it missing", err)
		}
		if _, ok := AppletReady(home); ok {
			t.Error("AppletReady = true after a failed build, want false")
		}
	})

	t.Run("an empty home is an error and runs nothing", func(t *testing.T) {
		var run recordingRunner
		stub, err := EnsureApplet(context.Background(), "", run.run)
		if err == nil {
			t.Fatalf(`EnsureApplet with an empty home = %q, want an error`, stub)
		}
		if len(run.calls) != 0 {
			t.Errorf("ran %v, want nothing", run.calls)
		}
	})
}

// TestEnsureAppletRealTools builds the applet with the machine's own osacompile, plutil and
// codesign, answering lsregister without running it, so the machine's Launch Services database
// is never touched and no banner is ever shown.
func TestEnsureAppletRealTools(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the applet is a macOS build")
	}
	home := t.TempDir()
	ctx := context.Background()
	_, err := EnsureApplet(ctx, home, func(ctx context.Context, name string, args ...string) error {
		if strings.HasSuffix(name, "lsregister") {
			return nil
		}
		return ExecRunner(ctx, name, args...)
	})
	if err != nil {
		t.Fatalf("EnsureApplet with the real tools: %v", err)
	}
	app := AppletPath(home)
	if out, err := exec.Command("codesign", "-v", app).CombinedOutput(); err != nil {
		t.Errorf("codesign -v: %v: %s", err, out)
	}
	out, err := exec.Command("plutil", "-extract", "CFBundleIdentifier", "raw",
		filepath.Join(app, "Contents", "Info.plist")).CombinedOutput()
	if err != nil {
		t.Fatalf("plutil -extract CFBundleIdentifier: %v: %s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != BundleID {
		t.Errorf("CFBundleIdentifier = %q, want %q", got, BundleID)
	}
}
