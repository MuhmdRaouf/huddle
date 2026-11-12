---
description: Turn zai off: its models out of /model, huddle-router stops routing this provider, stored key removed
allowed-tools: Bash(sh:*)
---

!`sh "${CLAUDE_PLUGIN_ROOT}/dist/run" provider zai remove`

Show the report above to the user as it is. If it has a line that says what could not be done, say so in one line and that running `/huddle:zai:remove` again retries it. Add nothing else.
