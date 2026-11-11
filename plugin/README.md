# huddle plugin

Routes Claude Code's requests to Z.ai, Kimi, DeepSeek, MiniMax and Qwen through a local router, and delegates tasks to other coding CLIs.

## Commands

| Command | What it does |
|---|---|
| `/huddle:setup [harness]` | Set huddle up, or show whether one harness is ready |
| `/huddle:run <harness> <task>` | Run a task on a harness, in its own worktree |
| `/huddle:status [job-id]` | Show every job, or one job in full |
| `/huddle:result <job-id>` | Read a job's report and diff, then accept it or return it with feedback |
| `/huddle:cancel <job-id>` | Stop a job and remove its worktree |
| `/huddle:update` | Install the binary of the version this plugin carries and hand the router over |
| `/huddle:add-dashboard-host <host>` | Let a domain such as `huddle.example.com` open the dashboard |
| `/huddle:remove-dashboard-host <host>` | Stop that domain from opening the dashboard |

## The delegate agent

The `delegate` agent forwards one task to a harness and returns the harness's report unchanged; every job runs in its own git worktree.

## Hooks

`hooks/hooks.json` runs the SessionStart hook, which brings a stopped router back, and the UserPromptSubmit hook; a hook never blocks a session.

Run `/huddle:setup`, then restart Claude Code once.
