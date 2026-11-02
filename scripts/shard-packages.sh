#!/bin/sh
# shard-packages prints the import paths one CI test shard runs, one per line. Run it from the repo root:
#   sh scripts/shard-packages.sh core|server|server-a|cmd|cmd-overhead|cmd-heavy|cmd-hub|cli|cli-a|rest-heavy|rest
# core is the provider engine; rest is every other package, so a new package lands in rest without a change here.
# Three packages are too slow for one runner each and split over several shards that run the same package:
# server (server, server-a), cmd (cmd, cmd-overhead, cmd-heavy, cmd-hub) and the huddle CLI (cli, cli-a);
# scripts/shard-flags.sh picks the tests of each shard. rest-heavy is six slow packages of rest, named one by one:
# a subpackage of theirs stays in rest.
# scripts/covergate's tests prove no package falls between the shards and only the shards of one split package
# share a package.

set -eu

case "${1:-}" in
core) go list ./internal/core/... ;;
server | server-a) go list ./internal/huddle/server/... ;;
cmd | cmd-overhead | cmd-heavy | cmd-hub) go list ./cmd/huddle/... ;;
cli | cli-a) go list ./internal/huddle/cli ;;
rest-heavy) go list ./internal/huddle/hooks ./internal/huddle/channel ./internal/huddle/hub ./internal/plugins ./internal/adapters ./internal/radar/cost ;;
rest)
  module=$(go list -m)
  subtrees="internal/core|internal/huddle/server|cmd/huddle"
  packages="internal/huddle/cli|internal/huddle/hooks|internal/huddle/channel|internal/huddle/hub|internal/plugins|internal/adapters|internal/radar/cost"
  go list ./... | grep -v -E "^$module/(($subtrees)(/|\$)|($packages)\$)"
  ;;
*)
  echo "usage: sh scripts/shard-packages.sh core|server|server-a|cmd|cmd-overhead|cmd-heavy|cmd-hub|cli|cli-a|rest-heavy|rest" >&2
  exit 2
  ;;
esac
