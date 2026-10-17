// The system service (a hub machine's install, run with no Claude session open): `huddle service
// install` writes a launchd agent (macOS) or a systemd user unit (Linux) that runs this binary's
// serve entry in the foreground, then loads it; `huddle service uninstall` unloads it and removes
// the file. The unit file sits outside the huddle home, so the install is recorded in the home's
// service ledger the moment the file stands, and the plugin's own removal uninstalls the service
// with everything else.
package outside

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/fsutil"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// ServiceLabel is the service's name: the launchd label and the plist's file name on macOS.
const ServiceLabel = "me.raouf.huddle"

// ServiceUnit is the systemd user unit's name on Linux.
const ServiceUnit = "huddle"

// serviceDescription is the unit's description wherever one is asked for.
const serviceDescription = "Huddle hub"

// ServiceRunner runs one system command (launchctl, systemctl). The real one runs it as a child
// of this process; a test stands in with one that records its argv.
type ServiceRunner interface {
	Run(ctx context.Context, name string, args ...string) error
}

// CommandRunner runs the system command as a child of this process, the words it prints folded
// into the error: launchctl's and systemctl's own answers are the best diagnosis a failed load
// carries.
type CommandRunner struct{}

// Run runs name with args and waits for it, bounded by ctx.
func (CommandRunner) Run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	var said bytes.Buffer
	cmd.Stdout, cmd.Stderr = &said, &said
	if err := cmd.Run(); err != nil {
		words := strings.Join(args, " ")
		if line := strings.TrimSpace(said.String()); line != "" {
			return fmt.Errorf("%s %s: %w: %s", name, words, err, line)
		}
		return fmt.Errorf("%s %s: %w", name, words, err)
	}
	return nil
}

// ServiceRecord is the service ledger's entry: the unit file the install wrote outside the home
// (the thing an uninstall removes), the binary it named (the thing a refresh compares) and when.
type ServiceRecord struct {
	Unit string `json:"unit"`
	Bin  string `json:"bin"`
	At   string `json:"at"`
}

// ReadService is the home's service ledger entry; false when no service is installed.
func ReadService(ctx context.Context, root string) (ServiceRecord, bool) {
	return fsutil.ReadJSONFile[ServiceRecord](ctx, home.Home{Root: root}.Service())
}

// RemoveService drops the ledger, there or not.
func RemoveService(root string) {
	_ = os.Remove(home.Home{Root: root}.Service())
}

// ServiceOptions shapes one install or uninstall: the home whose ledger records it, the binary
// the unit runs, the environment that places the unit file, the user id launchctl's gui/ domain
// names, the runner that runs the system command and the clock the ledger's timestamp comes from.
type ServiceOptions struct {
	Home   string
	Bin    string
	Env    func(string) string
	UID    int
	Runner ServiceRunner
	Now    func() time.Time
}

// env reads one variable of the options' environment, the process's own with none.
func (o ServiceOptions) env(name string) string {
	if o.Env == nil {
		return os.Getenv(name)
	}
	return o.Env(name)
}

// runner is the options' runner, the real one with none.
func (o ServiceOptions) runner() ServiceRunner {
	if o.Runner == nil {
		return CommandRunner{}
	}
	return o.Runner
}

// now is the options' clock, the wall clock with none.
func (o ServiceOptions) now() time.Time {
	if o.Now == nil {
		return time.Now()
	}
	return o.Now()
}

// LaunchdPlistPath is the launchd agent's place: ~/Library/LaunchAgents.
func LaunchdPlistPath(env func(string) string) string {
	return filepath.Join(readEnv(env, "HOME"), "Library", "LaunchAgents", ServiceLabel+".plist")
}

// SystemdUnitPath is the systemd user unit's place: $XDG_CONFIG_HOME/systemd/user, else
// ~/.config/systemd/user.
func SystemdUnitPath(env func(string) string) string {
	if xdg := readEnv(env, "XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "systemd", "user", ServiceUnit+".service")
	}
	return filepath.Join(readEnv(env, "HOME"), ".config", "systemd", "user", ServiceUnit+".service")
}

// readEnv reads one variable through env, the process's own with none.
func readEnv(env func(string) string, name string) string {
	if env == nil {
		return os.Getenv(name)
	}
	return env(name)
}

// RenderLaunchdPlist is the launchd agent's plist: the label, the binary running serve
// --foreground, started at load and kept alive, its output in the home's own log. The renderer
// knows no platform, so both files are tested wherever the tests run.
func RenderLaunchdPlist(bin, log string) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + ServiceLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + xmlEscape(bin) + `</string>
		<string>serve</string>
		<string>--foreground</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>` + xmlEscape(log) + `</string>
	<key>StandardErrorPath</key>
	<string>` + xmlEscape(log) + `</string>
</dict>
</plist>
`)
}

