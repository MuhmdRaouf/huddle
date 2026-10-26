package domain

import (
	"fmt"
	"path/filepath"
)

// JobPaths is one job's files: its directory under `jobs/`, the sidecars beside job.json, and its
// worktree.
type JobPaths struct {
	Dir string
	// The job record, written only by the job store.
	Record string
	Brief  string
	// The attempt's artifacts directory.
	Artifacts string
	// The detached driver's stdout and stderr.
	DriverLog string
	// Present while a stop was requested and not yet acted on.
	Stop string
	// The driver's progress snapshot, for readers that are not the driver.
	Progress string
	// Held by the job's driver; holds its pid.
	DriverLock string
	// The delegation engine's process group leader, while it runs.
	EnginePid string
	Worktree  string
	// The fresh detached checkout an accept verifies the commit to land in; it exists only while
	// the accept runs.
	Checkout string
}

// AttemptLog is attempt n's event log beside job.json.
func (p JobPaths) AttemptLog(n int) string {
	return filepath.Join(p.Dir, fmt.Sprintf("attempt-%d.jsonl", n))
}

// GateLog is gate `gate` of attempt n's log.
func (p JobPaths) GateLog(n, gate int) string {
	return filepath.Join(p.Dir, fmt.Sprintf("gate-%d-%d.log", n, gate))
}

// RegenerateLog is a brief's `regenerate` rule's output while landing, and a gate's in the
// landing check.
func (p JobPaths) RegenerateLog(n int) string {
	return filepath.Join(p.Dir, fmt.Sprintf("regenerate-%d.log", n))
}

// LandCheckLog is the landing check's log for attempt n.
func (p JobPaths) LandCheckLog(n int) string {
	return filepath.Join(p.Dir, fmt.Sprintf("land-check-%d.log", n))
}

// StateLayout names every file and directory a plugin keeps under its state root, named once.
// Nothing else in the codebase spells a state file's name; the setup ledger reads the same names
// here. Pure paths (path/filepath only).
type StateLayout struct {
	Root string
	// Setup ran (and was not removed); the hooks act only once it exists.
	SetupDone string
	// Which delegation engines are enabled and who watches each.
	Engines string
	// Every change setup made outside the plugin, so removal undoes exactly those.
	Ledger string
	// The ledger of a disabled plugin, kept for its re-enable.
	DisabledLedger string
	// The CLAUDE_CONFIG_DIR the claude worker runs with.
	ClaudeHome string
	// The adaptive concurrency limiter's shared state.
	Limiter string
	// The semaphore's slot and ticket files.
	Slots string
	Jobs  string
	// The accepts' temporary checkouts, one per job id.
	Checkouts string
	// The local CA every plugin's router shares, next to the state roots (~/.agents/ca): it signs
	// only api.anthropic.com, and never enters a system trust store.
	CA string
}

const enginePidFilename = "engine.pid"

// EnginePidBeside finds the job's engine.pid from one of its attempt logs (a worker knows its
// log, not its job).
func EnginePidBeside(attemptLog string) string {
	return filepath.Join(filepath.Dir(attemptLog), enginePidFilename)
}

// NewStateLayout lays out every file a plugin keeps under root.
func NewStateLayout(root string) StateLayout {
	jobs := filepath.Join(root, "jobs")
	checkouts := filepath.Join(root, "checkouts")
	return StateLayout{
		Root:           root,
		SetupDone:      filepath.Join(root, "setup-done"),
		Engines:        filepath.Join(root, "engines.json"),
		Ledger:         filepath.Join(root, "ledger.json"),
		DisabledLedger: filepath.Join(root, "ledger.disabled.json"),
		ClaudeHome:     filepath.Join(root, "claude-home"),
		Limiter:        filepath.Join(root, "limiter.json"),
		Slots:          filepath.Join(root, "slots"),
		Jobs:           jobs,
		Checkouts:      checkouts,
		CA:             filepath.Join(filepath.Dir(root), "ca"),
	}
}

// Job lays out one job's files under jobs/.
func (l StateLayout) Job(id string) JobPaths {
	dir := filepath.Join(l.Jobs, id)
	return JobPaths{
		Dir:        dir,
		Record:     filepath.Join(dir, "job.json"),
		Brief:      filepath.Join(dir, "brief.md"),
		Artifacts:  filepath.Join(dir, "artifacts"),
		DriverLog:  filepath.Join(dir, "driver.log"),
		Stop:       filepath.Join(dir, "stop"),
		Progress:   filepath.Join(dir, "progress.json"),
		DriverLock: filepath.Join(dir, "driver.lock"),
		EnginePid:  filepath.Join(dir, enginePidFilename),
		Worktree:   filepath.Join(l.Root, "worktrees", id),
		Checkout:   filepath.Join(l.Checkouts, id),
	}
}
