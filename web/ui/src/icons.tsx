// icons.tsx — the Lucide icons the shared package draws, bundled as their SVG paths so nothing loads
// from the network and only the shapes in use ship: toast alert/check/dismiss/undo and the menu's check
// mark, the three marks the huddle shell's tabs carry, the scheme switch's sun, moon and monitor, and
// the plus of an add action.
import type { JSX } from "preact";

/** The icons the shared overlays and the huddle shell's tabs need. */
export type IconName =
  | "alert"
  | "check"
  | "x"
  | "undo"
  | "now"
  | "channels"
  | "radar"
  | "sun"
  | "moon"
  | "monitor"
  | "plus";

/** Icon name → the svg shapes inside a 24×24 stroke box. */
const P: Record<IconName, readonly JSX.Element[]> = {
  alert: [
    <path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3" />,
    <path d="M12 9v4M12 17h.01" />,
  ],
  x: [<path d="M18 6 6 18M6 6l12 12" />],
  check: [<path d="M20 6 9 17l-5-5" />],
  undo: [<path d="M3 7v6h6" />, <path d="M21 17a9 9 0 0 0-15-6.7L3 13" />],
  now: [<circle cx="12" cy="12" r="8" />, <circle cx="12" cy="12" r="3" />],
  channels: [
    <circle cx="6" cy="12" r="3" />,
    <circle cx="18" cy="6" r="3" />,
    <circle cx="18" cy="18" r="3" />,
    <path d="M9 11l6-4M9 13l6 4" />,
  ],
  radar: [<circle cx="12" cy="12" r="9" />, <circle cx="12" cy="12" r="5" />, <path d="M12 12l6-6" />],
  sun: [
    <circle cx="12" cy="12" r="4" />,
    <path d="M12 2v2M12 20v2m-7.07-17.07 1.41 1.41m11.32 11.32 1.41 1.41M2 12h2m16 0h2M6.34 17.66l-1.41 1.41M19.07 4.93l-1.41 1.41" />,
  ],
  moon: [<path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z" />],
  monitor: [<rect width="20" height="14" x="2" y="3" rx="2" />, <path d="M8 21h8M12 17v4" />],
  plus: [<path d="M5 12h14M12 5v14" />],
};

/** One stroke icon at `class` size, hidden from screen readers (the text around it carries the meaning). */
export function Icon({
  name,
  class: c = "size-4",
}: {
  name: IconName;
  class?: string | undefined;
}): JSX.Element {
  return (
    <svg
      class={`${c} shrink-0`}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      stroke-width="2"
      stroke-linecap="round"
      stroke-linejoin="round"
      aria-hidden="true"
    >
      {P[name]}
    </svg>
  );
}
