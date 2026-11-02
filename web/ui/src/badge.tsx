// badge.tsx — a short status word or count on its tone's tint: neutral, one of the four status
// colours, which mean the same on every page (ok healthy or done, warn needs a look, fail broken,
// info news), or one of the two states a count waits in (needs-you a blocking ask, turn-done a
// finished turn). A count can be solid: the tone's mark as a filled disc with the on-accent ink.

import type { ComponentChildren, JSX } from "preact";

/** The badge's colour: neutral, a status, or the state a waiting count is in. */
export type Tone = "neutral" | "ok" | "warn" | "fail" | "info" | "needs-you" | "turn-done";

/** One badge. */
export function Badge({
  tone = "neutral",
  solid = false,
  children,
}: {
  tone?: Tone;
  /** The tone's mark as a filled disc; only the two count tones have one. */
  solid?: boolean;
  children: ComponentChildren;
}): JSX.Element {
  return <span class={`ui-badge ui-badge-${tone}${solid ? " ui-badge-solid" : ""}`}>{children}</span>;
}
