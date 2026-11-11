---
description: Stop a delegated job, mark it cancelled, leave its folder as it is
argument-hint: '<job-id>'
disable-model-invocation: true
allowed-tools: Bash(sh:*)
---

!`sh "${CLAUDE_PLUGIN_ROOT}/dist/run" cancel $ARGUMENTS 2>&1`

Show the output above to the user as it is. Add nothing.