// RenderSystemdUnit is the systemd user unit: the binary running serve --foreground, restarted
// when it fails, wanted by the user's default target.
func RenderSystemdUnit(bin string) []byte {
	return []byte(`[Unit]
Description=` + serviceDescription + `

[Service]
ExecStart=` + systemdExecStart(bin) + ` serve --foreground
Restart=on-failure

[Install]
WantedBy=default.target
`)
}

// xmlEscape escapes one path for the plist's text nodes: a directory name carrying XML's own
// characters must not tear the file open.
func xmlEscape(s string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		return s
	}
	return b.String()
}

// systemdExecStart spells the binary's path the way an ExecStart line reads it: a percent sign is
// systemd's own escape, and a path with spaces in it is quoted whole.
func systemdExecStart(bin string) string {
	escaped := strings.ReplaceAll(bin, "%", "%%")
	if strings.ContainsAny(escaped, " \t") {
		return `"` + escaped + `"`
	}
	return escaped
}

// InstallService installs this platform's service: a launchd agent on macOS, a systemd user unit
// on Linux.
func InstallService(ctx context.Context, o ServiceOptions) (ServiceRecord, error) {
	if runtime.GOOS == "darwin" {
		return InstallLaunchdService(ctx, o)
	}
	return InstallSystemdService(ctx, o)
}

// InstallLaunchdService writes the launchd agent and loads it. Idempotent: bootstrap refuses a
// label that is already loaded, so the load clears one that stands first — a second install
// rewrites the agent and restarts the service on it.
func InstallLaunchdService(ctx context.Context, o ServiceOptions) (ServiceRecord, error) {
	unit := LaunchdPlistPath(o.Env)
	if err := writeUnit(ctx, unit, RenderLaunchdPlist(o.Bin, home.Home{Root: o.Home}.LogFile())); err != nil {
		return ServiceRecord{}, err
	}
	record, err := recordService(ctx, o, unit, o.Bin)
	if err != nil {
		return record, err
	}
	gui := fmt.Sprintf("gui/%d", o.UID)
	_ = o.runner().Run(ctx, "launchctl", "bootout", gui+"/"+ServiceLabel) // nothing loaded yet is fine
	if err := o.runner().Run(ctx, "launchctl", "bootstrap", gui, unit); err != nil {
		return record, err
	}
	return record, nil
}

// InstallSystemdService writes the systemd user unit and enables it. Idempotent: enable --now on
// an enabled unit is systemd's own no-op.
func InstallSystemdService(ctx context.Context, o ServiceOptions) (ServiceRecord, error) {
	unit := SystemdUnitPath(o.Env)
	if err := writeUnit(ctx, unit, RenderSystemdUnit(o.Bin)); err != nil {
		return ServiceRecord{}, err
	}
	record, err := recordService(ctx, o, unit, o.Bin)
	if err != nil {
		return record, err
	}
	if err := o.runner().Run(ctx, "systemctl", "--user", "enable", "--now", ServiceUnit); err != nil {
		return record, err
	}
	return record, nil
}

// UninstallService removes this platform's service.
func UninstallService(ctx context.Context, o ServiceOptions) error {
	if runtime.GOOS == "darwin" {
		return UninstallLaunchdService(ctx, o)
	}
	return UninstallSystemdService(ctx, o)
}

// UninstallLaunchdService unloads the agent and removes its file and the ledger entry.
// Idempotent: a bootout with nothing loaded is not an error, and a missing file is fine.
func UninstallLaunchdService(ctx context.Context, o ServiceOptions) error {
	gui := fmt.Sprintf("gui/%d", o.UID)
	_ = o.runner().Run(ctx, "launchctl", "bootout", gui+"/"+ServiceLabel)
	return removeServiceFiles(ctx, o, LaunchdPlistPath(o.Env))
}

// UninstallSystemdService disables the unit and removes its file and the ledger entry.
// Idempotent: disabling an unknown unit is not treated as a failure.
func UninstallSystemdService(ctx context.Context, o ServiceOptions) error {
	_ = o.runner().Run(ctx, "systemctl", "--user", "disable", "--now", ServiceUnit)
	return removeServiceFiles(ctx, o, SystemdUnitPath(o.Env))
}

