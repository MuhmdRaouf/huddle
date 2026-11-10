---
description: Show a delegated job's report and change; accept it or return it with feedback
argument-hint: '<job-id> [--diff] [--accept [--no-commit]] [--return "<feedback>"]'
disable-model-invocation: true
allowed-tools: Bash(sh:*)
---

!`sh "${CLAUDE_PLUGIN_ROOT}/dist/run" result $ARGUMENTS 2>&1`

Present the full output above to the user. Do not summarize or condense it. Keep the job id, the report, the changed files and the `Next` line. Add nothing.
