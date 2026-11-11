<p align="center">
  <img src="assets/banner.svg" alt="huddle — one local router and delegation for Claude Code" width="100%"/>
</p>

<p align="center">
  <a href="https://www.gnu.org/licenses/gpl-3.0"><img src="https://img.shields.io/badge/License-GPL--3.0--or--later-blue.svg" alt="License: GPL-3.0-or-later"></a>
  <a href="https://www.rust-lang.org/"><img src="https://img.shields.io/badge/Rust-1.99.0-B7410E?logo=rust" alt="Rust Version"></a>
  <a href="https://code.claude.com/docs/en/plugins"><img src="https://img.shields.io/badge/Claude_Code-plugin-cba6f7" alt="Claude Code plugin"></a>
  <a href="https://github.com/MuhmdRaouf/huddle/actions/workflows/ci.yml"><img src="https://github.com/MuhmdRaouf/huddle/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a>
</p>

<p align="center">Built with ❤️ for the Claude Code community.</p>

<p align="center">
  <a href="https://github.com/sponsors/MuhmdRaouf"><img src="https://img.shields.io/badge/Sponsor_on_GitHub-EA4AAA?style=for-the-badge&logo=githubsponsors&logoColor=white" height="36" alt="Sponsor on GitHub"></a>&nbsp;&nbsp;<a href="https://buymeacoffee.com/muhmdraouf"><img src="https://img.shields.io/badge/Buy_me_a_coffee-FFDD00?style=for-the-badge&logo=buymeacoffee&logoColor=black" height="36" alt="Buy me a coffee"></a>
</p>

Claude Code sends every model request to Anthropic. A model from Z.ai, Moonshot,
DeepSeek, MiniMax or Alibaba needs its own endpoint and its own key. A task for
another coding CLI needs its own terminal, and its result has to come back by hand.

huddle is a Claude Code plugin and one Rust binary with two jobs. A local router
sends each model request to the provider the model belongs to, and every
`claude-*` request to Anthropic untouched. Delegation hands a task to another
coding CLI in its own git worktree, and Claude reads the diff and the report,
then accepts or returns it.

## What it does

The `muhmdraouf` marketplace (`.claude-plugin/marketplace.json`) ships six
plugins from this repository. The five provider plugins depend on `huddle`.

| Plugin | What it is | Models |
|---|---|---|
| [`huddle`](plugin/README.md) | The router, the session hooks and the delegation commands (`/huddle:setup`, `/huddle:run`, `/huddle:status`, `/huddle:result`, `/huddle:cancel`) | every `claude-*` model, to Anthropic |
| [`zai-plugin-cc`](plugins/zai-plugin-cc/README.md) | Z.ai GLM lane, key and `/model` entries | `glm-5.3`, `glm-5.3-flash` |
| [`kimi-plugin-cc`](plugins/kimi-plugin-cc/README.md) | Moonshot Kimi lane, key and `/model` entries | `kimi-k3`, `kimi-k2.6` |
| [`deepseek-plugin-cc`](plugins/deepseek-plugin-cc/README.md) | DeepSeek lane, key and `/model` entries | `deepseek-v4-pro`, `deepseek-flash` |
| [`minimax-plugin-cc`](plugins/minimax-plugin-cc/README.md) | MiniMax lane, key and `/model` entries | `MiniMax-M3`, `MiniMax-M2.7-highspeed` |
| [`qwen-plugin-cc`](plugins/qwen-plugin-cc/README.md) | Alibaba Qwen lane, key and `/model` entries | `qwen3.8-max`, `qwen3.8-flash` |

Each provider plugin has the same five commands: `/<p>:setup`, `/<p>:usage`,
`/<p>:board`, `/<p>:review` and `/<p>:remove`, where `<p>` is `zai`, `kimi`,
`deepseek`, `minimax` or `qwen`. Each also ships one agent per model, so Claude
can hand work to that model by name.

