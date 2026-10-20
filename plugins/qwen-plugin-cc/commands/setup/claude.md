---
description: Turn qwen on with the built-in setup only, no questions (key check, router, Qwen in /model, agents on Qwen)
allowed-tools: Bash(sh:*)
---

Run with the Bash tool, with `timeout` 600000 (setup may wait up to 9 minutes for the key page):

```bash
sh "${CLAUDE_PLUGIN_ROOT}/dist/run" qwen.js setup
```

Exit 6 means not ready; the report still names every check. Show the report to the user. If something is not ready,
give the fix for each failing line. Write what you add yourself as a short title and bullet points, the action first:

- Alibaba Cloud key missing: setup prints a URL and opens a one-time key page on 127.0.0.1 in the browser. The user
  saves the key there; setup checks it, keeps it in the OS keystore and continues by itself in the same run.
  If setup exits 6 asking to finish in the browser (no key within 9 minutes, or the page ended), show the printed
  URL and run setup again once the user is done. Never ask for the key in the chat, and never read or print a key
  file. Headless Linux without a keyring: set `QWEN_API_KEY` in the environment
  Claude Code starts from.
- `claude` binary missing or too old: install or update Claude Code so `claude` is on `PATH`.
- ping failed: the line says why (an API error such as 401 means the key is wrong or revoked).
- exit 127 with "no huddle release is published; install Go to build it from the marketplace checkout": install
  Go, so the launcher can build the binary of the checkout's own commit.

The `runtime` line names the binary the plugin's bundles run on, with its version and path. The router and its
jobs run on the same one.

Plain `setup` needs no flags. What it does:

- Without a key it opens the key page described above and waits for it; otherwise it asks nothing.
- It checks the key with one minimal request to Alibaba Cloud.
- It records qwen with huddle-router and makes sure the daemon is up: the one router for every provider plugin, on
  127.0.0.1:18786. Each session's huddle MCP server checks huddle-router every 2 s and restarts it after 5 s of
  silence. The huddle plugin's session hook re-applies the setup after a plugin update.
- It adds Qwen 3.8 Max and Qwen 3.8 Flash to `/model` next to Sonnet and Opus, and points Claude Code at the router.
- Picking one of them in `/model` sends those requests through the router to Alibaba Cloud. Every `claude-*` model still goes
  to Anthropic untouched, also while the router restarts or updates itself.

The report leads with the outcome:

- `ready`: when this run changed something it ends with the restart line. Show it, because the current session keeps
  talking to Anthropic until Claude Code restarts, and Qwen 3.8 Max in `/model` and the qwen agents work from the next
  session.
- `not routed`: a proxy or base URL in `settings.json` is the user's own, so nothing reaches huddle-router. Give
  the fix the line names.

If Claude ever can't connect: when part of the router fails, it passes Claude's requests straight to Anthropic, and
the huddle plugin's session hook takes the proxy keys (or the base URL) off a router that cannot come back. Removing the
`env.HTTPS_PROXY` and `env.NODE_EXTRA_CA_CERTS` entries from `~/.claude/settings.json` (or restoring
`~/.claude/settings.json.qwen-backup`) always restores direct Anthropic access.

Uninstalling the plugin: run `/qwen:remove`, then `/plugin uninstall qwen-plugin-cc@muhmdraouf`. Uninstalled first, the
huddle plugin's session hook notices the plugin is gone: it takes its entries out of `settings.json`, puts the
agents' models back, and huddle-router stops routing this provider at its next reload.

`/qwen:remove` (`setup --remove`) undoes all of it: the agents go back to the models they had before setup (as the ledger
recorded them), the `/model` entries and the proxy keys go away (or the base URL, when a proxy of the user's own
kept base-URL mode), and huddle-router stops routing this provider at once (the daemon keeps
running for the providers still set up). The huddle plugin's session hook re-applies the setup after a
plugin update overwrites it.

This is the built-in setup alone: it asks nothing and sets up no other tool. `/qwen:setup` asks what to set up (the
built-in setup only, or also omp, opencode or pi as engines for qwen jobs) and runs that; `/qwen:setup:omp`,
`/qwen:setup:opencode` and `/qwen:setup:pi` each add one tool.
