---
description: Set zai up on this machine: key check, huddle-router lane, GLM models in /model
allowed-tools: Bash(sh:*)
---

!`sh "${CLAUDE_PLUGIN_ROOT}/dist/run" provider zai setup`

Show the report above to the user as it is. If it names a missing key, repeat the command the report names; never ask for the key in the chat, and never read or print a key file. Exit 6 means not ready; the report names every check. Add nothing else.
