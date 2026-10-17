// The launcher the plugin ships as plugin/dist/run: Claude Code's hooks, commands, agents and the MCP
// server start it with `sh "${CLAUDE_PLUGIN_ROOT}/dist/run" <entry> [args...]`. It maps the entry to the
// huddle binary's word and runs it on the installed binary, which the launcher itself downloads and
// checksum-verifies on first use of any entry — huddle has no setup command. The satellite words ride the
// same launcher: `radar <args>` and `kanban <args>` run radar's and kanban's command lines in the one
// binary. There is no JS fallback: with no binary available and none downloadable the launcher says why and
// exits 127, and a hook never blocks or fails Claude Code — a hook (`hook <name>`, or the word `hook` in
// radar's and kanban's hook lines) exits 0 at once, starting one detached download of the binary, locked so
// all hooks share it. scripts/build.mjs renders plugin/dist/run from this spec and the committed manifest.
import { readdirSync, readFileSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";

/** The GitHub repository every release downloads from. */
const REPO = "MuhmdRaouf/huddle";

/** The release the launcher pins (the newest `v<semver>.json` in `releaseDir`): its version, the
 *  repository its assets download from, and every target's sha256. Null while no manifest exists: the
 *  launcher then has nothing to pin, and generating one throws — a broken release must stop the build, not
 *  ship quietly. */
export function releaseInfo(releaseDir) {
  let names = [];
  try {
    names = readdirSync(releaseDir);
  } catch (error) {
    if (error?.code !== "ENOENT" && error?.code !== "ENOTDIR") throw error;
    return null;
  }
  const manifests = [];
  for (const name of names) {
    const match = /^v(\d+)\.(\d+)\.(\d+)\.json$/.exec(name);
    if (match !== null) manifests.push({ name, parts: match.slice(1).map(Number) });
  }
  if (manifests.length === 0) return null;
  manifests.sort((a, b) => a.parts[0] - b.parts[0] || a.parts[1] - b.parts[1] || a.parts[2] - b.parts[2]);
  const manifest = JSON.parse(readFileSync(join(releaseDir, manifests.at(-1).name), "utf8"));
  const sha256 = {};
  for (const [target, asset] of Object.entries(manifest.assets ?? {})) sha256[target] = asset.sha256;
  return { version: manifest.version, repo: REPO, sha256 };
}

/** The launcher spec for the huddle binary: one binary, no setup command, so every entry downloads it on
 *  first use — a command or mcp waits for the download (one stderr line, exit 127 on failure; mcp carries
 *  its own 60 s and exit 1, since Claude Code starts an MCP server and waits for it), and a hook starts one
 *  detached download and exits 0 at once. `release` is the release to run (releaseInfo's result over the
 *  repository's release dir); without one there is no launcher to generate and this throws. */
export function huddleSpec(release) {
  if (release === null)
    throw new Error(
      "no huddle release to pin: run go run ./scripts/build -manifest and commit release/v<VERSION>.json",
    );
  return {
    header: `# huddle: starts the huddle binary — the CLI's words, the radar and kanban words, mcp, serve and
#   the hooks — downloading and verifying it on first use; huddle has no setup command.
#   sh "\${CLAUDE_PLUGIN_ROOT}/dist/run" <command> [args...]
# huddle lives under \${AGENTS_HUDDLE_HOME:-$HOME/.agents/huddle-bin}/${release.version}; the first
# command downloads it from ${release.repo}'s GitHub releases and verifies its sha256 there
# (HUDDLE_DOWNLOAD_BASE overrides the release URL for tests), waiting up to 120 s — 60 s for mcp,
# which Claude Code starts and waits for. A hook never blocks or fails Claude Code: it exits 0 at
# once, starting one detached download — guarded by a lock in the version dir so every hook shares
# it — that installs the binary for the next hook.`,
    name: "huddle",
    binary: "huddle",
    downloadFn: "download_huddle",
    tagPrefix: "v",
    version: release.version,
    repo: release.repo,
    sha256: release.sha256,
    argName: "command",
    // Every entry point the binary carries (cmd/huddle/entries.go): the cli wrapper entry, whose CLI word
    // follows it so it adds none of its own; the CLI's own words; the two satellite words, whose command
    // lines follow them; the two servers; the hooks, whose name follows `hook`.
    entries: [
      ["cli", ""],
      ...selfEntries([
        "help",
        "-h",
        "setup",
        "up",
        "down",
        "server",
        "token",
        "members",
        "kick",
        "open",
        "join",
        "whoami",
      ]),
      ...selfEntries([
        "listen",
        "map",
        "assign",
        "brief",
        "wait",
        "wait-task",
        "gate",
        "ack",
        "send",
        "reply",
        "pub",
        "doing",
        "done",
        "blocked",
        "skipped",
        "todo",
        "new",
        "start",
        "finish",
        "depend",
        "handoff",
        "ask",
        "remember",
        "verify",
        "share",
        "knowledge",
        "recall",
        "kb",
        "task",
        "pause",
        "resume",
        "pass",
        "leave",
        "ops",
      ]),
      ["mcp", "mcp"],
      ["serve", "serve"],
      ["hook", "hook"],
      ["radar", "radar"],
      ["kanban", "kanban"],
    ],
    hookFlag: false,
    hookEntries: ["hook"],
    binaryArg: "",
    homeEnv: "AGENTS_HUDDLE_HOME",
    homeDir: "$HOME/.agents/huddle-bin",
    baseEnv: "HUDDLE_DOWNLOAD_BASE",
    fetch: "always",
    tuned: { mcp: { timeout: 60, failStatus: 1 } },
    timeout: 120,
    failStatus: 127,
    missing: `huddle: no huddle ${release.version} binary for this platform ($os-$arch)`,
  };
}

/** Each word as its own entry pair: the word is its own subcommand. */
const selfEntries = (words) => words.map((word) => [word, word]);

/** The case mapping of the first argument: one line per entry, or one line naming them all when every entry
 *  maps to itself. */
function caseMapping(spec) {
  if (spec.entries.every(([arg, entry]) => arg === entry))
    return `  ${spec.entries.map(([arg]) => arg).join("|")}) entry=$${spec.argName} ;;`;
  return spec.entries.map(([arg, entry]) => `  ${arg}) entry=${entry} ;;`).join("\n");
}

/** The exec line every dispatch point shares: the binary, the entry and the caller's arguments. An entry
 *  that adds no word of its own (huddle's cli, whose CLI word follows it) expands to nothing here rather
 *  than to an empty argument. */
const execLine = (spec) => {
  const entry = spec.entries.some(([, word]) => word === "") ? '${entry:+"$entry"}' : '"$entry"';
  return `exec "$bin" ${spec.binaryArg}${entry} "$@"`;
};

/** status=$? and the exec when a foreground download landed its binary. */
function downloadRun(spec, pad) {
  return `${pad}status=$?
${pad}if [ "$status" = 0 ]; then
${pad}  ${execLine(spec)}
${pad}fi`;
}

/** The exit a failed foreground download returns: the spec's own, or a per-entry pick where the spec tunes
 *  one (huddle's mcp exits 1: any nonzero status tells Claude Code its MCP server never started). */
function failExit(spec, pad) {
  const tuned = Object.entries(spec.tuned ?? {}).filter(([, tune]) => tune.failStatus !== undefined);
  if (tuned.length === 0) return `${pad}exit ${spec.failStatus}`;
  return [
    `${pad}code=${spec.failStatus}`,
    ...tuned.map(([entry, tune]) => `${pad}[ "$entry" = ${entry} ] && code=${tune.failStatus}`),
    `${pad}exit $code`,
  ].join("\n");
}

/** The two one-line failures and the exit when it did not. */
function downloadReport(spec, pad) {
  return `${pad}if [ "$status" = 2 ]; then
${pad}  echo "${spec.name}: ${spec.binary} download failed its checksum" >&2
${pad}else
${pad}  echo "${spec.name}: could not download ${spec.binary} ${spec.version} ($url)" >&2
${pad}fi
${failExit(spec, pad)}`;
}

/** The foreground download call with the timeout it runs under: the spec's own, or a per-entry pick where
 *  the spec tunes one (huddle's mcp downloads with 60 s: Claude Code starts an MCP server and waits for it). */
function downloadCall(spec, pad) {
  const tuned = Object.entries(spec.tuned ?? {}).filter(([, tune]) => tune.timeout !== undefined);
  if (tuned.length === 0) return `${pad}${spec.downloadFn} ${spec.timeout}`;
  return [
    `${pad}timeout=${spec.timeout}`,
    ...tuned.map(([entry, tune]) => `${pad}[ "$entry" = ${entry} ] && timeout=${tune.timeout}`),
    `${pad}${spec.downloadFn} "$timeout"`,
  ].join("\n");
}

/** The every-entry foreground download (spec.fetch "always"): a command without a binary downloads it itself,
 *  then runs it or says why in one line. */
function eagerFetch(spec) {
  return `${downloadCall(spec, "  ")}
${downloadRun(spec, "  ")}
${downloadReport(spec, "  ")}`;
}

/** The launcher script for the spec (huddleSpec's result): the entry's word on the installed binary, which
 *  the launcher itself downloads and checksum-verifies; a hook whose binary is missing starts one detached,
 *  lock-guarded download so the next hook finds it installed. */
export function launcherScript(spec) {
  const hookInit = spec.hookEntries.map((entry) => `[ "$entry" = ${entry} ] && hook=1`);
  const hookLines = ["hook=", ...hookInit, "detached="].join("\n");
  // A hook is huddle's own `hook` entry, or the word `hook` in the arguments — radar's `radar hook` and
  // kanban's `kanban hook session-start` carry it second, where the entry alone cannot see it. A stray
  // `hook` in another command's arguments only moves the missing-binary case from exit 127 to the silent
  // hook path, never changes a run that finds the binary.
  const scanBody = `  [ "$arg" = hook ] && hook=1
  [ "$arg" = "--detached-fetch" ] && detached=1`;
  const dispatch = [
    `if [ "$os" != unsupported ] && [ "$arch" != unsupported ]; then
  if [ -x "$bin" ]; then
    ${execLine(spec)}
  fi`,
    `  if [ -n "$detached" ]; then
    fetch_under_lock
    exit 0
  fi`,
    `  if [ -n "$hook" ]; then
    # One detached download for every hook: it re-runs this script on $0 with --detached-fetch, so the fetch
    # reuses the same URL and checksum table; it ignores HUP and outlives the hook that started it.
    (
      trap '' HUP
      sh_cmd=$(command -v sh 2>/dev/null) || sh_cmd=/bin/sh
      exec "$sh_cmd" "$0" "$${spec.argName}" --detached-fetch
    ) >/dev/null 2>&1 </dev/null &
    exit 0
  fi`,
    eagerFetch(spec),
    "fi",
  ].join("\n");
  return `#!/bin/sh
${spec.header}
# Generated by scripts/launcher.mjs; do not edit.
dir=$(dirname -- "$0")
${spec.argName}=$1
shift
case "$${spec.argName}" in
${caseMapping(spec)}
  *)
    echo "${spec.name}: unknown ${spec.argName} $${spec.argName}" >&2
    exit 2
    ;;
esac
${hookLines}
for arg in "$@"; do
${scanBody}
done
os=$(uname -s)
case "$os" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) os=unsupported ;;
esac
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) arch=unsupported ;;
esac
home=\${${spec.homeEnv}:-${spec.homeDir}}
bin=$home/${spec.version}/${spec.binary}
base=\${${spec.baseEnv}:-https://github.com/${spec.repo}/releases/download}
url=$base/${spec.tagPrefix}${spec.version}/${spec.binary}-$os-$arch
want=
case "$os-$arch" in
${Object.entries(spec.sha256)
  .map(([target, hex]) => `  ${target}) want=${hex} ;;`)
  .join("\n")}
esac

# ${spec.downloadFn} <timeout>: fetch the release asset, verify its sha256 and install it as $bin. Returns 0 when
# the binary is installed, 1 when the asset could not be fetched, 2 when it failed its checksum.
${spec.downloadFn}() {
  timeout=$1
  mkdir -p "$home/${spec.version}"
  tmp=$home/${spec.version}/.${spec.binary}.$$.download
  got=
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --max-time "$timeout" -o "$tmp" "$url" && got=1
  elif command -v wget >/dev/null 2>&1; then
    wget -q -T "$timeout" -O "$tmp" "$url" && got=1
  fi
  if [ -z "$got" ]; then
    rm -f "$tmp"
    return 1
  fi
  sum=
  if command -v sha256sum >/dev/null 2>&1; then sum=$(sha256sum "$tmp")
  elif command -v shasum >/dev/null 2>&1; then sum=$(shasum -a 256 "$tmp")
  elif command -v openssl >/dev/null 2>&1; then sum=$(openssl dgst -sha256 -r "$tmp")
  fi
  if [ -n "$want" ] && [ "\${sum%% *}" = "$want" ]; then
    chmod 755 "$tmp"
    mv -f "$tmp" "$bin"
    return 0
  fi
  rm -f "$tmp"
  return 2
}

# fetch_under_lock: the download a detached fetch runs. The version dir's lock holds the fetcher's pid and
# start time ("<pid> <epoch seconds>"); another fetch exits while that pid lives and the lock is under 120 s
# old, and takes over a lock whose holder died or stalled, so one download serves every hook.
fetch_under_lock() {
  trap '' HUP
  mkdir -p "$home/${spec.version}"
  lock=$home/${spec.version}/.${spec.binary}.fetch.lock
  now=$(date +%s)
  if (set -C; echo "$$ $now" >"$lock") 2>/dev/null; then
    :
  else
    read pid when <"$lock" || :
    if [ -z "$pid" ]; then
      sleep 1
      read pid when <"$lock" || :
    fi
    case "$pid" in *[!0-9]*|"") pid=0 ;; esac
    case "$when" in *[!0-9]*|"") when=0 ;; esac
    if [ "$pid" -ne 0 ] && kill -0 "$pid" 2>/dev/null && [ $(( $(date +%s) - when )) -lt 120 ]; then
      return 0
    fi
    rm -f "$lock"
    (set -C; echo "$$ $now" >"$lock") 2>/dev/null || return 0
  fi
  if [ -x "$bin" ]; then
    rm -f "$lock"
    return 0
  fi
  ${spec.downloadFn} ${spec.timeout}
  rm -f "$lock"
  return 0
}

${dispatch}
[ -n "$hook" ] && exit 0
[ -n "$detached" ] && exit 0
echo "${spec.missing}" >&2
exit ${spec.failStatus}
`;
}

// Run directly, scripts/launcher.mjs renders plugin/dist/run from the committed manifest:
// `node scripts/launcher.mjs` writes it (mode 755), `--check` renders in memory and byte-compares, so a
// launcher stale against the manifest fails the check instead of shipping.
const invoked = process.argv[1] ? import.meta.url === pathToFileURL(resolve(process.argv[1])).href : false;
if (invoked) {
  const root = join(import.meta.dirname, "..");
  const script = launcherScript(huddleSpec(releaseInfo(join(root, "release"))));
  const target = join(root, "plugin", "dist", "run");
  if (process.argv.includes("--check")) {
    let committed = null;
    try {
      committed = readFileSync(target, "utf8");
    } catch (error) {
      if (error?.code !== "ENOENT") throw error;
    }
    if (committed !== script) {
      console.error("plugin/dist/run is stale against release/v<VERSION>.json; run: npm run build:launcher");
      process.exit(1);
    }
  } else {
    writeFileSync(target, script, { mode: 0o755 });
    console.error("wrote plugin/dist/run");
  }
}
