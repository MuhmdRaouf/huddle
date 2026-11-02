#!/bin/sh
# shard-flags prints the go test flag that picks the tests one CI shard runs in its package, or nothing when the
# shard runs its whole package. Run it from the repo root:
#   sh scripts/shard-flags.sh core|server|server-a|cmd|cmd-overhead|cmd-heavy|cmd-hub|cli|cli-a|rest-heavy|rest
# server, cmd and cli split over several shards that run one package. server and cli split by the first letter of
# the test name (server-a and cli-a -run a pattern, server and cli -skip the very same one), because their tests are
# a long flat tail. cmd's time sits in a few end-to-end tests, so they are named: cmd-heavy and cmd-hub -run one
# list each, and cmd -skips all three lists. cmd-overhead runs TestE2ERouterOverhead alone on its runner: the guard
# compares its timings with a same-run reference, which contention from any other test on the runner still skews.
# Every test runs in exactly one shard; scripts/covergate's tests prove it against go test -list.

set -eu

server_a='^Test[A-L]'
cli_a='^Test[A-K]'
cmd_overhead='TestE2ERouterOverhead'
cmd_heavy='TestE2EHandoverAStartFromANewBinaryKeepsTheRouterPortOpen|TestE2EPhoneSimulator|TestProviderCliE2E|TestE2EAnOwnerMessageReachesOnlyItsSession|TestE2EARouterUpdateKeepsAStreamingTurn|TestE2EAHubRestartKeepsAStreamingTurn|TestE2EChannelJoinSurvivesASlowHub'
cmd_hub='TestE2EHubStaysLight|TestDevChannelE2E'

case "${1:-}" in
core | rest-heavy | rest) ;;
server) echo "-skip=$server_a" ;;
server-a) echo "-run=$server_a" ;;
cli) echo "-skip=$cli_a" ;;
cli-a) echo "-run=$cli_a" ;;
cmd) echo "-skip=^($cmd_overhead|$cmd_heavy|$cmd_hub)\$" ;;
cmd-overhead) echo "-run=^($cmd_overhead)\$" ;;
cmd-heavy) echo "-run=^($cmd_heavy)\$" ;;
cmd-hub) echo "-run=^($cmd_hub)\$" ;;
*)
  echo "usage: sh scripts/shard-flags.sh core|server|server-a|cmd|cmd-overhead|cmd-heavy|cmd-hub|cli|cli-a|rest-heavy|rest" >&2
  exit 2
  ;;
esac
