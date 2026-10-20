// The launcher the plugin ships as plugin/dist/run: Claude Code's hooks, commands, agents and the MCP
// server start it with `sh "${CLAUDE_PLUGIN_ROOT}/dist/run" <entry> [args...]`. It maps the entry to the
// huddle binary's word and runs it on the installed binary, which the launcher itself downloads and
// checksum-verifies on first use of any entry — huddle has no setup command. While no release is
// published, the launcher instead builds the binary of the checkout's own commit: $HUDDLE_SOURCE, the
// first directory at or above the launcher's plugin dir whose go.mod names the module, or the marketplace
// checkout under CLAUDE_CONFIG_DIR — once per commit, hooks starting that build detached. The satellite
// words ride the same launcher: `radar <args>` and `kanban <args>` run radar's and kanban's command lines
// in the one binary. There is no JS fallback: with no binary available and none downloadable the launcher
// says why and exits 127, and a hook never blocks or fails Claude Code — a hook (`hook <name>`, or the
// word `hook` in radar's and kanban's hook lines) exits 0 at once, starting one detached download or
// build of the binary, locked so all hooks share it. scripts/launcher.mjs renders plugin/dist/run from
// this spec and the committed manifest.
import { mkdirSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";

/** The GitHub repository every release downloads from. */
const REPO = "MuhmdRaouf/huddle";

/** The provider plugins this repository ships, each with a dist/run of its own. */
export const PROVIDERS = ["zai", "kimi", "deepseek", "minimax", "qwen"];

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
# which Claude Code starts and waits for. While no release is published, the launcher instead builds
# the binary of the checkout's own commit: $HUDDLE_SOURCE, the first directory at or above the
# plugin dir whose go.mod names the module, or the marketplace checkout under CLAUDE_CONFIG_DIR —
# once per commit, under a lock so one build serves every invocation, and a hook starts it detached.
# Without Go the release download stays the path, its failure naming the install-Go line. Every
# verified install also points $home/current at the binary — the stable path a harness config names,
# good across upgrades. A hook never blocks or fails Claude Code: it exits 0 at once, starting one
# detached download or build — guarded by a lock so every hook shares it — that installs the binary
# for the next hook.`,
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
        "status",
      ]),
      ["mcp", "mcp"],
      ["serve", "serve"],
      ["hook", "hook"],
      ["radar", "radar"],
      ["kanban", "kanban"],
      ["gateway", "gateway"],
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

/** The module whose sources the launcher builds while no release is published. */
const HUDDLE_MODULE = "github.com/MuhmdRaouf/huddle";

/** The launcher spec for one provider plugin: the same huddle binary, version and checksum table
 *  the huddle launcher pins, exec'd as `huddle provider <p> <entry>` — the bundle name the
 *  commands run (<p>.js) maps onto the provider engine's cli entry. Like the huddle launcher there
 *  is no setup command: every bundle downloads the binary on first use, and a hook — any --hook
 *  argument, or the word `hook` — exits 0 at once, starting one detached download. `release` is
 *  releaseInfo's result over the repository's release dir; without one there is no launcher to
 *  generate and this throws. */
