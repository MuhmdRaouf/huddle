---
description: Check every ticket against the bar; exit 1 when any fails
allowed-tools: Bash
---

!`sh "${CLAUDE_PLUGIN_ROOT}/dist/run" kanban lint $ARGUMENTS 2>&1`

Show the output above to the user as it is, with no preamble or recap.
