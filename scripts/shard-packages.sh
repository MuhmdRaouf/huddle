#!/bin/sh
# shard-packages prints the import paths one CI test shard runs, one per line. Run it from the repo root:
#   sh scripts/shard-packages.sh core|server|server-a|cmd|cmd-heavy|rest
# core, server and cmd are the three heaviest suites; rest is every other package, so a new package lands in rest
# without a change here. server and cmd are too slow for one runner each, so each splits in two shards over the same
# package (server and server-a, cmd and cmd-heavy); scripts/shard-flags.sh picks the tests of each half.
# scripts/covergate's tests prove no package falls between the shards and only the two halves of a split package
# share one.

set -eu

case "${1:-}" in
core) go list ./internal/core/... ;;
server | server-a) go list ./internal/huddle/server/... ;;
cmd | cmd-heavy) go list ./cmd/huddle/... ;;
rest)
  module=$(go list -m)
  go list ./... | grep -v -E "^$module/(internal/core|internal/huddle/server|cmd/huddle)(/|\$)"
  ;;
*)
  echo "usage: sh scripts/shard-packages.sh core|server|server-a|cmd|cmd-heavy|rest" >&2
  exit 2
  ;;
esac
