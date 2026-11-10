# minimax

Routes MiniMax models through huddle-router inside Claude Code. Install the `huddle` plugin first, then run `/huddle:setup` and restart Claude Code once.

- `/minimax:setup` checks the key, adds the provider to huddle-router and the MiniMax models to `/model`. The key comes from `MINIMAX_API_KEY`, `~/.config/minimax-plugin-cc/env` or `huddle provider minimax key`.
- `/minimax:usage` shows tokens and estimated cost per day and window.
- `/minimax:board` and `/minimax:review` show delegated jobs and take decisions.
- `/minimax:remove` undoes the setup. Run it before `/plugin uninstall minimax-plugin-cc@muhmdraouf`.
