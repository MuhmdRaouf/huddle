<p align="center">
  <img src="../../assets/banners/qwen-plugin-cc.svg" alt="qwen-plugin-cc — Qwen models in Claude Code, through huddle" width="100%"/>
</p>

<p align="center">
  <a href="https://www.gnu.org/licenses/gpl-3.0"><img src="https://img.shields.io/badge/License-GPL--3.0--or--later-blue.svg" alt="License: GPL-3.0-or-later"></a>
  <a href="https://code.claude.com/docs/en/plugins"><img src="https://img.shields.io/badge/Claude_Code-plugin-cba6f7" alt="Claude Code plugin"></a>
  <a href="https://github.com/MuhmdRaouf/huddle/actions/workflows/ci.yml"><img src="https://github.com/MuhmdRaouf/huddle/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a>
</p>

<p align="center">Built with ❤️ for the Claude Code community.</p>

# qwen

Routes Alibaba Qwen models through huddle-router inside Claude Code. Pick
`qwen3.8-max` or `qwen3.8-flash` in `/model`, or hand work to their agents by name; every
`claude-*` request still reaches Anthropic untouched.

This plugin depends on [`huddle`](../../plugin/README.md): install it first,
run `/huddle:setup` and restart Claude Code once.

## Commands

| Command | What it does |
|---|---|
| `/qwen:setup` | Check the key with Alibaba Cloud, add the `qwen*` lane to huddle-router and the models to `/model` |
| `/qwen:usage` | Requests, tokens and estimated cost per model, today and the last 7 and 30 days |
| `/qwen:board` | What delegated jobs are doing now |
| `/qwen:review` | Review a delegated job and accept it or return it with feedback |
| `/qwen:remove` | Take the lane, the `/model` entries and the stored key back out |

## Models

| Model | Agent | Upstream |
|---|---|---|
| `qwen3.8-max` | `qwen:qwen3.8-max` | `dashscope-intl.aliyuncs.com` |
| `qwen3.8-flash` | `qwen:qwen3.8-flash` | `dashscope-intl.aliyuncs.com` |

## Quick start

1. Install huddle and this plugin:

   ```text
   /plugin install huddle@muhmdraouf
   /huddle:setup
   /plugin install qwen-plugin-cc@muhmdraouf
   ```

2. Restart Claude Code once, if `/huddle:setup` asked for it.
3. Get a key from [Alibaba Cloud](https://modelstudio.console.alibabacloud.com/?tab=playground#/api-key). Then run `/qwen:setup`.
4. Pick `qwen3.8-max` in `/model`.

## Key

The key comes from `QWEN_API_KEY` or `DASHSCOPE_API_KEY`, `~/.config/qwen-plugin-cc/env` or
`huddle provider qwen key`, which reads hidden stdin. It is stored in the OS
keystore: the macOS Keychain, or the Secret Service on Linux. It never travels
in a chat, an argument or an output line. The huddle dashboard shows whether a
key is stored, never the key.

## Remove

Run `/qwen:remove`, then `/plugin uninstall qwen-plugin-cc@muhmdraouf`. The other
providers keep serving.

## License

[GPL-3.0-or-later](LICENSE).
