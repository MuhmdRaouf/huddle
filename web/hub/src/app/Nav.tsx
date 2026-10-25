// Nav.tsx — the five tabs (Now, Channels, Radar, Kanban, Settings) as each layout draws them: the
// phone's bar pinned to the bottom edge, the tablet's 72 px icon rail, the desktop's left sidebar
// with the brand on top. Every one is the `Main` navigation and carries the needs-you count on Now,
// coloured by the one badge rule. Every colour comes from the tokens.

import { Badge } from "@huddle/ui/badge.tsx";
import { Icon as SharedIcon } from "@huddle/ui/icons.tsx";
import type { JSX } from "preact";
import { Icon } from "../icons.tsx";
import type { NeedItem } from "../registry.ts";
import { TABS, type Tab } from "./router.ts";
import { useVersion } from "./TopBar.tsx";

/** What a needs-you badge shows: how many wait on the owner, and the state its colour names. */
export type NeedsBadge = { n: number; tone: "needs-you" | "turn-done" };

/** What the badge counts: the open channel's inbox items (questions, approvals, paused sessions,
 *  blocked tasks, alerts), the registry's sessions that wait on the owner, and the registry's
 *  needs-you items, which say what each session waits for (null until the first read answers). */
export type NeedsCount = { inbox: number; sessions: number; items: readonly NeedItem[] | null };

/** The one badge rule: red (needs-you) while anything counted blocks work (a permission, a
 *  question, an inbox item), orange (turn-done) when every open item is a finished turn. Items the
 *  hub has not read yet count as blocking, so an ask never shows in the calm colour. */
export function needsBadge({ inbox, sessions, items }: NeedsCount): NeedsBadge {
  const open = (items ?? []).filter((x) => x.state === "open");
  const turnsOnly = inbox === 0 && open.length > 0 && open.every((x) => x.kind === "turn");
  return { n: inbox + sessions, tone: turnsOnly ? "turn-done" : "needs-you" };
}

/** The needs-you count in its state's colour; nothing when nobody needs the owner. */
export function TabBadge({ needs }: { needs: NeedsBadge }): JSX.Element | null {
  return needs.n > 0 ? <Badge tone={needs.tone}>{needs.n}</Badge> : null;
}

/** A tab's focus ring, on the accent. */
const RING = "outline-none focus-visible:ring-2 focus-visible:ring-[var(--ui-accent)]";

/** A rail or sidebar tab's colours: the current one on the raised surface, the rest muted. */
const tone = (on: boolean, current: string): string =>
  on
    ? `bg-[var(--ui-surface-2)] ${current}`
    : "text-[var(--ui-muted)] hover:bg-[var(--ui-surface-2)] hover:text-[var(--ui-text)]";

/** One tab's mark: the shared glyphs for the three the design draws, the settings sunburst and
 *  the kanban columns from the owner UI's own set. */
function TabGlyph({ id, class: c }: { id: Tab; class?: string | undefined }): JSX.Element {
  return id === "settings" ? (
    <Icon name="sparkle" class={c} />
  ) : id === "kanban" ? (
    <Icon name="columns" class={c} />
  ) : (
    <SharedIcon name={id} class={c} />
  );
}

/** The phone's tab bar: five 52 px targets, pinned to the viewport's bottom over the inset. */
export function BottomNav({ tab, needs }: { tab: Tab; needs: NeedsBadge }): JSX.Element {
  return (
    <nav
      data-nav="bottom"
      aria-label="Main"
      class="ui-tabbar border-t border-[var(--ui-line)] bg-[var(--ui-surface)]"
    >
      {TABS.map(([id, label, href]) => (
        <a
          key={id}
          href={href}
          data-tab={id}
          aria-current={tab === id ? "page" : undefined}
          class={`relative flex min-h-[52px] flex-col items-center justify-center gap-1 rounded-[var(--ui-radius)] text-[11px] ${RING} ${
            tab === id ? "font-semibold text-[var(--ui-accent)]" : "text-[var(--ui-muted)]"
          }`}
        >
          <TabGlyph id={id} class="size-[22px]" />
          <span>{label}</span>
          {id === "now" ? (
            <span class="absolute top-1 left-[54%]">
              <TabBadge needs={needs} />
            </span>
          ) : null}
        </a>
      ))}
    </nav>
  );
}

/** The tablet's icon rail: the five tabs as 52 px labelled icons, Settings at the bottom. */
export function IconRail({ tab, needs }: { tab: Tab; needs: NeedsBadge }): JSX.Element {
  return (
    <nav
      data-nav="rail"
      aria-label="Main"
      class="flex w-[72px] shrink-0 flex-col items-center gap-2 border-r border-[var(--ui-line)] bg-[var(--ui-surface)] py-[18px]"
    >
      {TABS.map(([id, label, href]) => (
        <a
          key={id}
          href={href}
          data-tab={id}
          aria-label={label}
          title={label}
          aria-current={tab === id ? "page" : undefined}
          class={`relative flex size-[52px] items-center justify-center rounded-[14px] ${RING} ${
            id === "settings" ? "mt-auto" : ""
          } ${tone(tab === id, "text-[var(--ui-accent)]")}`}
        >
          <TabGlyph id={id} class="size-6" />
          {id === "now" ? (
            <span class="absolute top-1 right-1">
              <TabBadge needs={needs} />
            </span>
          ) : null}
        </a>
      ))}
    </nav>
  );
}

/** The desktop's sidebar: the brand with the server's version over the five tabs, as 44 px rows
 *  of icon and label, Settings at the bottom. */
export function SideNav({ tab, needs }: { tab: Tab; needs: NeedsBadge }): JSX.Element {
  const v = useVersion();
  return (
    <aside
      data-sidebar=""
      class="ui-sidebar shrink-0 border-r border-[var(--ui-line)] bg-[var(--ui-surface)]"
    >
      <a
        href="#/channels"
        class={`mb-3 flex min-h-[var(--ui-tap)] shrink-0 items-center gap-2.5 rounded-[var(--ui-radius)] px-3 text-base font-semibold ${RING}`}
      >
        <SharedIcon name="channels" class="size-5 text-[var(--ui-accent)]" />
        <span>Huddle</span>
        <Badge>{`v${v}`}</Badge>
      </a>
      <nav data-nav="side" aria-label="Main" class="flex min-h-0 flex-1 flex-col gap-1">
        {TABS.map(([id, label, href]) => (
          <a
            key={id}
            href={href}
            data-tab={id}
            aria-current={tab === id ? "page" : undefined}
            class={`flex min-h-[var(--ui-tap)] items-center gap-3 rounded-[var(--ui-radius)] px-3 text-sm ${RING} ${
              id === "settings" ? "mt-auto" : ""
            } ${tone(tab === id, "font-semibold text-[var(--ui-text)]")}`}
          >
            <TabGlyph id={id} class="size-5 shrink-0" />
            <span class="min-w-0 flex-1 truncate">{label}</span>
            {id === "now" ? <TabBadge needs={needs} /> : null}
          </a>
        ))}
      </nav>
    </aside>
  );
}
