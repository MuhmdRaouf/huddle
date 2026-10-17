// layout.ts — the viewport seam: the three layouts the 640 and 1024 px lines cut (phone under
// 640, tablet to 1023, desktop from 1024) and the hook that follows a viewport across them.
// One component tree renders all three; only the chrome pieces the layout draws differ.

import { useEffect, useState } from "preact/hooks";

/** The three layouts: phone (< 640 px), tablet (640 to 1023), desktop (>= 1024). */
export type Layout = "phone" | "tablet" | "desktop";

/** The query that answers true from 640 px up. */
export const TABLET_QUERY = "(min-width: 640px)";

/** The query that answers true from 1024 px up. */
export const DESKTOP_QUERY = "(min-width: 1024px)";

/** The layout two breakpoint answers cut. A viewport past 1024 is past 640 too. */
export function layoutOf(tablet: boolean, desktop: boolean): Layout {
  return desktop ? "desktop" : tablet ? "tablet" : "phone";
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
