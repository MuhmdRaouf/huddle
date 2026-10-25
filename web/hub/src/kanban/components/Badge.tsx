import { Badge as ToneBadge, type Tone } from "@huddle/ui/badge.tsx";
import type { BadgeSpec } from "../types.ts";

/** A priority's tone by its level; P3 and anything else read neutral. */
const PRIORITY_TONE: Readonly<Record<string, Tone>> = { P0: "fail", P1: "warn", P2: "info" };

/** The status tone each kind reads: a stop is fail, a wait is warn, a priority its level. */
function toneOf(badge: BadgeSpec): Tone {
  switch (badge.kind) {
    case "expedite":
    case "blocked":
      return "fail";
    case "waits":
    case "hand":
    case "incomplete":
      return "warn";
    case "holder":
      return "info";
    case "priority":
      return PRIORITY_TONE[badge.label] ?? "neutral";
    default:
      return "neutral";
  }
}

/**
 * One badge: the kind picks its tone, the label is the text (colour never carries the meaning
 * alone). The list item keeps the kind as its b-<kind> class for the page and its tests to find.
 */
export function Badge({ badge }: { badge: BadgeSpec }) {
  return (
    <li class={`card-badge b-${badge.kind}`}>
      <ToneBadge tone={toneOf(badge)}>{badge.label}</ToneBadge>
    </li>
  );
}