export function providerSpec(release, provider) {
  if (release === null)
    throw new Error(
      "no huddle release to pin: run go run ./scripts/build -manifest and commit release/v<VERSION>.json",
    );
  return {
    header: `# ${provider}: runs the ${provider} plugin's bundles on the huddle binary (huddle provider ${provider}
#   <entry>), downloading and verifying it on first use.
#   sh "\${CLAUDE_PLUGIN_ROOT}/dist/run" <bundle>.js [args...]
# huddle lives under \${AGENTS_HUDDLE_HOME:-$HOME/.agents/huddle-bin}/${release.version}; the first
# command downloads it from ${release.repo}'s GitHub releases and verifies its sha256 there
# (HUDDLE_DOWNLOAD_BASE overrides the release URL for tests), waiting up to 120 s. While no release
# is published, the launcher instead builds the binary of the checkout's own commit: $HUDDLE_SOURCE,
# the first directory at or above the plugin dir whose go.mod names the module, or the marketplace
# checkout under CLAUDE_CONFIG_DIR — once per commit, under a lock so one build serves every
# invocation, and a hook starts it detached. Without Go the release download stays the path, its
# failure naming the install-Go line. Every verified install also points $home/current at the
# binary — the stable path a harness config names, good across upgrades. A hook never blocks or
# fails Claude Code: it exits 0 at once, starting one detached download or build — guarded by a
# lock so every hook shares it — that installs the binary for the next hook.`,
    name: provider,
    binary: "huddle",
    downloadFn: "download_huddle",
    tagPrefix: "v",
    version: release.version,
    repo: release.repo,
    sha256: release.sha256,
    argName: "bundle",
    // The one bundle the commands run (huddle's hooks run the providers' work) onto the
    // provider engine's cli entry; the fixed `provider <p>` words ride binaryArg, so the
    // entry stays one word.
    entries: [
      [`${provider}.js`, "cli"],
    ],
    hookFlag: false,
    hookEntries: [],
    // A provider hook carries its flag in the arguments (`<p>.js setup --hook`); the word `hook`
    // stays a hook spelling, as in the huddle launcher.
    hookFlags: ["--hook"],
    // The CLI reads the plugin root from the environment, and plain shells do not export it:
    // default it to the plugin dir above this launcher's own.
    prelude: `: "\${CLAUDE_PLUGIN_ROOT:=$(cd -- "$dir/.." && pwd)}"\nexport CLAUDE_PLUGIN_ROOT`,
    // The provider engine's fixed words before the entry: `huddle provider <p> <entry>`.
    binaryArg: `provider ${provider} `,
    homeEnv: "AGENTS_HUDDLE_HOME",
    homeDir: "$HOME/.agents/huddle-bin",
    baseEnv: "HUDDLE_DOWNLOAD_BASE",
    fetch: "always",
    timeout: 120,
    failStatus: 127,
    missing: `${provider}: no huddle ${release.version} binary for this platform ($os-$arch)`,
  };
}

/** The case mapping of the first argument: one line per entry, or one line naming them all when every entry
 *  maps to itself. */
function caseMapping(spec) {
  if (spec.entries.every(([arg, entry]) => arg === entry))
    return `  ${spec.entries.map(([arg]) => arg).join("|")}) entry=$${spec.argName} ;;`;
  return spec.entries.map(([arg, entry]) => `  ${arg}) entry=${entry} ;;`).join("\n");
}

/** The exec line every dispatch point shares: a binary variable, the entry and the caller's arguments.
 *  An entry that adds no word of its own (huddle's cli, whose CLI word follows it) expands to nothing
 *  here rather than to an empty argument. */
