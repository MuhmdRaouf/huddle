---
description: Review a delegated job and decide it: accept it or return it with feedback
argument-hint: "[job id]"
allowed-tools: Bash(sh:*)
---

!`sh "${CLAUDE_PLUGIN_ROOT}/dist/run" result $ARGUMENTS`

Show the report above to the user as it is. Then ask with AskUserQuestion what to do with this job, options `Accept` and `Return`. For `Accept`, run `sh "${CLAUDE_PLUGIN_ROOT}/dist/run" result <id> --accept`. For `Return`, ask for one line of feedback and run `sh "${CLAUDE_PLUGIN_ROOT}/dist/run" result <id> --return '<feedback>'`. Never accept without asking.