Every word the binary answers, every file it writes and every environment
variable it reads is in [`docs/USAGE.md`](docs/USAGE.md).

## Architecture

One binary, three layers. Dependencies point down only:

```text
crates/huddle          the one binary: one file per word (setup, run, status,
     │                 result, cancel, hook, uninstall, router/*, provider/*).
     │                 Builds Env and Paths once in main and passes them down.
     ▼
huddle-daemon          the router's run loop: one process per user, handover.
huddle-router          the proxy: lanes, the key swap, the Anthropic default.
huddle-setup           the install, the settings patch and its undo.
huddle-jobs            the job model and its git worktrees.
huddle-harness         the harness table and its runners.
     │
     ▼
huddle-catalog         provider data and routing.
huddle-pki             the local CA and the leaf certificates.
huddle-keystore        keys in the OS store.
huddle-procs           processes.
huddle-config          state paths.
```

Two crates sit beside the layers and never ship in the binary:
`huddle-testkit` (temp homes, ports, a fake upstream, a fake harness) and
`huddle-release` (the release notes, a changelog section and the size check).

Each crate has one job and a concrete error enum. Expected failures are values:
no `unwrap`, `expect` or `panic!` outside tests. The release profile aborts on
panic, so the router's request paths never panic.

## How it works

`/huddle:setup` installs the binary and points Claude Code at the router. From
then on, a model picked in `/model` decides where each request goes.

### Routing

1. The router listens on `127.0.0.1:4835` (`HUDDLE_ROUTER_PORT` moves it).
   Its health check is `/huddle-router/health`.
2. Claude Code reaches it through `HTTPS_PROXY`, and trusts it through the local
   CA in `NODE_EXTRA_CA_CERTS`. A proxy or CA of your own is never touched.
3. Each enabled provider has one lane. A request picks its lane by catalog id,
   then by the longest model prefix.
4. A `claude-*` model, or a model no lane claims, reaches Anthropic unchanged.
5. A provider request is forwarded unchanged except the destination, the key
   swap and the hop-by-hop headers. Qwen's `context_management` field is the one
   body edit.

The daemon re-reads `~/.agents/huddle-router/providers.json` every 2 s, so
adding or removing a provider needs no restart. A binary update, or a resident
size past 1 GiB, hands the socket to a new process while the requests in flight
drain.

### Dashboard

Open `http://127.0.0.1:4835/` while the router runs. The page updates live.

- **Health**: the router's version and uptime, whether Claude Code points at it
  (`HTTPS_PROXY` and the local CA in `settings.json`), requests per second and
  recent errors.
- **Lanes**: Anthropic and each provider, with requests, errors, time to first
  byte, tokens, and whether a key is stored (yes or no, never the key).
- **Requests**: the 200 latest model requests and the ones in flight, with
  model, lane, status, time and tokens.
- **Jobs**: delegated jobs that are queued, running, waiting for review, or
  dead (their driver is gone).

The page keeps everything in the router's memory. A restart clears it. It
shows no prompts and no keys. A different `HUDDLE_ROUTER_PORT` moves the page
with the router.

### Providers

- `/<p>:setup` checks the key with the provider, enables the lane, starts the
  router and adds the models to `/model`.
- The key comes from `<P>_API_KEY`, `~/.config/<p>-plugin-cc/env` or
  `huddle provider <p> key`, which reads hidden stdin. It is stored in the OS
  keystore: the macOS Keychain, or the Secret Service on Linux. It never travels
  in a chat, an argument or an output line.
- `/<p>:usage` shows requests, tokens and estimated cost per model, for today
  and the last 7 and 30 days.
- `/<p>:remove` takes the lane, the `/model` entries and the key back out. The
  other providers keep serving.

### Delegation

Hand a task to another coding CLI: `claude`, `codex`, `opencode`, `omp`, `pi`,
`zcode` and the rest of the harness table. `/huddle:setup` with no argument
lists every harness and whether it is ready.

