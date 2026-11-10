# AGENTS.md

Guidance for coding agents (Claude Code, Codex, Gemini CLI, opencode and others) working in this repository.
`CLAUDE.md` imports this file.

Huddle: a Claude Code plugin with two jobs, shipped as one Rust binary and one plugin:

- **huddle-router**: a local proxy that routes Claude Code's requests to the right provider (zai, kimi, deepseek, minimax, qwen) and sends everything else to Anthropic.
- **delegation**: Claude Code hands a task to another coding CLI (table-driven harnesses: codex, opencode, omp, pi, zcode and the rest), the task runs in its own git worktree, and Claude reads the diff and report and accepts or returns it.

Five thin provider plugins ship from this repository beside the `huddle` plugin.

Commands:

```sh
mise run setup                                  # pinned tools and the pre-commit hook, once per clone
mise run check                                  # every gate: lint, fmt, clippy, tests
mise run docker:check                           # every gate inside the docker toolchain image
mise run docker:build                           # both static linux binaries, size-checked, in docker
cargo test --workspace                          # the whole suite
cargo test -p huddle --test e2e_flow            # one e2e file
cargo fmt --all --check
cargo clippy --workspace --all-targets --locked -- -D warnings
cargo build --release -p huddle                 # the release binary (LTO, panic=abort, strip)
cargo run -q -p huddle-release -- size target/release/huddle  # the binary size limit from release/size-limit
claude --plugin-dir ./plugin                    # load the plugin from the working tree
cargo run -q -p huddle-testkit --bin huddle-check-attribution  # no AI attribution in the history
```

## Layout

- `crates/huddle`: the one binary. `src/words/` holds one file per word: `setup`, `run`, `status`, `result`, `cancel`, `hook`, `uninstall`, `router/*`, `provider/*`. `tests/` holds one e2e file per area.
- `crates/huddle-router`, `crates/huddle-daemon`: the router and its run loop, singleton and handover.
- `crates/huddle-config`, `crates/huddle-catalog`, `crates/huddle-pki`, `crates/huddle-keystore`: state paths, provider data and routing, local CA, keys in the OS store.
- `crates/huddle-harness`, `crates/huddle-jobs`, `crates/huddle-procs`: harness table and runners, job model and worktrees, processes.
- `crates/huddle-setup`: the install, the settings patch and its undo.
- `crates/huddle-testkit`: temp homes, ports, fake upstream, fake harness (dev only).
- `plugin/`: what Claude Code installs for huddle (`.claude-plugin/plugin.json`, `hooks/hooks.json`, `commands/`, `agents/delegate.md`, `skills/delegate/`, `dist/run`). `plugins/<p>-plugin-cc/`: the five provider plugins, each with its agents, commands and a `dist/run`.
- `.claude-plugin/marketplace.json`: the `muhmdraouf` marketplace with the six plugins.

## Architecture

- One router per user. `huddle router serve` listens on `127.0.0.1:4835` (`HUDDLE_ROUTER_PORT` moves it), health at `/huddle-router/health`, state under `~/.agents/huddle-router/` (`providers.json` lists the enabled providers, re-read every 2 s; one lane per provider, matched by catalog id, then the longest model prefix). `claude-*` goes to Anthropic untouched; a model no lane claims goes to Anthropic.
- One hook. `plugin/hooks/hooks.json` wires SessionStart and a UserPromptSubmit probe that restarts a dead router (`hook prompt-submit`). SessionStart: it runs `~/.agents/huddle/bin/huddle hook session-start`, which does nothing before `/huddle:setup` has run, and when the router is stopped starts it detached and returns. A hook never blocks a session: it exits 0 on any failure and stays silent when it has nothing to say.
- `/huddle:setup` runs `plugin/dist/run setup`, a POSIX sh script that downloads the release for this OS and CPU from GitHub Releases and checks it against `SHA256SUMS` (or runs a local `cargo build` while no release exists), then `huddle setup --install`, which starts the router, points Claude Code's `HTTPS_PROXY` at it with the local CA in `NODE_EXTRA_CA_CERTS`, and records what it changed in a ledger. The patch is idempotent and reversible: `huddle uninstall` restores `settings.json` byte for byte. Binaries live at `~/.agents/huddle/bin/huddle-<version>` with a `huddle` symlink beside them.
- `/<p>:setup` adds a provider's lane, key and `/model` entries; `/<p>:remove` undoes them. Keys live in the OS keystore, entered through the environment, `~/.config/<p>-plugin-cc/env` or `huddle provider <p> key` reading hidden stdin.
- Delegation runs a table-driven harness in a fresh git worktree (`~/.agents/huddle/jobs/<repo-slug>/`). `/huddle:run` waits by default, `--background` returns; `/huddle:status` finds dead drivers; `/huddle:result <id> [--diff] [--accept [--no-commit]] [--return "<feedback>"]` decides; `/huddle:cancel <id>` stops and cleans up.
- `panic=abort`, Rust stable, reproducible flags are not a goal; CI enforces a 10 MB binary limit (`release/size-limit`) that never rises just to pass.

## Code rules

