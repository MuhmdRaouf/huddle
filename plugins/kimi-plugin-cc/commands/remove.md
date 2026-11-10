---
description: Turn kimi off: its models out of /model, huddle-router stops routing this provider, stored key removed. Run it before /plugin uninstall
allowed-tools: Bash(sh:*)
---

!`sh "${CLAUDE_PLUGIN_ROOT}/dist/run" provider kimi remove`

Show the report above to the user as it is. If it has a line that says what could not be done, say so in one line and that running `/kimi:remove` again retries it. Otherwise add one line: to uninstall the plugin as well, run `/plugin uninstall kimi-plugin-cc@muhmdraouf` now. Add nothing else.
