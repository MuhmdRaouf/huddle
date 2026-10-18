package server

// The owner's key: the root credential of a server no CLI handed one to — a service manager's
// (a launchd agent, a systemd unit) or a terminal's `huddle serve` — written in the home's run
// directory the way Jupyter's runtime file and the Docker socket hand the same machine's owner
// their rights. The CLI of that machine alone reads it (cli's creds.go): the hub listener
// serves no path to it, and a satellite, which runs from another machine's home, never sees it.

import (
	"context"
	"os"

	"github.com/MuhmdRaouf/huddle/internal/adapters/fsutil"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// writeOwnerKey lays the run's root credential down for this machine's owner: 0600 inside the
// 0700 run directory, whole through a temp file and a rename, so a reader never sees half of it
// and a crash never leaves a torn one.
func writeOwnerKey(ctx context.Context, homeDir, root string) error {
	_, err := fsutil.WriteFileAtomic(ctx, home.Home{Root: homeDir}.OwnerKey(),
		[]byte(root+"\n"), fsutil.WriteOptions{Mkdir: true})
	return err
}

// removeOwnerKey takes the owner's key away: no root of a server that no longer runs.
func removeOwnerKey(homeDir string) {
	_ = os.Remove(home.Home{Root: homeDir}.OwnerKey())
}
