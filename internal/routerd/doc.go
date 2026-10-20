// Package routerd hosts huddle-router as a daemon: one process per user serves the one router
// port (127.0.0.1:18786) for every enabled provider's lane, for Claude itself, and for the
// gateway mounted under /gateway. Whoever holds the router lock
// (~/.agents/huddle-router/run/router.lock) serves the port; every other start finds it held
// and steps aside. The lanes follow providers.json in the same home, re-read every poll, so
// enabling a provider needs no restart. `huddle router serve` runs Run in the foreground,
// `huddle router start` makes sure one runs detached, and `huddle serve` (the hub) hosts the
// same Run in its own process when no daemon has the lock.
package routerd
