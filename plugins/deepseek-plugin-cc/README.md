# deepseek

Routes DeepSeek models through huddle-router inside Claude Code. Install the `huddle` plugin first, then run `/huddle:setup` and restart Claude Code once.

- `/deepseek:setup` checks the key, adds the provider to huddle-router and the DeepSeek models to `/model`. The key comes from `DEEPSEEK_API_KEY`, `~/.config/deepseek-plugin-cc/env` or `huddle provider deepseek key`.
- `/deepseek:usage` shows tokens and estimated cost per day and window.
- `/deepseek:board` and `/deepseek:review` show delegated jobs and take decisions.
- `/deepseek:remove` undoes the setup. Run it before `/plugin uninstall deepseek-plugin-cc@muhmdraouf`.
