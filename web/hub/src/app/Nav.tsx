// Nav.tsx — the five tabs (Now, Channels, Radar, Kanban, Settings) as each layout draws them: the
// phone's bar pinned to the bottom edge, the tablet's 72 px icon rail, the desktop's left sidebar
// with the brand on top. Every one is the `Main` navigation and carries the needs-you count on Now.

import { Icon as SharedIcon } from "@huddle/ui/icons.tsx";
import type { JSX } from "preact";
import { Icon } from "../icons.tsx";
import { TABS, type Tab } from "./router.ts";
import { useVersion } from "./TopBar.tsx";

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

/** The needs-you count a tab's mark carries; nothing when nobody needs the owner. */
function TabBadge({ n }: { n: number }): JSX.Element | null {
  return n > 0 ? <span class="badge badge-warning badge-sm tnum">{n}</span> : null;
}

/** The phone's tab bar: five 52 px targets, pinned to the viewport's bottom over the inset. */
export function BottomNav({ tab, n }: { tab: Tab; n: number }): JSX.Element {
  return (
    <nav data-nav="bottom" aria-label="Main" class="ui-tabbar border-t hairline bg-base-200">
      {TABS.map(([id, label, href]) => (
        <a
          key={id}
          href={href}
          data-tab={id}
          aria-current={tab === id ? "page" : undefined}
          class={`relative flex min-h-[52px] flex-col items-center justify-center gap-1 rounded-lg text-[11px] outline-none focus-visible:ring-2 focus-visible:ring-primary ${
            tab === id ? "font-semibold text-primary" : "text-base-content/70"
          }`}
        >
          <TabGlyph id={id} class="size-[22px]" />
          <span>{label}</span>
          {id === "now" ? (
            <span class="absolute top-1 left-[54%]">
              <TabBadge n={n} />
            </span>
          ) : null}
        </a>
      ))}
    </nav>
  );
}

/** The tablet's icon rail: the five tabs as 52 px labelled icons, Settings at the bottom. */
export function IconRail({ tab, n }: { tab: Tab; n: number }): JSX.Element {
  return (
    <nav
      data-nav="rail"
      aria-label="Main"
      class="flex w-[72px] shrink-0 flex-col items-center gap-2 border-r hairline bg-base-200 py-[18px]"
    >
      {TABS.map(([id, label, href]) => (
        <a
          key={id}
          href={href}
          data-tab={id}
          aria-label={label}
          title={label}
          aria-current={tab === id ? "page" : undefined}
          class={`relative flex size-[52px] items-center justify-center rounded-[14px] outline-none focus-visible:ring-2 focus-visible:ring-primary ${
            id === "settings" ? "mt-auto" : ""
          } ${
            tab === id
              ? "bg-base-100 text-primary"
              : "text-base-content/70 hover:bg-base-100/60 hover:text-base-content"
          }`}
        >
          <TabGlyph id={id} class="size-6" />
          {id === "now" ? (
            <span class="absolute top-1 right-1">
              <TabBadge n={n} />
            </span>
          ) : null}
        </a>
      ))}
    </nav>
  );
}

/** The desktop's sidebar: the brand with the server's version over the five tabs, as 44 px rows
 *  of icon and label, Settings at the bottom. */
export function SideNav({ tab, n }: { tab: Tab; n: number }): JSX.Element {
  const v = useVersion();
  return (
    <aside data-sidebar="" class="ui-sidebar shrink-0 border-r hairline bg-base-200">
      <a
        href="#/channels"
        class="mb-3 flex min-h-11 shrink-0 items-center gap-2.5 rounded-lg px-3 text-base font-semibold outline-none focus-visible:ring-2 focus-visible:ring-primary"
      >
        <SharedIcon name="channels" class="size-5 text-primary" />
        <span>Huddle</span>
        <span class="badge badge-ghost badge-sm tnum">{`v${v}`}</span>
      </a>
      <nav data-nav="side" aria-label="Main" class="flex min-h-0 flex-1 flex-col gap-1">
        {TABS.map(([id, label, href]) => (
          <a
            key={id}
            href={href}
            data-tab={id}
            aria-current={tab === id ? "page" : undefined}
            class={`flex min-h-11 items-center gap-3 rounded-lg px-3 text-sm outline-none focus-visible:ring-2 focus-visible:ring-primary ${
              id === "settings" ? "mt-auto" : ""
            } ${
              tab === id
                ? "bg-base-100 font-semibold text-base-content"
                : "text-base-content/70 hover:bg-base-100/60 hover:text-base-content"
            }`}
          >
            <TabGlyph id={id} class="size-5 shrink-0" />
            <span class="min-w-0 flex-1 truncate">{label}</span>
            {id === "now" ? <TabBadge n={n} /> : null}
          </a>
        ))}
      </nav>
    </aside>
  );
}
