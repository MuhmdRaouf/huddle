#!/bin/sh
# reaper.sh <runner-pid> <bin-dir> — outlives a Playwright runner that is killed without its teardown.
# It waits while the runner is alive; once the runner is gone it stops every process whose executable
# sits under <bin-dir>/ (SIGTERM, five seconds, SIGKILL) and exits. It signals nothing else.
runner=$1
bindir=$2

while kill -0 "$runner" 2>/dev/null; do
  sleep 2
done

# The pids of the processes running from the bin folder; their own first word is the executable.
under_bin() {
  ps -axo pid=,command= | awk -v dir="$bindir" 'index($2, dir "/") == 1 { print $1 }'
}

pids=$(under_bin)
[ -n "$pids" ] || exit 0
# shellcheck disable=SC2086
kill -TERM $pids 2>/dev/null
sleep 5
for pid in $(under_bin); do
  kill -KILL "$pid" 2>/dev/null
done
