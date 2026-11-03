// layout.ts — the viewport seam: the three layouts the 768 and 1200 px lines cut (phone under
// 768, tablet to 1199, desktop from 1200) and the hook that follows a viewport across them.
// One component tree renders all three; only the chrome pieces the layout draws differ. The two
// lines are the theme's `tablet` and `desk` breakpoints (web/ui/src/theme.css), written in rem.

import { useEffect, useState } from "preact/hooks";

/** The three layouts: phone (< 768 px), tablet (768 to 1199), desktop (>= 1200). */
export type Layout = "phone" | "tablet" | "desktop";

/** The query that answers true from 768 px up. */
export const TABLET_QUERY = "(min-width: 48rem)";

/** The query that answers true from 1200 px up. */
export const DESKTOP_QUERY = "(min-width: 75rem)";

/** The query that answers true under 768 px, the phone. */
export const PHONE_QUERY = "(max-width: 47.9375rem)";

/** The layout two breakpoint answers cut. A viewport past 1200 is past 768 too. */
export function layoutOf(tablet: boolean, desktop: boolean): Layout {
  return desktop ? "desktop" : tablet ? "tablet" : "phone";
}

/** How many panes a tab with a list and an item draws: the phone draws one and pushes the item,
 *  the tablet and the desktop draw the list beside the item. */
export function panesOf(layout: Layout): "one" | "two" {
  return layout === "phone" ? "one" : "two";
}

/** The layout the browser's viewport holds right now. */
const read = (): Layout =>
  layoutOf(window.matchMedia(TABLET_QUERY).matches, window.matchMedia(DESKTOP_QUERY).matches);

/** The layout, followed across the two breakpoints: a resize that crosses one re-renders. */
export function useLayout(): Layout {
  const [layout, setLayout] = useState(read);
  useEffect(() => {
    const tablet = window.matchMedia(TABLET_QUERY);
    const desktop = window.matchMedia(DESKTOP_QUERY);
    const on = (): void => setLayout(layoutOf(tablet.matches, desktop.matches));
    tablet.addEventListener?.("change", on);
    desktop.addEventListener?.("change", on);
    return () => {
      tablet.removeEventListener?.("change", on);
      desktop.removeEventListener?.("change", on);
    };
  }, []);
  return layout;
}
