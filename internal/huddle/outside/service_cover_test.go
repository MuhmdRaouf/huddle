package outside

// The service installer's defaults, refusals and refresh corners: what fills in from the
// process when the options carry nothing, the installs that cannot land, and the refresh's
// quiet no-ops and restorations.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServiceOptionsFillTheirDefaultsFromTheProcess(t *testing.T) {
	t.Setenv("HUDDLE_SERVICE_PROBE", "process-value")
	o := ServiceOptions{}
	if got := o.env("HUDDLE_SERVICE_PROBE"); got != "process-value" {
		t.Errorf("env() = %q, want the process's variable", got)
	}
	if _, ok := interface{}(o.runner()).(CommandRunner); !ok {
		t.Errorf("runner() = %T, want the real runner", o.runner())
	}
	if o.now().IsZero() {
		t.Error("now() = the zero time, want a clock")
	}
	if got := readEnv(nil, "HUDDLE_SERVICE_PROBE"); got != "process-value" {
		t.Errorf("readEnv(nil) = %q, want the process's variable", got)
	}
	if got := readEnv(func(string) string { return "own" }, "X"); got != "own" {
		t.Errorf("readEnv(env) = %q, want the env's own", got)
	}
}

func TestCommandRunnerFoldsAWordlessFailureToo(t *testing.T) {
	shim := t.TempDir()
	name := "huddle-silent-tool"
	script := filepath.Join(shim, name)
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim)
	err := (CommandRunner{}).Run(context.Background(), name)
	if err == nil || !strings.Contains(err.Error(), "exit status 7") || strings.Contains(err.Error(), "7: 7") {
		t.Errorf("a wordless failure = %v, want the command and its status alone", err)
	}
}

func TestAnInstallWithoutAHomeRefusesEachPlatform(t *testing.T) {
	// launchd: no HOME leaves the plist no place, and the refusal names the variable to set
	o, _, env := serviceWorld(t)
	delete(env, "HOME")
	delete(env, "XDG_CONFIG_HOME")
	if _, err := InstallLaunchdService(context.Background(), o); err == nil {
		t.Error("InstallLaunchdService without HOME answered nil, want a refusal")
	} else if !strings.Contains(err.Error(), "set HOME") || strings.Contains(err.Error(), "XDG_CONFIG_HOME") {
		t.Errorf("the launchd refusal = %v, want HOME named and no XDG hint", err)
	}
	// systemd: no HOME and no XDG_CONFIG_HOME refuses with both named
	if _, err := InstallSystemdService(context.Background(), o); err == nil {
		t.Error("InstallSystemdService without a place answered nil, want a refusal")
	} else if !strings.Contains(err.Error(), "XDG_CONFIG_HOME") {
		t.Errorf("the systemd refusal = %v, want the XDG hint", err)
	}
}

func TestAnInstallKeepsItsLedgerRefusalHonest(t *testing.T) {
	// a home that cannot be written refuses the ledger, after the unit file stood
	root := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
	o, _, _ := serviceWorld(t)
	o.Home = root
	if _, err := InstallLaunchdService(context.Background(), o); err == nil {
		t.Error("InstallLaunchdService with an unwritable home answered nil, want the ledger's refusal")
	}
	if _, err := InstallSystemdService(context.Background(), o); err == nil {
		t.Error("InstallSystemdService with an unwritable home answered nil, want the ledger's refusal")
	}
}

func TestARefusingEnableSurfacesForSystemd(t *testing.T) {
	o, runner, _ := serviceWorld(t)
	refused := errors.New("systemctl: failed")
	runner.failing = map[string]error{"systemctl": refused}
	record, err := InstallSystemdService(context.Background(), o)
	if !errors.Is(err, refused) {
		t.Errorf("InstallSystemdService = %v, want the runner's refusal", err)
	}
	if record.Unit == "" {
		t.Error("the record is empty, want the unit file the install wrote")
	}
}

func TestUninstallServicePicksThisPlatform(t *testing.T) {
	o, runner, env := serviceWorld(t)
	if _, err := InstallService(context.Background(), o); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := UninstallService(context.Background(), o); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	last := runner.calls[len(runner.calls)-1]
	want := "systemctl --user disable --now " + ServiceUnit
	if strings.Contains(last, "launchctl") {
		want = "launchctl bootout gui/501/" + ServiceLabel
	}
	if last != want {
		t.Errorf("the unload was %q, want %q", last, want)
	}
	if serviceReadFile(filepath.Join(env["HOME"], "Library", "LaunchAgents", ServiceLabel+".plist")) != "" &&
		serviceReadFile(filepath.Join(env["XDG_CONFIG_HOME"], "systemd", "user", ServiceUnit+".service")) != "" {
		t.Error("a unit file is still there after the uninstall")
	}
}