const execLine = (spec, bin = '"$bin"') => {
  const entry = spec.entries.some(([, word]) => word === "") ? '${entry:+"$entry"}' : '"$entry"';
  return `exec ${bin} ${spec.binaryArg}${entry} "$@"`;
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

/** The source step all six launchers share, the development channel's path while no release is
 *  published: build_from_source finds the repository root, execs the commit's binary or has its build
 *  started; build_under_lock builds under the commit dir's lock; prune_src_builds keeps the three
 *  newest builds. The release path keeps its own error, plus the install-Go line. */
function sourceStep(spec) {
  const exec = execLine(spec, '"$src_bin"');
  const repoint = `if command -v ln >/dev/null 2>&1; then
      ln -s "src/$src_commit/huddle" "$home/.current.$$" && mv -f "$home/.current.$$" "$home/current"
    fi`;
  return `# huddle_mod <path>: whether the go.mod at <path> names the huddle module — the source step's match test.
huddle_mod() {
  [ -f "$1" ] || return 1
  read -r mod_line <"$1" || return 1
  [ "$mod_line" = "module ${HUDDLE_MODULE}" ]
}

# prune_src_builds keeps the three newest commit builds under $home/src; older ones go, oldest first.
prune_src_builds() {
  [ -d "$home/src" ] || return 0
  for old in $(ls -t "$home/src" 2>/dev/null | tail -n +4); do
    rm -rf "$home/src/$old"
  done
}

# build_under_lock: the source build the words wait for and a detached hook starts. The commit dir's
# lock holds the builder's pid and start time ("<pid> <epoch seconds>"); another builder's lock makes
# this build return 0 without building — the caller waits for its binary — and a lock whose holder
# died or stalled is taken over, so one build serves every invocation. The build's output lands in
# <commit dir>/build.log; a build that lands moves the binary to $src_bin, repoints $home/current and
# prunes older builds; one that fails returns 1, its own caller owing no wait.
build_under_lock() {
  trap '' HUP
  mkdir -p "$src_dir"
  lock=$src_dir/.huddle.build.lock
  now=$(date +%s)
  if (set -C; echo "$$ $now" >"$lock") 2>/dev/null; then
    :
  else
    read -r pid when <"$lock" || :
    if [ -z "$pid" ]; then
      sleep 1
      read -r pid when <"$lock" || :
    fi
    case "$pid" in *[!0-9]*|"") pid=0 ;; esac
    case "$when" in *[!0-9]*|"") when=0 ;; esac
    if [ "$pid" -ne 0 ] && kill -0 "$pid" 2>/dev/null && [ $(( $(date +%s) - when )) -lt 600 ]; then
      return 0
    fi
    rm -f "$lock"
    (set -C; echo "$$ $now" >"$lock") 2>/dev/null || return 0
  fi
  if [ -x "$src_bin" ]; then
    rm -f "$lock"
    return 0
  fi
  tmp=$src_dir/.huddle.$$.build
  if (cd -- "$src_root" && CGO_ENABLED=0 go build -trimpath -o "$tmp" ./cmd/huddle) >>"$src_dir/build.log" 2>&1; then
    chmod 755 "$tmp"
    mv -f "$tmp" "$src_bin"
    ${repoint}
    prune_src_builds
    rm -f "$tmp"
    rm -f "$lock"
    return 0
  fi
  rm -f "$tmp"
  rm -f "$lock"
  return 1
}

# build_from_source: run the binary of the checkout's own commit while no release is published. The
# repository root is $HUDDLE_SOURCE, or the first directory at or above the launcher's plugin dir
# whose go.mod names the module, or the marketplace checkout Claude Code keeps under CLAUDE_CONFIG_DIR;
# a root git cannot name a commit of skips the step. A built binary execs at once. With Go on PATH a
# missing one is built under the commit dir's lock — a hook leaves that to one detached run, every
# other word waits up to 300 s and names the build log on failure — and without Go, src_nogo has the
# release path add its install-Go line to the failure it prints anyway.
build_from_source() {
  src_root=\${HUDDLE_SOURCE:-}
  if [ -z "$src_root" ]; then
    src_walk=$(cd -- "$dir/.." 2>/dev/null && pwd) || src_walk=
    while [ -n "$src_walk" ]; do
      if huddle_mod "$src_walk/go.mod"; then
        src_root=$src_walk
        break
      fi
      [ "$src_walk" = / ] && break
      src_walk=$(dirname -- "$src_walk")
    done
  fi
  if [ -z "$src_root" ]; then
    src_market=\${CLAUDE_CONFIG_DIR:-$HOME/.claude}/plugins/marketplaces/muhmdraouf
    if huddle_mod "$src_market/go.mod"; then src_root=$src_market; fi
  fi
  [ -n "$src_root" ] || return 0
  src_commit=$(git -C "$src_root" rev-parse --short=12 HEAD 2>/dev/null) || { src_root=; return 0; }
  [ -n "$src_commit" ] || { src_root=; return 0; }
  src_dir=$home/src/$src_commit
  src_bin=$src_dir/huddle
  if [ -x "$src_bin" ]; then
    ${repoint}
    ${exec}
  fi
  command -v go >/dev/null 2>&1 || { src_nogo=1; return 0; }
  if [ -n "$detached" ]; then
    build_under_lock
    exit 0
  fi
  if [ -n "$hook" ]; then
    # the build starts here even when an older release binary is installed: the dispatch below may
    # exec that binary for this hook, and the next hook finds the commit's own build
    (
      trap '' HUP
      sh_cmd=$(command -v sh 2>/dev/null) || sh_cmd=/bin/sh
      exec "$sh_cmd" "$0" "$command" --detached-fetch
    ) >/dev/null 2>&1 </dev/null &
    src_build=1
    return 0
  fi
  build_under_lock
  if [ "$?" -eq 0 ] && [ ! -x "$src_bin" ]; then
    # another builder holds the commit dir's lock: its binary is this word's binary, so wait for it
    waited=0
    while [ ! -x "$src_bin" ] && [ "$waited" -lt 300 ]; do
      sleep 1
      waited=$((waited + 1))
    done
  fi
  if [ -x "$src_bin" ]; then
    ${repoint}
    ${exec}
  fi
  echo "${spec.name}: building huddle from $src_root failed; see $src_dir/build.log" >&2
  exit 1
}`;
}

/** The launcher script for the spec (huddleSpec's or providerSpec's result): the entry's word on the installed
 *  binary, which the launcher itself downloads and checksum-verifies — or, while no release is published,
 *  builds from the checkout's own commit; a hook whose binary is missing starts one detached, lock-guarded
 *  download or build so the next hook finds it installed. */
export function launcherScript(spec) {
  const hookInit = spec.hookEntries.map((entry) => `[ "$entry" = ${entry} ] && hook=1`);
  const hookLines = ["hook=", ...hookInit, "detached="].join("\n");
  // A hook is huddle's own `hook` entry, or the word `hook` in the arguments — a satellite's hook
  // line (`radar hook`, `kanban hook session-start`) carries it second, where the entry alone cannot
  // see it. A stray `hook` in another command's arguments only moves the missing-binary case from
  // exit 127 to the silent hook path, never changes a run that finds the binary. A spec may name
  // further argument words that mark a hook (a provider's hooks carry `--hook`).
  const scanBody = [
    `  [ "$arg" = hook ] && hook=1`,
    ...(spec.hookFlags ?? []).map((flag) => `  [ "$arg" = ${flag} ] && hook=1`),
    `  [ "$arg" = "--detached-fetch" ] && detached=1`,
  ].join("\n");
  // Lines between the launcher's own dir and the argument split: a provider launcher defaults and
  // exports CLAUDE_PLUGIN_ROOT, the plugin dir above it.
  const prelude = spec.prelude === undefined ? "" : `${spec.prelude}\n`;
  const dispatch = [
    `if [ "$os" != unsupported ] && [ "$arch" != unsupported ]; then
  if [ -x "$bin" ]; then
    ${execLine(spec)}
  fi`,
    `  if [ -n "$detached" ]; then
    fetch_under_lock
    exit 0
  fi`,
    `  [ -n "$hook" ] && [ -n "$src_build" ] && exit 0
  if [ -n "$hook" ]; then
    # One detached fetch for every hook: it re-runs this script on $0 with --detached-fetch, so the
    # release download or the source build reuses the same steps; it ignores HUP and outlives the
    # hook that started it.
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
${prelude}${spec.argName}=$1
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
    # $home/current names the installed binary for configs that must survive an upgrade: the link
    # is created beside $home and renamed over any old one, so a reader sees one path or the
    # other, never half of either. A machine without ln skips it — the resolved path is written
    # instead, as before this link existed.
    if command -v ln >/dev/null 2>&1; then
      ln -s "${spec.version}/${spec.binary}" "$home/.current.$$" && mv -f "$home/.current.$$" "$home/current"
    fi
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

${sourceStep(spec)}

# the step's exec runs on the caller's arguments, so they ride in as its own positionals
build_from_source "$@"
if [ -n "$src_nogo" ] && [ -z "$hook" ] && [ -z "$detached" ]; then
  echo "${spec.name}: no huddle release is published; install Go to build it from the marketplace checkout" >&2
fi

${dispatch}
[ -n "$hook" ] && exit 0
[ -n "$detached" ] && exit 0
echo "${spec.missing}" >&2
exit ${spec.failStatus}
`;
}

// Run directly, scripts/launcher.mjs renders the six launchers from the committed manifest: the huddle
// plugin's plugin/dist/run and one dist/run per provider plugin. `node scripts/launcher.mjs` writes them
// (mode 755), `--check` renders in memory and byte-compares, so a launcher stale against the manifest
// fails the check instead of shipping.
const invoked = process.argv[1] ? import.meta.url === pathToFileURL(resolve(process.argv[1])).href : false;
if (invoked) {
  const root = join(import.meta.dirname, "..");
  const release = releaseInfo(join(root, "release"));
  const scripts = [
    ["plugin/dist/run", launcherScript(huddleSpec(release))],
    ...PROVIDERS.map((provider) => [
      `plugins/${provider}-plugin-cc/dist/run`,
      launcherScript(providerSpec(release, provider)),
    ]),
  ];
  if (process.argv.includes("--check")) {
    const stale = [];
    for (const [relative, script] of scripts) {
      let committed = null;
      try {
        committed = readFileSync(join(root, relative), "utf8");
      } catch (error) {
        if (error?.code !== "ENOENT") throw error;
      }
      if (committed !== script) stale.push(relative);
    }
    if (stale.length > 0) {
      for (const relative of stale) {
        console.error(`${relative} is stale against release/v<VERSION>.json`);
      }
      console.error("run: npm run build:launcher");
      process.exit(1);
    }
  } else {
    for (const [relative, script] of scripts) {
      mkdirSync(join(root, relative, ".."), { recursive: true });
      writeFileSync(join(root, relative), script, { mode: 0o755 });
      console.error(`wrote ${relative}`);
    }
  }
}
