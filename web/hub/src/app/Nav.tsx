// Nav.tsx — the phone's tab bar (Home, Sessions, Channels, Radar, Boards) pinned to the bottom
// edge, the needs-you badge on Home coloured by the one badge rule, and the health dot the avatar
// carries. The tablet and the desktop list the same five tabs in the top bar (TopBar.tsx). Every
// colour comes from the tokens.

import { Badge } from "@huddle/ui/badge.tsx";
import type { JSX } from "preact";
import { Icon, type IconName } from "../icons.tsx";
import type { NeedItem } from "../registry.ts";
import { type NavTab, TABS, type Tab } from "./router.ts";

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

/** The needs-you count as a disc in its state's colour; nothing when nobody needs the owner. */
export function TabBadge({ needs }: { needs: NeedsBadge }): JSX.Element | null {
  return needs.n > 0 ? (
    <Badge tone={needs.tone} solid>
      {needs.n}
    </Badge>
  ) : null;
}

/** A red dot that says a part of the hub is failing; nothing while every part is well. */
export function HealthDot({ healthy }: { healthy: boolean }): JSX.Element | null {
  if (healthy) return null;
  return (
    <>
      <span
        data-health-dot=""
        class="inline-block size-2.5 shrink-0 rounded-full border-2 border-[var(--ui-surface)] bg-[var(--ui-fail)]"
        aria-hidden="true"
      />
      <span class="sr-only">A part of the hub is failing</span>
    </>
  );
}

/** A tab's focus ring, on the accent. */
const RING = "outline-none focus-visible:ring-2 focus-visible:ring-[var(--ui-accent)]";

/** Each tab's mark as the phone canvas draws it: a house, a speech bubble, a hash, a gauge and
 *  board columns. */
const TAB_ICON: Readonly<Record<NavTab, IconName>> = {
  home: "home",
  sessions: "chat",
  channels: "hash",
  radar: "gauge",
  kanban: "boards",
};

/** One tab's mark. */
function TabGlyph({ id, class: c }: { id: NavTab; class?: string | undefined }): JSX.Element {
  return <Icon name={TAB_ICON[id]} class={c} />;
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
          {id === "home" ? (
            <span class="absolute top-1 left-[54%]">
              <TabBadge needs={needs} />
            </span>
          ) : null}
        </a>
      ))}
    </nav>
  );
}
