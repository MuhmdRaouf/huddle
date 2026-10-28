// chip.tsx — a small rounded label: a status or a tag that only shows, or a filter the user turns
// on and off. A display chip is a span; a filter chip is a button that keeps 28 px of chip under a
// --ui-tap hit area under a finger. `onClick` is required when `pressed` is given: a toggle that
// answers no click is a dead control. A chip carries any `data-*` attribute it is given, so a page
// or a test can find it.

import type { ComponentChildren, JSX } from "preact";

/** The chip's colour: neutral, a status, or the accent. */
export type ChipTone = "neutral" | "info" | "ok" | "warn" | "fail" | "accent";

/** What a chip shows and whether it toggles. */
export type ChipProps = {
  children: ComponentChildren;
  tone?: ChipTone | undefined;
  /** A 6 px dot in the tone's colour before the text; beats an icon. */
  dot?: boolean | undefined;
  /** A leading icon, 14 px; the dot wins when both are asked for. */
  icon?: ComponentChildren | undefined;
  /** Given: the chip is a toggle button carrying the state in aria-pressed. */
  pressed?: boolean | undefined;
  onClick?: (() => void) | undefined;
  /** The data attributes a page or a test finds the chip by. */
  [data: `data-${string}`]: string | undefined;
};

/** One chip. */
export function Chip({
  children,
  tone = "neutral",
  dot = false,
  icon,
  pressed,
  onClick,
  ...data
}: ChipProps): JSX.Element {
  const cls = `ui-chip ui-chip-${tone}`;
  const mark = dot ? <span class="ui-chip-dot" aria-hidden="true" /> : icon;
  if (pressed === undefined) {
    return (
      <span {...data} class={cls}>
        {mark}
        {children}
      </span>
    );
  }
  return (
    <button {...data} type="button" class={`${cls} ui-chip-toggle`} aria-pressed={pressed} onClick={onClick}>
      {mark}
      {children}
    </button>
  );
}
