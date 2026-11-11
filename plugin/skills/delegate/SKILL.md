---
name: delegate
description: What to do after a huddle delegated job comes back. Use when a /huddle:run or the delegate agent returned a job report, when deciding to accept or return a delegated job, or when the user mentions /huddle:result, a job id, or a job awaiting review.
---

A delegated job runs on the harness you named, in the folder you are in or the one you pass with `--dir <path>`. Huddle makes no worktree: for isolation, create a git worktree yourself and pass its path. The harness edits that folder in place and nothing is committed until you accept. A folder is not a sandbox: the harness can still reach the rest of the machine as this user. Claude reads the diff and the report, then accepts or returns it.

The commands (run these yourself with Bash; the slash commands are for the user to type):

```bash
sh "${CLAUDE_PLUGIN_ROOT}/dist/run" status [<id>]
sh "${CLAUDE_PLUGIN_ROOT}/dist/run" result <id> --diff
sh "${CLAUDE_PLUGIN_ROOT}/dist/run" result <id> --accept
sh "${CLAUDE_PLUGIN_ROOT}/dist/run" result <id> --accept --no-commit
sh "${CLAUDE_PLUGIN_ROOT}/dist/run" result <id> --return '<feedback>'
sh "${CLAUDE_PLUGIN_ROOT}/dist/run" cancel <id>
```

Rules:

1. Read the report and `--diff` before you decide. Say what the job changed and what you decided.
2. Accept only work the diff proves. Return it with specific feedback when the change misses the task; the harness resumes in the same folder.
3. Never turn a failed or incomplete job into your own implementation: report it, then return it or cancel it.
4. If a harness is missing or not logged in, send the user to `/huddle:setup <harness>`.
5. A job in `failed (worker died)` lost its driver. Say so plainly; return it to run again or cancel it.
6. When the change must keep tests green, run the job with --gate, and --setup when the folder needs installing first. Use --scope and --forbid when the task has limits.
7. A verdict is what huddle measured, not what the harness claims. `gate_fail` and `scope_violation` do not stop an accept: read the output, then return the job with it as feedback, or accept it and say why.
8. Accept re-runs the job's setup and gates in the folder. If they fail, nothing was committed: return the job or cancel it. Accept commits on the branch checked out in the folder and refuses when it is no longer the branch the job started on.
9. A job that touches a folder with uncommitted changes says so in its report: its diff includes them.
10. A readonly job that changes any file fails; the folder is left as it is.
