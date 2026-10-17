![Shared UI: Preact modules for Huddle and Radar](../../assets/ui-banner.svg)

# Shared UI

**Reusable Preact modules and styles for the Huddle and Radar dashboards.** This private package is compiled
into each dashboard. It is not installed by plugin users.

| Area                    | What lives here                                              |
| ----------------------- | ------------------------------------------------------------ |
| Navigation and overlays | Tabs, menus, dialogs, slide-overs and toasts.                |
| Data display            | Charts, counters, code and Markdown rendering.               |
| Shared behavior         | Time labels, fuzzy matching and model colors.                |
| Visual styles           | Theme styles and the IBM Plex font dependencies.             |
| Tests                   | Component behavior, keyboard interaction and shared helpers. |

## Install before building a dashboard

From this directory:

```sh
bun install --frozen-lockfile
bun run check
```

Huddle and Radar import these sources and fonts directly. Install this package before either consumer's build
or test suite. Each consumer also has its own locked dependencies.

## Make a shared change

1. Change the shared module and its behavior tests.
2. Run `bun run check` here.
3. Rebuild both dashboards from the repository root:

   ```sh
   (cd plugins/huddle && bun run build)
   (cd plugins/radar && bun run build)
   ```

4. Run `npm run check:all` from the repository root.
5. Include both dashboards' changed bundles with the source change.

## Checks

| Task               | Command from this directory |
| ------------------ | --------------------------- |
| Full package check | `bun run check`             |
| Behavior tests     | `bun run test`              |
| Coverage           | `bun run coverage`          |
| Format             | `bun run format`            |

Consumer READMEs: [Huddle](../../plugins/huddle/README.md) · [Radar](../../plugins/radar/README.md).
