---
description: Name the hub for a Caddy reverse proxy and print the site block; off goes back to localhost only
argument-hint: "[<name> | off | --check]"
allowed-tools: Bash
---

!`sh "${CLAUDE_PLUGIN_ROOT}/dist/run" domain $ARGUMENTS 2>&1`

Show the output above to the user as it is, with no preamble or recap: the Caddy site block stays a code block
they can paste. Do nothing else; never edit a Caddyfile yourself.
