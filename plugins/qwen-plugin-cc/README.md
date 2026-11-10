# qwen

Routes Alibaba Qwen models through huddle-router inside Claude Code. Install the `huddle` plugin first, then run `/huddle:setup` and restart Claude Code once.

- `/qwen:setup` checks the key, adds the provider to huddle-router and the Qwen models to `/model`. The key comes from `QWEN_API_KEY` (or `DASHSCOPE_API_KEY`), `~/.config/qwen-plugin-cc/env` or `huddle provider qwen key`.
- `/qwen:usage` shows tokens and estimated cost per day and window.
- `/qwen:board` and `/qwen:review` show delegated jobs and take decisions.
- `/qwen:remove` undoes the setup. Run it before `/plugin uninstall qwen-plugin-cc@muhmdraouf`.
