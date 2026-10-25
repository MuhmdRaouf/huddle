// keyboard.ts — the on-screen keyboard's height, read off the visual viewport. iOS keeps the
// layout viewport (and every fixed box in it) as tall as the screen while the keyboard is up and
// shrinks only the visual viewport, so what the layout holds below the visible part is the
// height a box pinned to the bottom must rise by to stay in sight.

import { useEffect, useState } from "preact/hooks";

/** The three numbers the inset is read from: the layout viewport's height, the visual
 *  viewport's height and how far the page scrolled the visual one down inside the layout. */
export type ViewportBox = { innerHeight: number; height: number; offsetTop: number };

/** The height the keyboard covers at the layout viewport's bottom, in whole pixels. */
export const keyboardInset = (v: ViewportBox): number =>
  Math.max(0, Math.round(v.innerHeight - v.height - v.offsetTop));

/** The keyboard's height now, followed through the visual viewport's resize and scroll; zero
 *  where the browser has no visual viewport. */
export function useKeyboardInset(): number {
  const [inset, setInset] = useState(0);
  useEffect(() => {
    const vv = window.visualViewport;
    if (!vv) return;
    const read = (): void =>
      setInset(
        keyboardInset({ innerHeight: window.innerHeight, height: vv.height, offsetTop: vv.offsetTop }),
      );
    read();
    vv.addEventListener("resize", read);
    vv.addEventListener("scroll", read);
    return () => {
      vv.removeEventListener("resize", read);
      vv.removeEventListener("scroll", read);
    };
  }, []);
  return inset;
}
