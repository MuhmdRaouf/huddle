package outside

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// recordingRunner is the runner port's double: it notes every argv it is handed and fails the
// commands named in failing.
type recordingRunner struct {
	calls   []string
	failing map[string]error
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) error {
	r.calls = append(r.calls, strings.Join(append([]string{name}, args...), " "))
	if err, ok := r.failing[name]; ok {
		return err
	}
	return nil
}

// serviceWorld is one install's world: a temp home, temp HOME and XDG_CONFIG_HOME the unit file's
// place comes from, a fixed clock and a recording runner.
func serviceWorld(t *testing.T) (ServiceOptions, *recordingRunner, map[string]string) {
	t.Helper()
	env := map[string]string{
		"HOME":            t.TempDir(),
		"XDG_CONFIG_HOME": t.TempDir(),
	}
	runner := &recordingRunner{}
	o := ServiceOptions{
		Home:   t.TempDir(),
		Bin:    filepath.Join(t.TempDir(), "bin", "huddle"),
		Env:    func(name string) string { return env[name] },
		UID:    501,
		Runner: runner,
		Now:    func() time.Time { return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC) },
	}
	return o, runner, env
}

// serviceReadFile is one file's whole body, or "" when it is not there.
func serviceReadFile(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

// TestRenderLaunchdPlist pins the launchd agent's bytes: the label, the binary running
// serve --foreground, started at load and kept alive, its output in the home's own log. The
// renderer knows no platform, so both files are tested wherever the tests run.
func TestRenderLaunchdPlist(t *testing.T) {
	got := string(RenderLaunchdPlist("/opt/huddle/bin/huddle", "/home/me/.config/huddle/run/huddle.log"))
	want := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>me.raouf.huddle</string>
	<key>ProgramArguments</key>
	<array>
		<string>/opt/huddle/bin/huddle</string>
		<string>serve</string>
		<string>--foreground</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>/home/me/.config/huddle/run/huddle.log</string>
	<key>StandardErrorPath</key>
	<string>/home/me/.config/huddle/run/huddle.log</string>
</dict>
</plist>
`
	if got != want {
		t.Errorf("RenderLaunchdPlist =\n%s\nwant\n%s", got, want)
	}
	// a path with XML's own characters in it must not tear the file open
	got = string(RenderLaunchdPlist("/opt/a&b<huddle>", "/home/me/.config/huddle/run/huddle.log"))
	if !strings.Contains(got, "<string>/opt/a&amp;b&lt;huddle&gt;</string>") {
		t.Errorf("the plist does not escape its paths:\n%s", got)
	}
}

// TestRenderSystemdUnit pins the user unit's bytes: the binary running serve --foreground,
// restarted when it fails, wanted by the user's default target.
func TestRenderSystemdUnit(t *testing.T) {
	got := string(RenderSystemdUnit("/opt/huddle/bin/huddle"))
	want := `[Unit]
Description=Huddle hub

[Service]
ExecStart=/opt/huddle/bin/huddle serve --foreground
Restart=on-failure

[Install]
WantedBy=default.target
`
	if got != want {
		t.Errorf("RenderSystemdUnit =\n%s\nwant\n%s", got, want)
	}
	// a percent sign is systemd's own escape, a space splits an ExecStart
	got = string(RenderSystemdUnit("/opt/100% done/huddle"))
	if !strings.Contains(got, "ExecStart=\"/opt/100%% done/huddle\" serve --foreground\n") {
		t.Errorf("the unit does not escape its ExecStart:\n%s", got)
	}
}

// TestInstallLaunchdService proves the macOS install: the plist lands in ~/Library/LaunchAgents
// with the binary's path and serve --foreground, the load runs through the runner (bootout first,
// bootstrap after), and the ledger in the home records the file.
func TestInstallLaunchdService(t *testing.T) {
	o, runner, env := serviceWorld(t)
	record, err := InstallLaunchdService(context.Background(), o)
	if err != nil {
		t.Fatalf("InstallLaunchdService: %v", err)
	}
	plist := filepath.Join(env["HOME"], "Library", "LaunchAgents", "me.raouf.huddle.plist")
	if record.Unit != plist {
		t.Errorf("the record names %q, want %q", record.Unit, plist)
	}
	if got, want := serviceReadFile(plist), string(RenderLaunchdPlist(o.Bin, filepath.Join(o.Home, "run", "huddle.log"))); got != want {
		t.Errorf("the plist =\n%s\nwant\n%s", got, want)
	}
	if info, err := os.Stat(plist); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("the plist is %v %v, want 0644", info, err)
	}
	want := []string{
		"launchctl bootout gui/501/me.raouf.huddle",
		"launchctl bootstrap gui/501 " + plist,
	}
	if fmt.Sprint(runner.calls) != fmt.Sprint(want) {
		t.Errorf("the runner saw %v, want %v", runner.calls, want)
	}
	rec, ok := ReadService(context.Background(), o.Home)
	if !ok || rec.Unit != plist || rec.At != "2026-10-16T12:00:00.000Z" {
		t.Errorf("the ledger = (%+v, %v), want the unit and the fixed clock's timestamp", rec, ok)
	}
	if rec.Bin != o.Bin {
		t.Errorf("the ledger names the binary %q, want %q", rec.Bin, o.Bin)
	}
}

// TestInstallSystemdService proves the Linux install: the unit lands in
// $XDG_CONFIG_HOME/systemd/user (else ~/.config/systemd/user) and enable --now runs through the
// runner.
func TestInstallSystemdService(t *testing.T) {
	t.Run("XDG_CONFIG_HOME places the unit", func(t *testing.T) {
		o, runner, env := serviceWorld(t)
		record, err := InstallSystemdService(context.Background(), o)
		if err != nil {
			t.Fatalf("InstallSystemdService: %v", err)
		}
		unit := filepath.Join(env["XDG_CONFIG_HOME"], "systemd", "user", "huddle.service")
		if record.Unit != unit {
			t.Errorf("the record names %q, want %q", record.Unit, unit)
		}
		if got, want := serviceReadFile(unit), string(RenderSystemdUnit(o.Bin)); got != want {
			t.Errorf("the unit =\n%s\nwant\n%s", got, want)
		}
		want := []string{"systemctl --user enable --now huddle"}
		if fmt.Sprint(runner.calls) != fmt.Sprint(want) {
			t.Errorf("the runner saw %v, want %v", runner.calls, want)
		}
		if rec, ok := ReadService(context.Background(), o.Home); !ok || rec.Unit != unit {
			t.Errorf("the ledger = (%+v, %v), want the unit %q", rec, ok, unit)
		}
	})
	t.Run("without XDG_CONFIG_HOME the unit sits in ~/.config", func(t *testing.T) {
		o, _, env := serviceWorld(t)
		delete(env, "XDG_CONFIG_HOME")
		record, err := InstallSystemdService(context.Background(), o)
		if err != nil {
			t.Fatalf("InstallSystemdService: %v", err)
		}
		want := filepath.Join(env["HOME"], ".config", "systemd", "user", "huddle.service")
		if record.Unit != want {
			t.Errorf("the record names %q, want %q", record.Unit, want)
		}
	})
}

// TestInstallIsIdempotent pins the second install: the same file, the load run again, one ledger
// entry.
func TestInstallIsIdempotent(t *testing.T) {
	for _, each := range []struct {
		name string
		run  func(context.Context, ServiceOptions) (ServiceRecord, error)
	}{
		{"launchd", InstallLaunchdService},
		{"systemd", InstallSystemdService},
	} {
		t.Run(each.name, func(t *testing.T) {
			o, runner, _ := serviceWorld(t)
			first, err := each.run(context.Background(), o)
			if err != nil {
				t.Fatalf("the first install: %v", err)
			}
			second, err := each.run(context.Background(), o)
			if err != nil {
				t.Fatalf("the second install: %v", err)
			}
			if second.Unit != first.Unit {
				t.Errorf("the second install moved the unit to %q", second.Unit)
			}
			if len(runner.calls) != 4 && each.name == "launchd" {
				t.Errorf("the runner saw %d calls after two installs, want 4", len(runner.calls))
			}
			rec, ok := ReadService(context.Background(), o.Home)
			if !ok || rec.Unit != first.Unit {
				t.Errorf("the ledger after two installs = (%+v, %v)", rec, ok)
			}
		})
	}
}

// TestInstallSurfacesALoadFailure keeps the failure honest: a load that fails surfaces as the
// install's own error, while the ledger still records the unit file that stands outside the home.
func TestInstallSurfacesALoadFailure(t *testing.T) {
	o, runner, _ := serviceWorld(t)
	refused := errors.New("Bootstrap failed: 5: Input/output error")
	runner.failing = map[string]error{"launchctl": refused}
	_, err := InstallLaunchdService(context.Background(), o)
	if !errors.Is(err, refused) {
		t.Errorf("a failed load = %v, want the runner's refusal to surface", err)
	}
	if _, ok := ReadService(context.Background(), o.Home); !ok {
		t.Error("the ledger does not record the unit file a failed load leaves behind")
	}
}

// TestInstallRefusesAHomelessEnvironment keeps the unit out of the working directory: with no
// HOME (and no XDG_CONFIG_HOME) there is no honest place for the file.
func TestInstallRefusesAHomelessEnvironment(t *testing.T) {
	o, _, env := serviceWorld(t)
	delete(env, "HOME")
	delete(env, "XDG_CONFIG_HOME")
	if _, err := InstallSystemdService(context.Background(), o); err == nil {
		t.Error("InstallSystemdService without HOME answered nil, want a refusal")
	}
}

// TestUninstallLaunchdService proves the macOS uninstall: bootout through the runner, the plist
// and the ledger gone, and a second uninstall on nothing installed is a success.
func TestUninstallLaunchdService(t *testing.T) {
	o, runner, env := serviceWorld(t)
	if _, err := InstallLaunchdService(context.Background(), o); err != nil {
		t.Fatalf("install: %v", err)
	}
	plist := filepath.Join(env["HOME"], "Library", "LaunchAgents", "me.raouf.huddle.plist")
	if err := UninstallLaunchdService(context.Background(), o); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if serviceReadFile(plist) != "" {
		t.Error("the plist is still there after the uninstall")
	}
	if _, ok := ReadService(context.Background(), o.Home); ok {
		t.Error("the ledger is still there after the uninstall")
	}
	if len(runner.calls) == 0 {
		t.Fatal("the runner saw no unload")
	}
	last := runner.calls[len(runner.calls)-1]
	if want := "launchctl bootout gui/501/me.raouf.huddle"; last != want {
		t.Errorf("the unload was %q, want %q", last, want)
	}
	if err := UninstallLaunchdService(context.Background(), o); err != nil {
		t.Errorf("the second uninstall: %v, want a success on nothing installed", err)
	}
}

// TestUninstallSystemdService proves the Linux uninstall: disable --now through the runner, the
// unit file and the ledger gone, idempotent.
func TestUninstallSystemdService(t *testing.T) {
	o, runner, env := serviceWorld(t)
	if _, err := InstallSystemdService(context.Background(), o); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := UninstallSystemdService(context.Background(), o); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	unit := filepath.Join(env["XDG_CONFIG_HOME"], "systemd", "user", "huddle.service")
	if serviceReadFile(unit) != "" {
		t.Error("the unit is still there after the uninstall")
	}
	if _, ok := ReadService(context.Background(), o.Home); ok {
		t.Error("the ledger is still there after the uninstall")
	}
	if len(runner.calls) == 0 {
		t.Fatal("the runner saw no unload")
	}
	last := runner.calls[len(runner.calls)-1]
	if want := "systemctl --user disable --now huddle"; last != want {
		t.Errorf("the unload was %q, want %q", last, want)
	}
	if err := UninstallSystemdService(context.Background(), o); err != nil {
		t.Errorf("the second uninstall: %v, want a success on nothing installed", err)
	}
}

// TestUninstallRemovesTheRecordedFile pins the ledger's promise: the uninstall removes the file
// the install wrote (the ledger's own word), not the one this environment would compute now.
func TestUninstallRemovesTheRecordedFile(t *testing.T) {
	o, _, env := serviceWorld(t)
	elsewhere := filepath.Join(env["HOME"], "Elsewhere", "me.raouf.huddle.plist")
	if err := os.MkdirAll(filepath.Dir(elsewhere), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(elsewhere, []byte("unit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := "{\n  \"unit\": " + quoteJSON(elsewhere) + ",\n  \"at\": \"2026-10-16T12:00:00.000Z\"\n}\n"
	if err := os.WriteFile(filepath.Join(o.Home, "service.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UninstallLaunchdService(context.Background(), o); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if serviceReadFile(elsewhere) != "" {
		t.Error("the recorded unit file is still there")
	}
	if computed := filepath.Join(env["HOME"], "Library", "LaunchAgents", "me.raouf.huddle.plist"); serviceReadFile(computed) != "" {
		t.Error("the uninstall wrote a unit file where none was installed")
	}
}

// quoteJSON spells one string the way the ledger's JSON writes it.
func quoteJSON(s string) string {
	return "\"" + strings.ReplaceAll(s, "\\", "\\\\") + "\""
}

// TestInstallServicePicksThisPlatform pins the picker: the platform's own installer runs, so the
// file this OS loads is the file that lands.
func TestInstallServicePicksThisPlatform(t *testing.T) {
	o, _, env := serviceWorld(t)
	record, err := InstallService(context.Background(), o)
	if err != nil {
		t.Fatalf("InstallService: %v", err)
	}
	want := filepath.Join(env["XDG_CONFIG_HOME"], "systemd", "user", "huddle.service")
	if runtime.GOOS == "darwin" {
		want = filepath.Join(env["HOME"], "Library", "LaunchAgents", "me.raouf.huddle.plist")
	}
	if record.Unit != want || serviceReadFile(want) == "" {
		t.Errorf("InstallService wrote %q, want the platform's unit at %q", record.Unit, want)
	}
}

// TestCommandRunnerRunsTheCommandAndFoldsItsWords proves the real runner with a PATH shim: the
// command's own words land in the error, and a command that succeeds is no error at all.
func TestCommandRunnerRunsTheCommandAndFoldsItsWords(t *testing.T) {
	shim := t.TempDir()
	name := "huddle-fake-tool"
	script := filepath.Join(shim, name)
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\"\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim)
	err := CommandRunner{}.Run(context.Background(), name, "one", "two")
	if err == nil || !strings.Contains(err.Error(), "one two") {
		t.Errorf("a failed command = %v, want its own words in the error", err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := (CommandRunner{}).Run(context.Background(), name); err != nil {
		t.Errorf("a successful command = %v, want nil", err)
	}
}

// TestRefreshServiceIfStale pins the update path: a service whose recorded binary no longer
// runs is re-rendered, reloaded and re-recorded; one that already names the running binary
// changes nothing; a home without a service installs nothing; and a reload that fails leaves the
// old file and the old record, silently.
func TestRefreshServiceIfStale(t *testing.T) {
	t.Run("a stale binary is re-rendered, reloaded and re-recorded", func(t *testing.T) {
		o, runner, env := serviceWorld(t)
		o.Bin = "/opt/old/huddle"
		if _, err := InstallLaunchdService(context.Background(), o); err != nil {
			t.Fatalf("install: %v", err)
		}
		plist := filepath.Join(env["HOME"], "Library", "LaunchAgents", "me.raouf.huddle.plist")
		o.Bin = "/opt/new/huddle"
		RefreshServiceIfStale(context.Background(), o)
		want := string(RenderLaunchdPlist("/opt/new/huddle", filepath.Join(o.Home, "run", "huddle.log")))
		if got := serviceReadFile(plist); got != want {
			t.Errorf("the plist =\n%s\nwant the new binary:\n%s", got, want)
		}
		loads := runner.calls[len(runner.calls)-2:]
		wantCalls := []string{
			"launchctl bootout gui/501/me.raouf.huddle",
			"launchctl bootstrap gui/501 " + plist,
		}
		if fmt.Sprint(loads) != fmt.Sprint(wantCalls) {
			t.Errorf("the reload the runner saw = %v, want %v", loads, wantCalls)
		}
		rec, ok := ReadService(context.Background(), o.Home)
		if !ok || rec.Bin != "/opt/new/huddle" {
			t.Errorf("the ledger = (%+v, %v), want the new binary recorded", rec, ok)
		}
	})
	t.Run("the systemd half reloads with daemon-reload then restart", func(t *testing.T) {
		o, runner, env := serviceWorld(t)
		o.Bin = "/opt/old/huddle"
		if _, err := InstallSystemdService(context.Background(), o); err != nil {
			t.Fatalf("install: %v", err)
		}
		unit := filepath.Join(env["XDG_CONFIG_HOME"], "systemd", "user", "huddle.service")
		record, _ := ReadService(context.Background(), o.Home)
		refreshSystemd(context.Background(), o, record, "/opt/new/huddle")
		if got, want := serviceReadFile(unit), string(RenderSystemdUnit("/opt/new/huddle")); got != want {
			t.Errorf("the unit =\n%s\nwant\n%s", got, want)
		}
		wantCalls := []string{
			"systemctl --user daemon-reload",
			"systemctl --user restart huddle",
		}
		loads := runner.calls[len(runner.calls)-2:]
		if fmt.Sprint(loads) != fmt.Sprint(wantCalls) {
			t.Errorf("the reload the runner saw = %v, want %v", loads, wantCalls)
		}
		if rec, _ := ReadService(context.Background(), o.Home); rec.Bin != "/opt/new/huddle" {
			t.Errorf("the ledger names %q, want the new binary", rec.Bin)
		}
	})
	t.Run("the same binary changes nothing", func(t *testing.T) {
		o, runner, env := serviceWorld(t)
		if _, err := InstallLaunchdService(context.Background(), o); err != nil {
			t.Fatalf("install: %v", err)
		}
		plist := filepath.Join(env["HOME"], "Library", "LaunchAgents", "me.raouf.huddle.plist")
		before, calls := serviceReadFile(plist), len(runner.calls)
		RefreshServiceIfStale(context.Background(), o)
		if got := serviceReadFile(plist); got != before {
			t.Error("the plist was rewritten for the same binary")
		}
		if len(runner.calls) != calls {
			t.Errorf("the runner saw %v, want no reload", runner.calls[calls:])
		}
		if rec, _ := ReadService(context.Background(), o.Home); rec.At != "2026-10-16T12:00:00.000Z" {
			t.Errorf("the ledger was rewritten: %+v", rec)
		}
	})
	t.Run("no ledger entry installs nothing", func(t *testing.T) {
		o, runner, env := serviceWorld(t)
		RefreshServiceIfStale(context.Background(), o)
		if runner.calls != nil {
			t.Errorf("the runner saw %v, want nothing", runner.calls)
		}
		if _, ok := ReadService(context.Background(), o.Home); ok {
			t.Error("a service was installed from nothing")
		}
		for _, path := range []string{
			filepath.Join(env["HOME"], "Library", "LaunchAgents", "me.raouf.huddle.plist"),
			filepath.Join(env["XDG_CONFIG_HOME"], "systemd", "user", "huddle.service"),
		} {
			if serviceReadFile(path) != "" {
				t.Errorf("a unit file appeared at %s", path)
			}
		}
	})
	t.Run("a failing reload keeps the old file and the old record", func(t *testing.T) {
		o, runner, env := serviceWorld(t)
		o.Bin = "/opt/old/huddle"
		if _, err := InstallLaunchdService(context.Background(), o); err != nil {
			t.Fatalf("install: %v", err)
		}
		plist := filepath.Join(env["HOME"], "Library", "LaunchAgents", "me.raouf.huddle.plist")
		runner.failing = map[string]error{"launchctl": errors.New("Bootstrap failed: 5: Input/output error")}
		o.Bin = "/opt/new/huddle"
		RefreshServiceIfStale(context.Background(), o) // an entry's side duty: silent whatever happens
		if got, want := serviceReadFile(plist), string(RenderLaunchdPlist("/opt/old/huddle", filepath.Join(o.Home, "run", "huddle.log"))); got != want {
			t.Errorf("the plist =\n%s\nwant the old one kept:\n%s", got, want)
		}
		if rec, _ := ReadService(context.Background(), o.Home); rec.Bin != "/opt/old/huddle" {
			t.Errorf("the ledger names %q, want the old binary kept", rec.Bin)
		}
	})
	t.Run("symlinks resolve: a launcher and its target are one binary", func(t *testing.T) {
		o, runner, _ := serviceWorld(t)
		dir := t.TempDir()
		real := filepath.Join(dir, "huddle-9.9.9")
		if err := os.WriteFile(real, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "huddle")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		o.Bin = link
		if _, err := InstallLaunchdService(context.Background(), o); err != nil {
			t.Fatalf("install: %v", err)
		}
		calls := len(runner.calls)
		o.Bin = real
		RefreshServiceIfStale(context.Background(), o)
		if len(runner.calls) != calls {
			t.Error("two spellings of one binary were treated as an update")
		}
	})
}
