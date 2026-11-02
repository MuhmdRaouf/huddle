// TopBar.tsx — the top bar of the tablet and the desktop (56 px): the brand "huddle", the five tabs
// as links beside it (the current one in the accent, the needs-you count beside Home's label) and
// the avatar at the right end; and the machines line the Sessions list draws. The phone's bar is in
// Nav.tsx, its header in App.tsx, with the shell that draws them. Every colour comes from the tokens.

import type { JSX } from "preact";
import type { LiveStatus } from "../store.ts";
import { AccountMenu } from "./AccountMenu.tsx";
import { type NeedsBadge, TabBadge } from "./Nav.tsx";
import { type Tab, TABS } from "./router.ts";

/** The machines line: the live dot over the machines the registry reports; until one answers,
 *  this machine is the one there is. */
export function MachinesLine({ live, text }: { live: LiveStatus; text?: string | undefined }): JSX.Element {
  const t =
    live === "live"
      ? "Live updates: on"
      : live === "offline"
        ? "Live updates: reconnecting"
        : "Live updates: connecting";
  const dot =
    live === "live"
      ? "bg-[var(--ui-ok)]"
      : live === "offline"
        ? "bg-[var(--ui-warn)]"
        : "bg-[var(--ui-idle)]";
  return (
    <span
      class="flex min-w-0 items-center gap-1.5 text-[13px] text-[var(--ui-muted)]"
      data-machines=""
      role="img"
      aria-label="Machines"
      title={t}
    >
      <span
        class={`size-2 shrink-0 rounded-[var(--ui-radius-full)] ${dot}`}
        data-live={live}
        aria-hidden="true"
      />
      <span class="truncate">{text ?? "This machine"}</span>
    </span>
  );
}

/** A link's focus ring, on the accent. */
const RING = "outline-none focus-visible:ring-2 focus-visible:ring-[var(--ui-accent)]";

/** The whole top bar: the brand, the five tabs and the avatar. */
export function TopBar({
  tab,
  needs,
  healthy,
}: {
  tab: Tab;
  needs: NeedsBadge;
  healthy: boolean;
}): JSX.Element {
  return (
    <header
      data-top-bar=""
      class="flex h-14 min-h-14 shrink-0 items-center gap-1 border-b border-[var(--ui-line)] bg-[var(--ui-surface)] px-5 desk:px-6"
    >
      <a
        href="#/home"
        class={`mr-3 flex min-h-[var(--ui-tap)] items-center rounded-[var(--ui-radius)] px-1 text-base font-bold desk:mr-5 ${RING}`}
      >
        huddle
      </a>
      <nav data-nav="top" aria-label="Main" class="flex min-w-0 items-center gap-1">
        {TABS.map(([id, label, href]) => (
          <a
            key={id}
            href={href}
            data-tab={id}
            aria-current={tab === id ? "page" : undefined}
            class={`flex min-h-[var(--ui-tap)] items-center gap-2 rounded-[var(--ui-radius)] px-3 text-sm ${RING} ${
              tab === id
                ? "font-semibold text-[var(--ui-accent)]"
                : "font-medium text-[var(--ui-muted)] hover:text-[var(--ui-text)]"
            }`}
          >
            {label}
            {id === "home" ? <TabBadge needs={needs} /> : null}
          </a>
        ))}
      </nav>
      <div class="ml-auto">
        <AccountMenu healthy={healthy} />
      </div>
    </header>
  );
}
