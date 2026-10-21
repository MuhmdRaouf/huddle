package process

import (
	"os"
	"path/filepath"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// LiveExecutable names a binary that exists now. A long-lived process may outlive its own
// build: the launcher keeps three builds under <home>/src/<commit>/huddle and deletes older
// ones, so a session's bridge or the router daemon can carry a path the disk has lost, and a
// restart from it would fail on a missing file. The answer is the launcher home's `current`
// link first, this process's own executable when it still sits on disk, and os.Args[0] as the
// last answer.
func LiveExecutable(env domain.EnvLookup) string {
	return liveExecutable(env, os.Executable)
}

// liveExecutable is LiveExecutable with the own path injected, so tests name a build that never
// existed instead of the running test binary.
func liveExecutable(env domain.EnvLookup, self func() (string, error)) string {
	if picked := pickLive(liveCandidates(env, self)); picked != "" {
		return picked
	}
	return os.Args[0]
}

// liveCandidate is one spot a start may name, with what the filesystem answered about it.
type liveCandidate struct {
	// path is where the candidate resolves: a `current` link's target, or the path itself.
	path string
	// err is why the filesystem could not answer; nil means path is stat'ed below.
	err error
	// regular and executable come off the stat: an executable regular file is startable.
	regular    bool
	executable bool
	// ownName says the candidate counts only when its base name is huddle: a `current` found
	// beside a build is trusted only when it names a huddle, while the launcher home's own
	// `current` is authoritative as the launcher keeps it.
	ownName bool
}

// pickLive returns the first candidate the filesystem answers with an executable regular file —
// named huddle where the spot demands it — and "" when none does.
func pickLive(cands []liveCandidate) string {
	for _, c := range cands {
		if c.err != nil || !c.regular || !c.executable {
			continue
		}
		if c.ownName && filepath.Base(c.path) != "huddle" {
			continue
		}
		return c.path
	}
	return ""
}

// liveCandidates lists the spots best first: the launcher home's `current`, the `current` links
// at this build's directory's parent and grandparent (`<home>/src/<commit>/huddle` and
// `<home>/<version>/huddle` both sit one or two levels under `<home>`), then this binary itself.
func liveCandidates(env domain.EnvLookup, self func() (string, error)) []liveCandidate {
	var cands []liveCandidate
	link := func(path string, ownName bool) {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			cands = append(cands, liveCandidate{path: path, err: err, ownName: ownName})
			return
		}
		cands = append(cands, statCandidate(resolved, ownName))
	}
	if home := env["AGENTS_HUDDLE_HOME"]; home != "" {
		link(filepath.Join(home, "current"), false)
	}
	selfPath, err := self()
	if err != nil || selfPath == "" {
		return cands
	}
	dir := filepath.Dir(selfPath)
	link(filepath.Clean(filepath.Join(dir, "..", "current")), true)
	link(filepath.Clean(filepath.Join(dir, "..", "..", "current")), true)
	cands = append(cands, statCandidate(selfPath, false))
	return cands
}

// statCandidate stats one path: what it is, or why the filesystem could not answer.
func statCandidate(path string, ownName bool) liveCandidate {
	info, err := os.Stat(path)
	if err != nil {
		return liveCandidate{path: path, err: err, ownName: ownName}
	}
	return liveCandidate{
		path:       path,
		regular:    info.Mode().IsRegular(),
		executable: info.Mode().Perm()&0o111 != 0,
		ownName:    ownName,
	}
}
