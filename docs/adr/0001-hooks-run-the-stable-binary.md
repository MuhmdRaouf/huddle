# ADR 1: Hooks run the stable binary

Status: accepted. It sets D8 in [DECISIONS.md](../DECISIONS.md).

## Context

- Claude Code fixes `CLAUDE_PLUGIN_ROOT` for the life of a session.
- The plugins carry no version (D8), so every marketplace commit installs into a new plugin cache
  directory, and an update removes the old one.
- A hook line that runs `${CLAUDE_PLUGIN_ROOT}/dist/run` then fails with exit 127 for the rest of
  the session. Claude Code shows a non-blocking hook error at most, so the session keeps working
  while nothing of it reaches the router: no session start re-applies the wiring, and no prompt
  brings a stopped router back.
- The bootstrap keeps a path that no update deletes: every download or verified build repoints
  `${HUDDLE_HOME:-$HOME/.agents/huddle}/bin/huddle` at the binary it installed.

## Decision

The plugin wires one SessionStart event (matcher `startup|resume|clear|compact`) and one
UserPromptSubmit probe, and nothing else. Both lines run the `huddle` link when it is there, and the
bootstrap only until the first binary exists:

```sh
h="${HUDDLE_HOME:-$HOME/.agents/huddle}/bin/huddle"; [ -x "$h" ] && exec "$h" hook session-start; exec sh "${CLAUDE_PLUGIN_ROOT}/dist/run" hook session-start
```

- The line names the binary home the way the bootstrap does, so `HUDDLE_HOME` moves both.
- It is POSIX sh with builtins only (`[`, `exec`): the warm path forks nothing before the binary.
- `[ -x ]` follows the link, so a link whose build is gone falls back to the bootstrap.
- The first hook on a new machine still reaches `dist/run`, which exits 0 in silence until
  `/huddle:setup` has installed a binary.
- The probe carries a one-second timeout at the harness and a 50 ms budget inside the binary, so a
  prompt never waits on a router.

`crates/huddle/tests/plugin_hooks.rs:the_hook_line_runs_the_installed_binary_even_after_its_plugin_directory_is_gone`
proves the contract: a hook still runs the `huddle` link after its plugin directory is deleted, and
`crates/huddle/tests/plugin_hooks.rs:the_hook_line_with_no_binary_exits_zero_silently_and_starts_nothing`
pins the silent pre-setup half.

## Consequences

- A session's hooks outlive a plugin update.
- The hook words and their stdin are a contract between the binary and every `hooks.json` still
  loaded in an open session. A hook word the binary drops stays accepted, as a silent exit 0, for
  as long as an old session may run.
- Once the `huddle` link exists a hook never starts the bootstrap's download or build; that comes from the
  next command that runs the plugin. Until it lands, hooks run the previous build.
