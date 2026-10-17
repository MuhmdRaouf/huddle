# Changelog

## 0.0.1 (2026-10-17)

First release. One Go module, `github.com/MuhmdRaouf/huddle`, holding the three
binaries: `huddle` (the channels hub), `radar` (the zero-token dashboard) and
`kanban` (the board). Every binary takes its version from the root `VERSION`
file, cross-compiles with `CGO_ENABLED=0` and `-trimpath` for darwin and linux
on amd64 and arm64, and ships a `release/v<VERSION>.json` manifest with
SHA-256 digests.

### Routing and dashboards

- `radar` serves the dashboard from its embedded page and ingests hook,
  transcript and router-spool events into one local store.
- `huddle` serves the channels hub with per-channel SQLite stores, MCP and CLI
  access, hooks and passkey-capable webauthn flows.
- `kanban` serves the board from its embedded page and keeps tickets in plain
  files under `.agents/kanban`.

### Setup and keys

- Keys stay in the OS keystore; nothing writes a key to a file or a chat.
- Servers bind to loopback only and never touch ports 18787-18791.

### Platforms

- macOS and Linux, amd64 and arm64, built with CGO off.
