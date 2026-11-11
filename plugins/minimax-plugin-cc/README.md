<p align="center">
  <img src="../../assets/banners/minimax-plugin-cc.svg" alt="minimax-plugin-cc — MiniMax models in Claude Code, through huddle" width="100%"/>
</p>

<p align="center">
  <a href="https://www.gnu.org/licenses/gpl-3.0"><img src="https://img.shields.io/badge/License-GPL--3.0--or--later-blue.svg" alt="License: GPL-3.0-or-later"></a>
  <a href="https://code.claude.com/docs/en/plugins"><img src="https://img.shields.io/badge/Claude_Code-plugin-cba6f7" alt="Claude Code plugin"></a>
  <a href="https://github.com/MuhmdRaouf/huddle/actions/workflows/ci.yml"><img src="https://github.com/MuhmdRaouf/huddle/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a>
</p>

<p align="center">Built with ❤️ for the Claude Code community.</p>

# minimax

Routes MiniMax models through huddle-router inside Claude Code. Pick
`MiniMax-M3` or `MiniMax-M2.7-highspeed` in `/model`, or hand work to their agents by name; every
`claude-*` request still reaches Anthropic untouched.

This plugin depends on [`huddle`](../../plugin/README.md): install it first,
run `/huddle:setup` and restart Claude Code once.

## Commands

| Command | What it does |
|---|---|
| `/minimax:setup` | Check the key with MiniMax, add the `MiniMax-*` lane to huddle-router and the models to `/model` |
| `/minimax:usage` | Requests, tokens and estimated cost per model, today and the last 7 and 30 days |
| `/minimax:board` | What delegated jobs are doing now |
| `/minimax:review` | Review a delegated job and accept it or return it with feedback |
| `/minimax:remove` | Take the lane, the `/model` entries and the stored key back out |

## Models

| Model | Agent | Upstream |
|---|---|---|
| `MiniMax-M3` | `minimax:MiniMax-M3` | `api.minimax.io` |
| `MiniMax-M2.7-highspeed` | `minimax:MiniMax-M2.7-highspeed` | `api.minimax.io` |

## Quick start

1. Install huddle and this plugin:

   ```text
   /plugin install huddle@muhmdraouf
   /huddle:setup
   /plugin install minimax-plugin-cc@muhmdraouf
   ```

2. Restart Claude Code once, if `/huddle:setup` asked for it.
3. Get a key from [MiniMax](https://platform.minimax.io/console/access). Then run `/minimax:setup`.
4. Pick `MiniMax-M3` in `/model`.

## Key

The key comes from `MINIMAX_API_KEY`, `~/.config/minimax-plugin-cc/env` or
`huddle provider minimax key`, which reads hidden stdin. It is stored in the OS
keystore: the macOS Keychain, or the Secret Service on Linux. It never travels
in a chat, an argument or an output line. The huddle dashboard shows whether a
key is stored, never the key.

## Remove

Run `/minimax:remove`, then `/plugin uninstall minimax-plugin-cc@muhmdraouf`. The other
providers keep serving.

## License

[GPL-3.0-or-later](LICENSE).
