---
description: Set huddle up, or show whether one coding CLI is ready
argument-hint: "[harness]"
allowed-tools: Bash(sh:*)
---

!`sh "${CLAUDE_PLUGIN_ROOT}/dist/run" setup --install 2>&1`
!`sh "${CLAUDE_PLUGIN_ROOT}/dist/run" setup $ARGUMENTS 2>&1`

Show the output above to the user as it is. If it says to restart Claude Code, tell the user to restart it
once. Do nothing else.
