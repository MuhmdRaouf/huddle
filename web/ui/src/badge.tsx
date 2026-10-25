// badge.tsx — a short status word on its tone's tint: neutral, or one of the four status colours,
// which mean the same on every page (ok healthy or done, warn needs a look, fail broken, info news).

import type { ComponentChildren, JSX } from "preact";

/** The badge's colour: neutral or a status. */
export type Tone = "neutral" | "ok" | "warn" | "fail" | "info";

/** One badge. */
export function Badge({
  tone = "neutral",
  children,
}: {
  tone?: Tone;
  children: ComponentChildren;
}): JSX.Element {
  return <span class={`ui-badge ui-badge-${tone}`}>{children}</span>;
}
