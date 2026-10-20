---
description: Stop the hub; radar and the boards are served by it
allowed-tools: Bash
---

!`sh "${CLAUDE_PLUGIN_ROOT}/dist/run" down 2>&1`
!`sh "${CLAUDE_PLUGIN_ROOT}/dist/run" radar stop 2>&1`
!`sh "${CLAUDE_PLUGIN_ROOT}/dist/run" kanban stop $ARGUMENTS 2>&1`

Show the output above to the user as it is, with no preamble or recap. Do nothing else.
