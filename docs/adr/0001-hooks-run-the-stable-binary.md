# ADR 1: Hooks run the stable binary

Status: accepted.

## Context

- Claude Code fixes `CLAUDE_PLUGIN_ROOT` for the life of a session.
- The plugins carry no version (D13), so every marketplace commit installs into a new plugin cache directory, and
  an update removes the old one.
- A hook line that runs `${CLAUDE_PLUGIN_ROOT}/dist/run` then fails with exit 127 for the rest of the session.
  Claude Code shows a non-blocking hook error at most, so the session keeps working while the registry hears
  nothing from it: the session reads idle, then stuck.
- The launcher already keeps a path that no update deletes: every build or verified download repoints
  `${AGENTS_HUDDLE_HOME:-$HOME/.agents/huddle-bin}/current` at the binary it installed.

## Decision

Every `plugin/hooks/hooks.json` line runs `current` when it is there, and the launcher only until the first build
exists:

```sh
h="${AGENTS_HUDDLE_HOME:-$HOME/.agents/huddle-bin}/current"; [ -x "$h" ] && exec "$h" hook <event>; exec sh "${CLAUDE_PLUGIN_ROOT}/dist/run" hook <event>
```

- The line names the binary home the way the launcher does, so `AGENTS_HUDDLE_HOME` moves both.
- It is POSIX sh with builtins only (`[`, `exec`): the warm path forks nothing before the binary.
- `[ -x ]` follows the link, so a `current` whose build is gone falls back to the launcher.
- The first hook on a new machine still reaches `dist/run`, which starts the detached build and exits 0.

`TestAHookSurvivesAPluginUpdate` (`internal/plugins/hook_survives_update_e2e_test.go`) proves it: the first hook
builds through `dist/run`, and a hook still reports to the hub after its plugin directory is deleted.

## Consequences

- A session's hooks outlive a plugin update.
- The hook words and their stdin are a contract between the binary and every `hooks.json` still loaded in an open
  session. A hook word the binary drops must stay accepted, as a silent exit 0, for as long as an old session may
  run.
- Once `current` exists a hook skips the launcher, so a hook never starts the build of a new plugin commit. That build comes from the next launcher run that is not a hook: the MCP server `.mcp.json` starts at
  each session start, or a command. Until it lands, hooks run the previous build.
- The MCP shim (`plugin/bin/huddle-mcp`) is the rebuild path: `.mcp.json` runs it, and through it `dist/run`, at each
  session start while the plugin directory exists, and runs `current mcp` directly once an update has deleted it
  (`TestTheMCPShimSurvivesAPluginUpdate`).
