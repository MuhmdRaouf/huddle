---
description: Run a task on another CLI harness in this folder (or a folder you name) and return its report for you to accept or return
argument-hint: '<harness> [--background] [--dir <path>] [--readonly|--exec|--write] [--effort <e>] <task>'
allowed-tools: Bash(sh:*)
---

Run a delegated job. The raw arguments are:

$ARGUMENTS

Run one Bash command, with the harness first and the task as the last argument, quoted:

```bash
sh "${CLAUDE_PLUGIN_ROOT}/dist/run" run <harness> [flags] '<task>'
```

Rules:

- Do not edit the task. Quote it for the shell (single quotes, a literal single quote written as '\'').
- The harness works in the current folder. For isolation, create a git worktree yourself and pass its path with `--dir <path>`; huddle makes no worktree of its own. A second write job in the same folder is refused while one runs.
- Flags before the task, each repeatable: `--setup '<cmd>'` prepares the folder before the harness starts, `--gate '<cmd>'` must exit 0, `--scope '<glob>'` lists what the job may change, `--forbid '<glob>'` what it must not. Pass the ones the user asked for.
- Do not read files first or do the work yourself: the harness works in the folder.
- Without --background the command waits until the job lands. Use a Bash timeout of 7500000 ms (just over the job's 2 hour limit).
- With --background it returns at once; say the job id and that `/huddle:status` shows it.
- If the harness is not ready the command says so and exits 6; relay that and stop.

After the command returns, follow the `delegate` skill.
