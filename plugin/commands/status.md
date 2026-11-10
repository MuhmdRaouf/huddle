---
description: Show huddle's delegated jobs, or one job in full
argument-hint: '[job-id]'
disable-model-invocation: true
allowed-tools: Bash(sh:*)
---

!`sh "${CLAUDE_PLUGIN_ROOT}/dist/run" status $ARGUMENTS 2>&1`

Show the output above to the user as it is. Do not summarize or condense it. If it is a table, keep it as a table. Add nothing.
