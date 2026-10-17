---
description: Ensure the hub is running and print its dashboard URL (Hub, Radar and Kanban tabs)
allowed-tools: Bash
---

!`sh "${CLAUDE_PLUGIN_ROOT}/dist/run" up 2>&1`
!`sh "${CLAUDE_PLUGIN_ROOT}/dist/run" open --print 2>&1`

Show the output above to the user as it is, with no preamble or recap. The dashboard carries the Hub, Radar and
Kanban tabs; say so when the user asks what it shows. Do nothing else.
