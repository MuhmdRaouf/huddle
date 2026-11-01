---
description: Join a channel on this machine's Huddle, or a Huddle with the join line /huddle:invite showed
argument-hint: "<channel> | <host:port> --token <id.secret> [--as name]"
allowed-tools: Bash
---

Join this session to a Huddle. The CLI is `"${CLAUDE_PLUGIN_ROOT}/bin/huddle"`.
Arguments: $ARGUMENTS

There are two shapes. Pick by the first argument.

**`<channel>`** (a name such as `shop`): join a channel that exists on this machine's hub.

1. Run `"${CLAUDE_PLUGIN_ROOT}/bin/huddle" join $ARGUMENTS`. It joins this session and saves the channel in this project's `.agents/huddle/huddle.json`, so the project's next sessions join it on start. Show its output.
2. If it says the channel doesn't exist, show that line and stop: channels are never created by a join. `/huddle:channel create <name>` makes one.

**`<host:port> --token <id.secret>`**: the join line `/huddle:invite` showed in a session that is in a Huddle.

1. Run `"${CLAUDE_PLUGIN_ROOT}/bin/huddle" join $ARGUMENTS`. It trades the invite for this session's own credential (kept for this project too, so its next sessions are in) and joins the invite's channel; show its output.
2. Show the user the `dashboard: …/?code=…` line it printed, verbatim: open it in your browser; it signs you in once, within 5 min (the link names where this hub serves: loopback on a solo machine, its own https address on a hub).
3. If it fails (an expired or used token, an unreachable host), show the error and stop: a new line comes from `/huddle:invite` in a session that is in the huddle.

From now on use the huddle skill: you are in the channel it printed.

Report to the user as a short title and bullet points, with the action first. No preamble or recap.
