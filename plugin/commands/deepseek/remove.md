---
description: Turn deepseek off: its models out of /model, huddle-router stops routing this provider, stored key removed
allowed-tools: Bash(sh:*)
---

!`sh "${CLAUDE_PLUGIN_ROOT}/dist/run" provider deepseek remove`

Show the report above to the user as it is. If it has a line that says what could not be done, say so in one line and that running `/huddle:deepseek:remove` again retries it. Add nothing else.
