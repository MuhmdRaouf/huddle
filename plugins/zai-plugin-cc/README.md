# zai

Routes Z.ai GLM models through huddle-router inside Claude Code. Install the `huddle` plugin first, then run `/huddle:setup` and restart Claude Code once.

- `/zai:setup` checks the key, adds the provider to huddle-router and the GLM models to `/model`. The key comes from `ZAI_API_KEY`, `~/.config/zai-plugin-cc/env` or `huddle provider zai key`.
- `/zai:usage` shows tokens and estimated cost per day and window.
- `/zai:board` and `/zai:review` show delegated jobs and take decisions.
- `/zai:remove` undoes the setup. Run it before `/plugin uninstall zai-plugin-cc@muhmdraouf`.
