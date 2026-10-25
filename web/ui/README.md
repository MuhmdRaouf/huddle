# Shared UI

**Reusable Preact modules and styles for the huddle dashboards.** This package (`@huddle/ui`) is compiled into
each dashboard in `web/` — `hub`, `radar` and `kanban`. It is private and never installed by plugin users.

| Area                    | What lives here                                              |
| ----------------------- | ------------------------------------------------------------ |
| Design system           | Tokens, the scheme switch and the components built on them.  |
| Navigation and overlays | Tabs, menus, dialogs, slide-overs and toasts.                |
| Data display            | Charts, counters, code and Markdown rendering.               |
| Shared behavior         | Time labels, fuzzy matching and model colors.                |
| Visual styles           | Theme styles and the IBM Plex font dependencies.             |
| Tests                   | Component behavior, keyboard interaction and shared helpers. |

## Use the design system

The design system is plain CSS tokens plus Preact components that draw only from them. A page on it
looks the same on a phone and a desktop, in light and dark, with nothing loaded from the network.

1. Import the two stylesheets after the page's other CSS: `@import "@huddle/ui/tokens.css";` then
   `@import "@huddle/ui/components.css";`.
2. Run `followScheme(document.documentElement, localStorage, matchMedia("(prefers-color-scheme: dark)"))`
   from `theme.ts` once the page mounts. It puts `data-scheme="light|dark"` on `<html>`: the OS's
   choice, or the override `writeScheme` keeps under `huddle:scheme`.
3. Wrap the page in `<main class="ui-page">` and build it from the components:

| Component                  | File              | Use it for                                            |
| -------------------------- | ----------------- | ----------------------------------------------------- |
| `Button`                   | `button.tsx`      | Actions: primary, secondary, ghost, danger; switches  |
| `Card`                     | `card.tsx`        | One block of content with a title and actions         |
| `List`, `ListItem`         | `list.tsx`        | Rows: sessions, channels, needs-you items             |
| `Sheet`                    | `sheet.tsx`       | Detail over the page: bottom on a phone, side on wide |
| `Tabs` (`class="ui-tabs"`) | `tabs.tsx`        | Switching views inside a page                         |
| `Table`                    | `table.tsx`       | Data that scrolls inside itself, never the page       |
| `Badge`                    | `badge.tsx`       | A short status word                                   |
| `ChartFrame`               | `chart-frame.tsx` | The title, value and frame around a chart             |
| `Field`                    | `field.tsx`       | A labelled input with a hint or an error              |
| `Switch`                   | `switch.tsx`      | One setting turned on or off                          |
| `RadioGroup`               | `radio-group.tsx` | One choice of a few, each with an optional help line  |

For anything the components do not cover, use the tokens directly, never a raw colour or size:

- **Colour:** `--ui-bg`, `--ui-surface`, `--ui-surface-2`, `--ui-line`, `--ui-line-strong`, `--ui-muted`,
  `--ui-text`; the accent `--ui-accent` with `--ui-on-accent` for text on it.
- **Status:** `ok` healthy or done, `warn` needs a look, `fail` broken or denied, `info` news. Each has
  `--ui-<tone>` for marks, `--ui-<tone>-soft` for fills and `--ui-<tone>-ink` for text.
- **States:** `needs-you` (a blocking ask, red), `turn-done` (a finished turn, orange), `working`,
  `idle`, `failed` and `cancelled`, each with the same mark, `-soft` and `-ink` trio, taken from a tone
  or a neutral.
- **Flavour:** `writeFlavour` keeps `catppuccin` or `plain` under `huddle:flavour`; `followScheme` and
  `applyFlavour` put `data-flavour="catppuccin"` on `<html>`, which remaps the colour tokens to
  Catppuccin latte (light) and mocha (dark).
- **Spacing:** `--ui-space-1` to `--ui-space-6` are 4, 8, 12, 16, 24 and 32 px.
- **Shape:** `--ui-radius-sm`, `--ui-radius`, `--ui-radius-lg` and `--ui-radius-full`.
- **Type:** `--ui-text-xs` to `--ui-text-xl`, `--ui-font`, and `--ui-font-mono` (or the `ui-mono`
  class) for commands and numbers.
- **Touch:** `--ui-tap` (44 px) is the smallest a control may be; `--ui-safe-top` and `--ui-safe-bottom`
  pad a pinned bar.

Icons are Lucide shapes bundled in `icons.tsx`: add the paths of a new icon there, never a package.
The hub's `#/dev/tokens` page shows every token and component; `web/e2e/tokens.spec.ts` checks it on
the `iphone` and `desktop` Playwright projects, with axe, and `test/theme-contrast.test.ts` keeps
every ink at 4.5:1 and every mark at 3:1 in light, dark, latte and mocha.

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
