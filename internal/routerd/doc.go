// Package routerd runs huddle-router as its own daemon: one process per user serves the one
// router port (127.0.0.1:18786) for every enabled provider's lane, for Claude itself, and for the
// gateway mounted under /gateway. The hub is a separate process beside it, so a hub restart,
// panic or memory ceiling never touches the router. Whoever holds the router lock
// (~/.agents/huddle-router/run/router.lock) serves the port; every other start finds it held
// and steps aside. The lanes follow providers.json in the same home, re-read every poll, so
// enabling a provider needs no restart. A start from another binary and a daemon past its RSS
// ceiling change the serving process by handover: the listening socket passes to the taker over
// a unix socket in the run dir, the port never closes, and the old process drains the streams it
// already accepted. `huddle router serve` runs Run in the foreground, and `huddle router start`,
// `huddle up`, the providers' session start and the session watchdog make sure one runs
// detached.
package routerd
