---
description: Create a channel on this machine's Huddle and join this session to it
argument-hint: "create <name>"
allowed-tools: Bash
---

Create a channel. The CLI is `"${CLAUDE_PLUGIN_ROOT}/bin/huddle"`.
Arguments: $ARGUMENTS

1. Run `"${CLAUDE_PLUGIN_ROOT}/bin/huddle" channel $ARGUMENTS` and show its output verbatim. `create <name>` makes the channel (a lowercase letter or digit first, then up to 39 letters, digits and dashes), joins this session to it and saves it in this project's `.agents/huddle/huddle.json`, so the project's next sessions join it on start.
2. If it says the channel exists, show that line and stop: `/huddle:join <name>` joins it.
3. If it says Huddle is unreachable, show the error and stop: `/huddle:open` starts the hub.
4. To bring another project in, tell the user to run `/huddle:join <name>` there.

Report to the user as a short title and bullet points, with the action first. No preamble or recap.
