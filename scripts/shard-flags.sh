#!/bin/sh
# shard-flags prints the go test flag that picks the tests one CI shard runs in its package, or nothing when the
# shard runs its whole package. Run it from the repo root:
#   sh scripts/shard-flags.sh core|server|server-a|cmd|cmd-heavy|rest
# server and cmd split in two shards over one package: server-a and cmd-heavy -run a pattern, server and cmd -skip
# the very same pattern, so every test runs in exactly one half. scripts/covergate's tests prove it against
# go test -list. The patterns balance the halves' time: server's tests are a long flat tail, so it splits by the
# first letter of the name; cmd's time sits in a few end-to-end tests, so they are named.

set -eu

server_a='^Test[A-L]'
cmd_heavy='^(TestE2ERouterOverhead|TestE2EHandoverAStartFromANewBinaryKeepsTheRouterPortOpen|TestE2EPhoneSimulator|TestProviderCliE2E|TestE2EAnOwnerMessageReachesOnlyItsSession|TestE2EARouterUpdateKeepsAStreamingTurn|TestE2EAHubRestartKeepsAStreamingTurn|TestE2EChannelJoinSurvivesASlowHub)$'

case "${1:-}" in
core | rest) ;;
server) echo "-skip=$server_a" ;;
server-a) echo "-run=$server_a" ;;
cmd) echo "-skip=$cmd_heavy" ;;
cmd-heavy) echo "-run=$cmd_heavy" ;;
*)
  echo "usage: sh scripts/shard-flags.sh core|server|server-a|cmd|cmd-heavy|rest" >&2
  exit 2
  ;;
esac
