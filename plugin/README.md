# huddle plugin

Routes Claude Code's requests to Z.ai, Kimi, DeepSeek, MiniMax and Qwen through a local router, and delegates tasks to other coding CLIs.

## Commands

| Command | What it does |
|---|---|
| `/huddle:setup [harness]` | Set huddle up, or show whether one harness is ready |
| `/huddle:run <harness> <task>` | Run a task on a harness, in the current folder or `--dir` |
| `/huddle:status [job-id]` | Show every job, or one job in full |
| `/huddle:result <job-id>` | Read a job's report and diff, then accept it or return it with feedback |
| `/huddle:cancel <job-id>` | Stop a job |
| `/huddle:update` | Install the binary of the version this plugin carries and hand the router over |
| `/huddle:<p>:setup` | Check a provider's key, add its lane, models and agents (`<p>` is `zai`, `kimi`, `deepseek`, `minimax` or `qwen`); a key page opens when no key is stored |
| `/huddle:<p>:usage` | That provider's requests, tokens and estimated cost |
| `/huddle:<p>:remove` | Take that provider's lane, models, agents and key back out |
| `/huddle:add-dashboard-host <host>` | Let a domain such as `huddle.example.com` open the dashboard |
| `/huddle:remove-dashboard-host <host>` | Stop that domain from opening the dashboard |

## The delegate agent

The `delegate` agent forwards one task to a harness and returns the harness's report unchanged; every job runs in the caller's folder or the one `--dir` names.

## Hooks

`hooks/hooks.json` runs the SessionStart hook, which brings a stopped router back, and the UserPromptSubmit hook; a hook never blocks a session.

Run `/huddle:setup`, then restart Claude Code once.