// RefreshServiceIfStale keeps an installed service pointing at the binary that runs: an update
// replaces the versioned binary the unit file names, so the first entry after it re-renders the
// file, reloads the service and re-records the ledger. A home without a service installs
// nothing, a service already naming this binary is left alone, and any failure is silent and
// leaves the file as it stood — an entry calls this beside its own work and never fails for it.
func RefreshServiceIfStale(ctx context.Context, o ServiceOptions) {
	record, ok := ReadService(ctx, o.Home)
	if !ok {
		return
	}
	current := o.Bin
	if current == "" {
		if own, err := os.Executable(); err == nil {
			current = own
		}
	}
	if record.Bin != "" && samePath(record.Bin, current) {
		return
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// the recorded unit names the service manager that holds it, so the refresh follows what was
	// installed rather than guessing from the platform
	if strings.HasSuffix(record.Unit, ".plist") {
		refreshLaunchd(bounded, o, record, current)
		return
	}
	refreshSystemd(bounded, o, record, current)
}

// samePath answers whether two spellings name one file, symlinks resolved on both: a launcher's
// path and the versioned binary it points at are the same update-wise.
func samePath(a, b string) bool {
	if a == b {
		return true
	}
	if resolved, err := filepath.EvalSymlinks(a); err == nil {
		a = resolved
	}
	if resolved, err := filepath.EvalSymlinks(b); err == nil {
		b = resolved
	}
	return a == b
}

// refreshLaunchd re-renders the launchd agent for the binary that runs and reloads it: bootout
// first (bootstrap refuses a loaded label), bootstrap after.
func refreshLaunchd(ctx context.Context, o ServiceOptions, record ServiceRecord, bin string) {
	unit := record.Unit
	old, err := os.ReadFile(unit)
	if err != nil {
		return // nothing installed to refresh
	}
	gui := fmt.Sprintf("gui/%d", o.UID)
	refreshUnit(ctx, o, unit, RenderLaunchdPlist(bin, home.Home{Root: o.Home}.LogFile()), old,
		func(runner ServiceRunner) error {
			_ = runner.Run(ctx, "launchctl", "bootout", gui+"/"+ServiceLabel)
			return runner.Run(ctx, "launchctl", "bootstrap", gui, unit)
		},
		func() { _, _ = recordService(ctx, o, unit, bin) })
}

// refreshSystemd re-renders the user unit for the binary that runs and reloads it: daemon-reload
// (the manager reads the new file), then restart (the running service becomes it).
func refreshSystemd(ctx context.Context, o ServiceOptions, record ServiceRecord, bin string) {
	unit := record.Unit
	old, err := os.ReadFile(unit)
	if err != nil {
		return // nothing installed to refresh
	}
	refreshUnit(ctx, o, unit, RenderSystemdUnit(bin), old,
		func(runner ServiceRunner) error {
			if err := runner.Run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
				return err
			}
			return runner.Run(ctx, "systemctl", "--user", "restart", ServiceUnit)
		},
		func() { _, _ = recordService(ctx, o, unit, bin) })
}

// refreshUnit swaps the unit file for the freshly rendered one, reloads the service, and records
// the new binary only when both held: a reload that fails puts the old file back and the ledger
// keeps naming it, silently.
func refreshUnit(ctx context.Context, o ServiceOptions, unit string, fresh, old []byte, reload func(ServiceRunner) error, record func()) {
	if err := writeUnit(ctx, unit, fresh); err != nil {
		return
	}
	if err := reload(o.runner()); err != nil {
		_, _ = fsutil.WriteFileAtomic(ctx, unit, old, fsutil.WriteOptions{Mode: fsutil.Perm(0o644)})
		return
	}
	record()
}

// writeUnit lays the unit file down where the platform keeps it, 0644 and atomic: both managers
// read it as this user, and a reader never sees half of it. A place the environment cannot name
// (no HOME, no XDG_CONFIG_HOME) is a refusal, never a file in the working directory.
func writeUnit(ctx context.Context, unit string, body []byte) error {
	if !filepath.IsAbs(unit) {
		return fmt.Errorf("no place for the service file: set HOME%s", xdgHint(unit))
	}
	_, err := fsutil.WriteFileAtomic(ctx, unit, body, fsutil.WriteOptions{Mode: fsutil.Perm(0o644), Mkdir: true})
	return err
}

// xdgHint names the variable the systemd path misses; empty on macOS, whose plist needs only HOME.
func xdgHint(unit string) string {
	if strings.HasSuffix(unit, ".service") {
		return " or XDG_CONFIG_HOME"
	}
	return ""
}

// recordService notes the install in the home's ledger the moment the unit file stands: the file
// is the change outside the plugin, recorded whether or not the load then succeeds, with the
// binary it names (the thing a later refresh compares).
func recordService(ctx context.Context, o ServiceOptions, unit, bin string) (ServiceRecord, error) {
	record := ServiceRecord{Unit: unit, Bin: bin, At: jsTimestamp(o.now())}
	body, err := domain.MarshalIndentJSON(record)
	if err != nil {
		return record, err
	}
	path := home.Home{Root: o.Home}.Service()
	if _, err := fsutil.WriteFileAtomic(ctx, path, body, fsutil.WriteOptions{Mkdir: true}); err != nil {
		return record, err
	}
	return record, nil
}

// removeServiceFiles removes the unit file — the one the ledger recorded when it has a record,
// else the one this environment computes — and the ledger entry with it. An unload that fails
// because nothing is loaded never stops the removal.
func removeServiceFiles(ctx context.Context, o ServiceOptions, computed string) error {
	unit := computed
	if recorded, ok := ReadService(ctx, o.Home); ok && recorded.Unit != "" {
		unit = recorded.Unit
	}
	if err := os.Remove(unit); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	RemoveService(o.Home)
	return nil
}
