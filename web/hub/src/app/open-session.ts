// open-session.ts — which session the address opens. Home and Sessions both draw a list and the
// session page; the id after the tab names the session, and no other tab opens one.

import type { Route, Tab } from "./router.ts";

/** The tabs whose address names a session after the tab. */
export const SESSION_TABS: ReadonlySet<Tab> = new Set<Tab>(["home", "sessions"]);

/** The session the route opens: the id after Home or Sessions, null for a list with no pick and
 *  for every other tab. */
export function openSessionOf(route: Pick<Route, "tab" | "sub">): string | null {
  return SESSION_TABS.has(route.tab) ? (route.sub[0] ?? null) : null;
}
