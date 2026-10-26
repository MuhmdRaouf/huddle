// The router's registry entry and its pid's command line: RegistryEntryFor is the entry a
// provider's router reports about itself, and CommandLineOf reads the command a pid runs
// (/proc, else `ps`).
package router

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/adapters/process"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// RegistryEntryFor is this plugin's registry entry: its resolved catalog ids (env overrides
// included) and the pid when it is known.
func RegistryEntryFor(provider domain.Provider, env domain.EnvLookup, pid *int64) RegisteredRouter {
	claim := domain.ResolveProvider(provider, env).ModelClaim()
	return RegisteredRouter{
		Name:          provider.Name,
		Port:          int64(domain.RouterPort(provider, env)),
		ModelPrefixes: claim.Prefixes,
		CatalogIds:    claim.IDs,
		Pid:           pid,
		UpdatedAt:     isoNow(),
	}
}

// CommandLineOf is a pid's command line (/proc, else `ps`), or false when neither can say.
func CommandLineOf(ctx context.Context, pid int64) (string, bool) {
	line, ok := procCmdline(fmt.Sprintf("/proc/%d/cmdline", pid))
	if !ok {
		line, ok = psCmdline(ctx, pid)
	}
	return line, ok
}

// procCmdline is one /proc cmdline file: NUL-separated argv as one line (the trailing NUL becomes
// a trailing space).
func procCmdline(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.Join(strings.Split(string(data), "\x00"), " "), true
}

// psCmdline is the ps fallback: the pid's command as `ps -p <pid> -o command=` reports it.
func psCmdline(ctx context.Context, pid int64) (string, bool) {
	timeoutMs := int64(2000)
	answer := process.RunToolSync(ctx, "ps", []string{"-p", strconv.FormatInt(pid, 10), "-o", "command="}, process.ToolOptions{TimeoutMs: &timeoutMs})
	if !process.Succeeded(answer) {
		return "", false
	}
	return strings.TrimSpace(string(answer.Stdout)), true
}
