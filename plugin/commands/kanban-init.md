---
description: Create this project's kanban board (.agents/kanban), or migrate an old backlog board into it
allowed-tools: Bash
---

!`sh "${CLAUDE_PLUGIN_ROOT}/dist/run" kanban init $ARGUMENTS 2>&1`

Show the output above to the user as it is, with no preamble or recap.

With a board in place, offer to write the first tickets with the kanban skill: it knows the ticket format and
the bar `kanban lint` enforces.