| Command | What it does |
|---|---|
| `/huddle:setup [harness]` | Set huddle up, or show whether one harness is ready |
| `/huddle:run <harness> <task>` | Run a task on a harness, in its own worktree; `--background` returns at once |
| `/huddle:status [job-id]` | Show every job, or one job in full |
| `/huddle:result <job-id>` | Read a job's report and diff, then `--accept` it or `--return` it with feedback |
| `/huddle:cancel <job-id>` | Stop a job and remove its worktree and branch |

Every job runs in its own git worktree under `~/.agents/huddle/jobs/`. Your
checkout does not change until you accept. A worktree is not a sandbox: the
harness can still reach the rest of the machine as this user.

### Setup and recovery

1. `/huddle:setup` runs `plugin/dist/run setup`, a POSIX sh script. It downloads
   the release for this OS and CPU and checks it against `SHA256SUMS`, or builds
   the checkout with `cargo` while no release is published.
2. The binary lands at `~/.agents/huddle/bin/huddle-<version>`, with a `huddle`
   link to it.
3. `huddle setup --install` creates the local CA, starts the router, patches
   `settings.json` and records every change in a ledger. Nothing changes before
   it runs.
4. The SessionStart hook brings a stopped router back after a crash or a
   reboot. The UserPromptSubmit probe restarts a dead router.
   A hook exits 0 on any failure and never blocks a session.
5. `huddle uninstall` undoes every patch and restores `settings.json` byte for
   byte.

## Quick start

1. Add the marketplace and install the plugin in Claude Code:

   ```text
   /plugin marketplace add MuhmdRaouf/huddle
   /plugin install huddle@muhmdraouf
   ```

2. Run `/huddle:setup`.
3. Restart Claude Code once, so it reads the proxy settings.
4. Add one provider, for example Z.ai GLM:

   ```text
   /plugin install zai-plugin-cc@muhmdraouf
   /zai:setup
   ```

5. Pick a GLM model in `/model`, or delegate a task:

   ```text
   /huddle:run opencode "add a test for the parser"
   ```

huddle runs on macOS and Linux, on amd64 and arm64. There is no native Windows
build; on Windows, run Claude Code and huddle inside WSL.

To remove it: `/<p>:remove` for each provider, then `huddle uninstall`, then
`/plugin uninstall huddle@muhmdraouf`.

## Layout

```text
huddle/
├── crates/
│   ├── huddle/                 # the binary
│   │   ├── src/words/          #   one file per word
│   │   └── tests/              #   one e2e file per area
│   ├── huddle-router/          # the proxy and its lanes
│   ├── huddle-daemon/          # the run loop, singleton and handover
│   ├── huddle-setup/           # the install, the settings patch and its undo
│   ├── huddle-jobs/            # the job model and worktrees
│   ├── huddle-harness/         # the harness table and runners
│   ├── huddle-catalog/         # provider data and routing
│   ├── huddle-pki/             # the local CA
│   ├── huddle-keystore/        # keys in the OS store
│   ├── huddle-procs/           # processes
│   ├── huddle-config/          # state paths
│   ├── huddle-release/         # release notes, changelog section, size check
│   └── huddle-testkit/         # temp homes, ports, fakes (dev only)
├── plugin/                     # the huddle plugin: commands, agents, skills,
│                               #   hooks/hooks.json and dist/run
├── plugins/                    # the five provider plugins, <p>-plugin-cc/
├── .claude-plugin/             # marketplace.json: the six plugins
├── docs/                       # USAGE, TESTPLAN, INCIDENTS, DECISIONS, adr/
├── release/size-limit          # the release binary limit, 10 MB
├── assets/                     # banner, logo, mark, app icon, favicons
├── .github/workflows/          # ci.yml and release.yml
├── Dockerfile                  # the toolchain image
├── docker-compose.yml          # mounts the checkout into the image
├── mise.toml                   # pinned tools and the tasks
├── VERSION                     # the release version
└── CHANGELOG.md                # one section per version
```