- Rust, statically typed everywhere; a concrete error enum per crate. No `unwrap`, `expect` or `panic!` in non-test code, and none on the router's or daemon's request paths. Expected failures are values.
- No process-wide mutable state; `Env` and `Paths` are built once in `main` and passed down: nothing below `main` reads `std::env`.
- No blocking call on an async task.
- Functional core, imperative shell: decisions are unit-tested with fakes; adapters do only I/O and are tested against real files, processes and temp directories.
- A hook never blocks a session: it exits 0 on any failure and stays silent when it has nothing to say.
- Plug and play: never ask a user to export a variable, edit a config or run a setup step beyond `/huddle:setup`.
- Keys live in the OS keystore, never in chat or in files the tests do not own.
- Tests are hermetic: `HOME`, `CLAUDE_CONFIG_DIR` and the `XDG_*` directories point at temp dirs from `huddle-testkit`; tests never touch the real `~/.claude`, `~/.agents` or `~/.config`, never port 4835, and never a process they did not start. Real upstreams are in-process fakes.
- Every incident is a case in `docs/INCIDENTS.md`: cause, guard test, rule. A new incident adds its section and its guard in the same change.
- macOS and Linux only; Windows users run huddle inside WSL, with the Linux binary.

## Writing code

1. **Simple first.** Write the smallest design that solves today's problem. No abstraction, option or layer until a second real use needs it; three similar lines beat a premature helper.
2. **High cohesion, low coupling.** A crate does one job and owns its data. Crates talk through small, explicit interfaces, never through each other's internals or shared globals.
3. **Everything statically typed.** Structs for data, small traits declared by the consumer; JSON is decoded into types at the edge and checked there.
4. **Good design, kept honest.** Clear boundaries, dependencies that point inward, names that say what a thing is. A pattern earns its place by removing code or a class of bugs, not by being a pattern.

## Tests

1. **TDD.** Write the failing test first, watch it fail, make it pass, then refactor. A bug fix starts with the test that shows the bug.
2. **E2E for every user-facing path.** Unit tests prove the parts; an e2e in `crates/huddle/tests/` against the real binary proves the path a user takes works as a whole.
3. **CI runs every test.** A test that runs on one machine only does not count. `mise run check` runs the gates locally; `.github/workflows/ci.yml` runs them on every push.

## Agent files

Everything an agent writes stays under `.agents/`, in the gitignored folders below, and nowhere else in the repo,
so none of it can reach git:

| Folder | What goes there |
|---|---|
| `.agents/scratch/` | notes, plans, drafts, logs, build caches, downloads |
| `.agents/secrets/` | keys and tokens a test run needs (mode 0600); docs name them, never print them |
| `.agents/tickets/` | the board and the tickets agents work from |
| `.agents/worktrees/` | one git worktree per ticket |

Only `.agents/skills/` is committed. Never write working files into the source tree, `/tmp` or the home
directory, and never pass a secret on a command line.

## Comments and docs

- Present tense, describing the code as it is. No history, no "used to", no earlier designs.
- Comment only the non-obvious why (a constraint, a protocol rule, a security reason).
- README and the plugin's skill text change in the same commit as the behaviour.

## Writing

Applies to docs, command and skill text, CLI output, commit messages and replies.

- Lead with the action or the answer; number steps that happen in order.
- Short sentences, plain words, specific numbers.
- No AI jargon ("seamless", "robust", "leverage", "comprehensive", "powerful", "delve", "unlock", "empower",
  "elevate", "streamline", "cutting-edge", "game-changer"). No emojis. No preamble or sign-off.

## Commits and pull requests

**Hard rule: no AI attribution, ever.** No `Co-authored-by` trailer, no "Generated with" line, no session link, no AI
author or committer, in commits, pull requests, tags or release notes. `.claude/settings.json` turns Claude Code's
attribution off; `cargo run -q -p huddle-testkit --bin huddle-check-attribution` runs in CI on every push.

- One line, Conventional Commits: `<type>(<scope>): <message>`. No body, no co-author, no AI jargon, no emojis. Types:
  `init`, `feat`, `fix`, `perf`, `refactor`, `docs`, `test`, `build`, `ci`, `chore`. Scopes: `huddle`, `core`,
  `plugin`, `repo`. Imperative, lowercase start, no trailing period, no body.
- Signed (`git commit -S`), as Raouf. Stage files by name; never `git commit -a` or `git add .`.
- Never commit secrets: the gitleaks pre-commit hook guards each commit and CI scans the whole history.

## CI

`.github/workflows/ci.yml`: repository rules (gitleaks, attribution, actionlint, plugin validation), the Rust gates (fmt, clippy, tests on Linux and macOS, cross builds for four targets, the 10 MB size guard), and the gate `All checks passed`. Every job is capped at 10 minutes. Reproduce failures locally; re-run only the failed jobs.

## Releases

- No release and no tag without Raouf's yes. While none exists, `/huddle:setup` builds from the marketplace checkout with `cargo`.
- A pushed tag `v<VERSION>` (root `VERSION` file) runs `release.yml`: four static-binary assets `huddle-<os>-<arch>` and `SHA256SUMS`.
- `plugin/dist/run` carries the version it downloads; it equals `VERSION`.
