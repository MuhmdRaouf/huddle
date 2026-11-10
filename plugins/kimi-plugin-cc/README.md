# kimi

Routes Moonshot Kimi models through huddle-router inside Claude Code. Install the `huddle` plugin first, then run `/huddle:setup` and restart Claude Code once.

- `/kimi:setup` checks the key, adds the provider to huddle-router and the Kimi models to `/model`. The key comes from `KIMI_API_KEY`, `~/.config/kimi-plugin-cc/env` or `huddle provider kimi key`.
- `/kimi:usage` shows tokens and estimated cost per day and window.
- `/kimi:board` and `/kimi:review` show delegated jobs and take decisions.
- `/kimi:remove` undoes the setup. Run it before `/plugin uninstall kimi-plugin-cc@muhmdraouf`.
