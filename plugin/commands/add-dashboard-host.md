---
description: Let a domain such as huddle.example.com open the huddle dashboard
argument-hint: '<host>'
disable-model-invocation: true
allowed-tools: Bash(sh:*)
---

!`sh "${CLAUDE_PLUGIN_ROOT}/dist/run" add-dashboard-host $ARGUMENTS 2>&1`

Show the output above to the user as it is. Do not summarize or condense it. Add nothing.