## Automation

### `ci.yml`

Runs on every push to `main`, every pull request and a manual
`workflow_dispatch`. A plan job skips the parts a change does not touch; a
manual run checks everything.

1. Repository rules: no secrets in the history (gitleaks), no AI attribution in
   the history, every pre-commit hook, and a valid marketplace and plugins.
2. Tests: `cargo fmt` and `cargo clippy` on Linux, the whole suite on Linux and
   macOS, and the router overhead gate alone on its own runner.
3. Release builds: the four binaries, each on a runner of its own CPU, with
   static Linux binaries, and the 10 MB limit on every one.
4. `All checks passed`: the one check branch protection requires.

Every job is capped at 10 minutes. A new push cancels the run in flight on the
same branch.

### `release.yml`

Runs when a tag `v*` is pushed. No workflow creates tags.

1. Checks that the tag is `v` plus the committed `VERSION`.
2. Builds `huddle-linux-amd64`, `huddle-linux-arm64`, `huddle-darwin-amd64` and
   `huddle-darwin-arm64`.
3. Writes `SHA256SUMS` and a build-provenance attestation.
4. Publishes the GitHub release `huddle <version>`, with the version's
   `CHANGELOG.md` section, the checksums and the commit as its notes.

## Development

`mise.toml` pins the tools (Rust 1.99.0, Python, pre-commit, gitleaks) and holds
the tasks:

```sh
mise run setup          # the pinned tools and the pre-commit hook, once per clone
mise run lint           # every pre-commit hook on every file
mise run check          # every gate CI runs: lint, fmt, clippy and the tests
mise run rust:check     # the Rust gates alone
mise run check:history  # secrets and AI attribution in every commit, as CI scans
```

A machine without the mise tools runs the same gates in the toolchain image.
`docker-compose.yml` mounts the checkout, so the image never holds a copy of it:

```sh
mise run docker:check   # the rust:check gates in the image
mise run docker:build   # both static Linux binaries, size-checked
mise run docker:lint    # every pre-commit hook in the image
mise run docker:shell   # a shell in the image, checkout mounted
```

To try the plugin from the working tree, run `claude --plugin-dir ./plugin`.

Tests are hermetic. `HOME`, `CLAUDE_CONFIG_DIR` and the `XDG_*` directories
point at temp directories from `huddle-testkit`. No test touches the real
`~/.claude`, `~/.agents` or `~/.config`, binds port 4835 or reaches a real
provider: upstreams are in-process fakes.

## Versioning

The root `VERSION` file holds the release version. One string names every
part of a release:

| `VERSION` | Git tag | GitHub release |
|---|---|---|
| `0.0.2` | `v0.0.2` | `huddle 0.0.2` |

- `release.yml` refuses a tag that does not match `VERSION`.
- `plugin/dist/run` carries the version it downloads, and it equals `VERSION`.
- `CHANGELOG.md` has one `## <version>` section per release.
  `huddle-release section <version> CHANGELOG.md` prints it for the tag
  message, and `huddle-release notes` builds the release notes from it.

## Support

<p align="center">
  Built with ❤️ for the Claude Code community.<br>
  If huddle makes your day a little easier, you can say thanks here:
</p>

<p align="center">
  <a href="https://github.com/sponsors/MuhmdRaouf"><img src="https://img.shields.io/badge/Sponsor_on_GitHub-EA4AAA?style=for-the-badge&logo=githubsponsors&logoColor=white" height="36" alt="Sponsor on GitHub"></a>&nbsp;&nbsp;<a href="https://buymeacoffee.com/muhmdraouf"><img src="https://img.shields.io/badge/Buy_me_a_coffee-FFDD00?style=for-the-badge&logo=buymeacoffee&logoColor=black" height="36" alt="Buy me a coffee"></a>
</p>

## License

[GPL-3.0-or-later](LICENSE).

Copyright © 2026 [MuhmdRaouf](mailto:mohammed@raouf.me).