func TestAnUninstallRefusesWhenTheUnitFileCannotBeRemoved(t *testing.T) {
	o, _, env := serviceWorld(t)
	elsewhere := filepath.Join(env["HOME"], "Elsewhere", ServiceLabel+".plist")
	if err := os.MkdirAll(elsewhere, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(elsewhere, "inside"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := "{\n  \"unit\": " + quoteJSON(elsewhere) + ",\n  \"at\": \"2026-10-16T12:00:00.000Z\"\n}\n"
	if err := os.WriteFile(filepath.Join(o.Home, "service.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UninstallLaunchdService(context.Background(), o); err == nil {
		t.Error("the uninstall of a unit file that cannot go answered nil, want an error")
	}
}

// staleLedger writes one service ledger entry naming unit, with the timestamp fixed.
func staleLedger(t *testing.T, home, unit string) {
	t.Helper()
	body := "{\n  \"unit\": " + quoteJSON(unit) + ",\n  \"bin\": \"/opt/old/huddle\",\n  \"at\": \"2026-10-16T12:00:00.000Z\"\n}\n"
	if err := os.WriteFile(filepath.Join(home, "service.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestARefreshSkipsTheUnitFilesThatAreNotThere(t *testing.T) {
	o, runner, env := serviceWorld(t)
	o.Bin = "/opt/new/huddle"
	// a launchd record whose plist is gone: nothing to refresh, no reload, no ledger rewrite
	staleLedger(t, o.Home, filepath.Join(env["HOME"], "Library", "LaunchAgents", ServiceLabel+".plist"))
	RefreshServiceIfStale(context.Background(), o)
	if len(runner.calls) != 0 {
		t.Errorf("the runner saw %v, want nothing for a missing plist", runner.calls)
	}
	// a systemd record whose unit is gone: the same silence
	staleLedger(t, o.Home, filepath.Join(env["XDG_CONFIG_HOME"], "systemd", "user", ServiceUnit+".service"))
	RefreshServiceIfStale(context.Background(), o)
	if len(runner.calls) != 0 {
		t.Errorf("the runner saw %v, want nothing for a missing unit", runner.calls)
	}
	if rec, _ := ReadService(context.Background(), o.Home); rec.Bin != "/opt/old/huddle" {
		t.Errorf("the ledger was rewritten to %+v, want it left alone", rec)
	}
}

func TestARefreshFollowsTheUnitTheLedgerNames(t *testing.T) {
	o, runner, env := serviceWorld(t)
	unit := filepath.Join(env["XDG_CONFIG_HOME"], "systemd", "user", ServiceUnit+".service")
	staleLedger(t, o.Home, unit)
	if err := os.MkdirAll(filepath.Dir(unit), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unit, RenderSystemdUnit("/opt/old/huddle"), 0o644); err != nil {
		t.Fatal(err)
	}
	o.Bin = "/opt/new/huddle"
	RefreshServiceIfStale(context.Background(), o)
	wantCalls := []string{"systemctl --user daemon-reload", "systemctl --user restart " + ServiceUnit}
	if fmt.Sprint(runner.calls) != fmt.Sprint(wantCalls) {
		t.Errorf("the runner saw %v, want %v", runner.calls, wantCalls)
	}
	if rec, _ := ReadService(context.Background(), o.Home); rec.Bin != "/opt/new/huddle" {
		t.Errorf("the ledger names %q, want the new binary", rec.Bin)
	}
}

func TestARefreshFallsBackToThisExecutableAndKeepsQuietWhenTheUnitCannotGo(t *testing.T) {
	o, runner, env := serviceWorld(t)
	plist := filepath.Join(env["HOME"], "Library", "LaunchAgents", ServiceLabel+".plist")
	staleLedger(t, o.Home, plist)
	if err := os.MkdirAll(filepath.Dir(plist), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plist, RenderLaunchdPlist("/opt/old/huddle", filepath.Join(o.Home, "run", "huddle.log")), 0o644); err != nil {
		t.Fatal(err)
	}
	// no Bin in the options: the refresh compares against this process's own executable
	o.Bin = ""
	RefreshServiceIfStale(context.Background(), o)
	if len(runner.calls) == 0 {
		t.Fatal("the runner saw nothing, want the refresh to have run")
	}
	rec, _ := ReadService(context.Background(), o.Home)
	if rec.Bin == "" || rec.Bin == "/opt/old/huddle" {
		t.Errorf("the ledger names %q, want this executable", rec.Bin)
	}
	// an unwritable unit file ends the refresh before any reload: silent, nothing changed
	o.Bin = "/opt/new/huddle"
	if err := os.Chmod(filepath.Dir(plist), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(plist), 0o700) })
	calls := len(runner.calls)
	RefreshServiceIfStale(context.Background(), o)
	if len(runner.calls) != calls {
		t.Errorf("the runner saw %v, want no reload past the write failure", runner.calls[calls:])
	}
}

func TestARefreshThatCannotReloadRestoresTheSystemdUnit(t *testing.T) {
	o, runner, env := serviceWorld(t)
	o.Bin = "/opt/old/huddle"
	if _, err := InstallSystemdService(context.Background(), o); err != nil {
		t.Fatalf("install: %v", err)
	}
	unit := filepath.Join(env["XDG_CONFIG_HOME"], "systemd", "user", ServiceUnit+".service")
	runner.failing = map[string]error{"systemctl": errors.New("systemctl: failed")}
	o.Bin = "/opt/new/huddle"
	RefreshServiceIfStale(context.Background(), o)
	if got, want := serviceReadFile(unit), string(RenderSystemdUnit("/opt/old/huddle")); got != want {
		t.Errorf("the unit =\n%s\nwant the old one kept:\n%s", got, want)
	}
	if rec, _ := ReadService(context.Background(), o.Home); rec.Bin != "/opt/old/huddle" {
		t.Errorf("the ledger names %q, want the old binary kept", rec.Bin)
	}
}

func TestTheServiceClockDefaultsFillTheLedger(t *testing.T) {
	o, _, _ := serviceWorld(t)
	o.Now = nil
	record, err := InstallLaunchdService(context.Background(), o)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	at, err := time.Parse("2006-01-02T15:04:05.000Z", record.At)
	if err != nil {
		t.Fatalf("the ledger's timestamp %q: %v", record.At, err)
	}
	if elapsed := time.Since(at); elapsed < 0 || elapsed > time.Minute {
		t.Errorf("the ledger's timestamp is %s away, want roughly now", elapsed)
	}
}
