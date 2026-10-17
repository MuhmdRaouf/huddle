---
description: One table: hub, radar, board server, gateway; running or not, address
allowed-tools: Bash
---

!`sh "${CLAUDE_PLUGIN_ROOT}/dist/run" status 2>&1`
!`sh "${CLAUDE_PLUGIN_ROOT}/dist/run" radar status 2>&1`
!`sh "${CLAUDE_PLUGIN_ROOT}/dist/run" kanban status 2>&1`
!`sh "${CLAUDE_PLUGIN_ROOT}/dist/run" gateway status 2>&1`

Show the output above to the user as it is, with no preamble or recap, as one table with a row per part — hub,
radar, board server, gateway — saying whether it runs and at which address. Leave out a row this project has
none of. Do nothing else.
