// The OS secret store's runner: the platform tools run without a shell (the real one spawns
// them; tests pass fakes).
package keystore

import (
	"context"

	"github.com/MuhmdRaouf/huddle/internal/adapters/process"
)

// toolTimeoutMs bounds a tool that hangs (a keychain waiting on an unlock prompt nobody answers).
const toolTimeoutMs = 60_000

// SpawnRunner spawns cmd directly (no shell), writes stdin and closes it, and collects stdout
// and stderr. A tool that cannot start answers Missing; nothing fails, and no field carries
// stdin.
func SpawnRunner(ctx context.Context, cmd string, args []string, stdin *string) RunResult {
	timeout := int64(toolTimeoutMs)
	options := process.ToolOptions{TimeoutMs: &timeout}
	if stdin != nil {
		options.Stdin = stdin
	}
	run := process.RunTool(ctx, cmd, args, options)
	if run.Kind == process.ToolMissing {
		return RunResult{Missing: true}
	}
	return RunResult{Code: run.Code, Stdout: string(run.Stdout), Stderr: string(run.Stderr)}
}
