package isoenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ShimsEnv names the directory InstallShims filled for this run. scripts/tmpguard makes it
// before the suite starts and reads its record after; Environ and Sandbox put its bin directory
// first on the PATH they build. A run without it (a plain go test) has no shims.
const ShimsEnv = "ISOENV_NOTIFIER_SHIMS"

// notifierPrograms are the programs that put something on the owner's desktop or build the
// applet that does. The notifier runs osascript and notify-send by name, so a shim first on the
// PATH catches the spawn; the applet's own stub is a path and is the notifier's own business.
var notifierPrograms = []string{"osascript", "osacompile", "notify-send"}

// spawnsFile is the record, beside bin: one line per spawn.
const spawnsFile = "spawns.log"

// InstallShims fills dir with bin/<program> for each notifier program and an empty record. A
// shim appends its name and its arguments to the record, one line, and exits 1.
func InstallShims(dir string) error {
	record := filepath.Join(dir, spawnsFile)
	if strings.ContainsAny(record, "'\n") {
		return fmt.Errorf("isoenv: the shim record path %q holds a quote or a newline", record)
	}
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(record, nil, 0o600); err != nil {
		return err
	}
	for _, program := range notifierPrograms {
		script := fmt.Sprintf("#!/bin/sh\nline=%s\nfor arg in \"$@\"; do line=\"$line $arg\"; done\nprintf '%%s\\n' \"$line\" >> '%s'\nexit 1\n", program, record)
		if err := os.WriteFile(filepath.Join(dir, "bin", program), []byte(script), 0o700); err != nil {
			return err
		}
	}
	return nil
}

// Spawns answers the notifier spawns the shims in dir recorded, one line each.
func Spawns(dir string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(dir, spawnsFile))
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

// shimmedPath answers the process's PATH with the run's shim directory first, and false when
// the run has no shims.
func shimmedPath() (string, bool) {
	return shimmedPathOver(os.Getenv("PATH"))
}

// shimmedPathOver answers path with the run's shim directory first, and false when the run has
// no shims.
func shimmedPathOver(path string) (string, bool) {
	dir := os.Getenv(ShimsEnv)
	if dir == "" {
		return "", false
	}
	shims := filepath.Join(dir, "bin")
	if path != "" {
		shims += string(os.PathListSeparator) + path
	}
	return shims, true
}

// Guard answers a copy of a hand-built child environment map that cannot reach the owner's
// desktop: a HUDDLE_NOTIFY_LOG under its HOME unless it names one, and the run's notifier shims
// first on its PATH when the run has them. A map with no HOME keeps no log.
func Guard(env map[string]string) map[string]string {
	guarded := make(map[string]string, len(env)+2)
	for name, value := range env {
		guarded[name] = value
	}
	if home := env["HOME"]; home != "" && env["HUDDLE_NOTIFY_LOG"] == "" {
		guarded["HUDDLE_NOTIFY_LOG"] = notifyLog(home)
	}
	if shimmed, ok := shimmedPathOver(env["PATH"]); ok {
		guarded["PATH"] = shimmed
	}
	return guarded
}
