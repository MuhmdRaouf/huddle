# Shared UI

**Reusable Preact modules and styles for the huddle dashboards.** This package (`@huddle/ui`) is compiled into
each dashboard in `web/` — `hub`, `radar` and `kanban`. It is private and never installed by plugin users.

| Area                    | What lives here                                              |
| ----------------------- | ------------------------------------------------------------ |
| Navigation and overlays | Tabs, menus, dialogs, slide-overs and toasts.                |
| Data display            | Charts, counters, code and Markdown rendering.               |
| Shared behavior         | Time labels, fuzzy matching and model colors.                |
| Visual styles           | Theme styles and the IBM Plex font dependencies.             |
| Tests                   | Component behavior, keyboard interaction and shared helpers. |

## Build the dashboards

From the repository root:

```sh
bun install          # once per clone; one lockfile over web/*
bun run check        # every web package: vp check, tests with coverage, no it.todo left
bun run build        # rebuild every page into internal/*/web/dist (committed)
```

Consumers import these sources and fonts directly; each also has its own locked dependencies.

## Make a shared change

1. Change the shared module and its behavior tests.
2. Run `bun run check` here (or from the root, which runs every web package).
3. Rebuild every dashboard from the repository root with `bun run build`.
4. Include the changed bundles with the source change: the built pages are committed.

## Checks

| Task               | Command from this directory |
| ------------------ | --------------------------- |
| Full package check | `bun run check`             |
| Behavior tests     | `bun run test`              |
| Coverage           | `bun run coverage`          |
| Format             | `bun run format`            |

The one consumer: [hub](../hub), which serves the Huddle, Radar and Kanban pages. The repository README
and `AGENTS.md` at the repository root have the product and the development rules.
