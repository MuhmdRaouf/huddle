import type { BadgeSpec } from "../types.ts";

/**
 * One badge: the kind picks its colour, the label is the text (colour never carries the meaning
 * alone). Priorities carry their own class too, so P0 and P3 read differently.
 */
export function Badge({ badge }: { badge: BadgeSpec }) {
  const priority = badge.kind === "priority" ? ` ${badge.label.toLowerCase()}` : "";
  return <li class={`badge b-${badge.kind}${priority}`}>{badge.label}</li>;
}
