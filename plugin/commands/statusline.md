---
description: Show the huddle status bar line, or install it into (or remove it from) Claude Code's status line
argument-hint: "[--install] [--remove] [--color]"
allowed-tools: Bash
---

Work with the huddle status bar line. The CLI is `"${CLAUDE_PLUGIN_ROOT}/bin/huddle"` (call it `huddle` below).
Arguments, if any: $ARGUMENTS

1. With `--install`: run `huddle statusline --install` and report what it wrote — the settings file it
   named, and that `--remove` puts the previous statusline back.
2. With `--remove`: run `huddle statusline --remove` and report what came back.
3. With neither flag: run a sample render —

   ```sh
   echo '{"session_id":"demo","rate_limits":{"five_hour":{"used_percentage":42,"resets_at":0},"seven_day":{"used_percentage":18,"resets_at":0}}}' | huddle statusline
   ```

   — and show the line it printed. An empty line means this project is not in a huddle yet: say so and
   point at `/huddle:setup`.

No preamble or recap; report what the command said.
